// Package biscuit assembles the source-backed Echo Dot (2nd gen) wiring. Hardware
// behavior is not verified on an Encore unit. See platforms/linux/NOTICE for sources.
package biscuit

import (
	"context"
	"errors"
	"fmt"
	"log"
	"math"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/farooqu/encore-satellite/backends/incubator"
	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/platforms/linux"
	"github.com/farooqu/encore-satellite/platforms/linux/alsa"
)

const (
	speakerStartupSteps = 60
	speakerMaxSteps     = 120
	speakerMaxPercent   = speakerMaxSteps * 100.0 / 120
	// 40 ms covers the observed ~29 ms refill-to-DELAY-error interval with
	// ~11 ms nominal margin, within the 4096-frame playback ring.
	speakerLeadFrames = 1920
)

type speaker struct {
	*linux.Mixer
	volumeMu      sync.Mutex
	volume        int32
	outputMuted   bool
	volumeChanged chan struct{}
	volumeAt      time.Time
}

func newSpeaker(pcm linux.TimedPCM) *speaker {
	s := &speaker{Mixer: linux.NewMixer(pcm, speakerLeadFrames), volume: speakerStartupSteps, volumeChanged: make(chan struct{}, 1)}
	s.Mixer.SetVolume(float64(speakerStartupSteps) / 120)
	return s
}

// Untuned 0..120 software range: silence..unity, independently starting at 50%.
// Unity is the mixer's no-amplification ceiling, not a qualified acoustic limit.
// State and real mixer gain change under one lock, never CAS followed by a
// potentially reordered gain write. Network delivery stays off the key path.
func (s *speaker) adjust(delta int32) {
	s.volumeMu.Lock()
	defer s.volumeMu.Unlock()
	s.volume = int32(min(speakerMaxSteps, max(0, int64(s.volume)+int64(delta))))
	s.outputMuted = false
	s.applyVolume()
}

func (s *speaker) outputState() (float32, bool) {
	s.volumeMu.Lock()
	defer s.volumeMu.Unlock()
	return float32(s.volume) * 100 / 120, s.outputMuted
}

func (s *speaker) volumeFeedback() volumeFeedback {
	s.volumeMu.Lock()
	defer s.volumeMu.Unlock()
	return volumeFeedback{steps: s.volume, muted: s.outputMuted, at: s.volumeAt}
}

func (s *speaker) setOutputVolume(percent float32) error {
	if math.IsNaN(float64(percent)) || percent < 0 || percent > speakerMaxPercent {
		return fmt.Errorf("dot: speaker volume must be from 0 to %g percent", float64(speakerMaxPercent))
	}
	s.volumeMu.Lock()
	defer s.volumeMu.Unlock()
	// Match the physical keys' 1/120 steps; report the applied value, not the
	// unquantized request. Setting volume does not release output mute.
	s.volume = int32(math.Round(float64(percent) * 120 / 100))
	s.applyVolume()
	return nil
}

func (s *speaker) setOutputMute(muted bool) {
	s.volumeMu.Lock()
	defer s.volumeMu.Unlock()
	s.outputMuted = muted
	s.applyVolume()
}

// Called with volumeMu held. Output mute is independent of mic privacy and
// music Stop: neither applying gain nor unmuting releases either safety latch.
func (s *speaker) applyVolume() {
	gain := float64(s.volume) / 120
	if s.outputMuted {
		gain = 0
	}
	s.Mixer.SetVolume(gain)
	s.volumeAt = time.Now()
	select {
	case s.volumeChanged <- struct{}{}:
	default:
	}
}

// AveragePCM equally averages signed little-endian packed24 microphones 0–6,
// excluding playback references 7/8. One division by 7*256 produces PCM16,
// truncating toward zero without dropping each microphone's low bits first.
// There is no steering, AEC, gain adjustment or buffered history.
func AveragePCM(b []byte) ([]int16, error) {
	if len(b) == 0 || len(b)%27 != 0 || len(b)/27 > audio.MaxSamples {
		return nil, errors.New("dot: invalid nine-channel packed24 block")
	}
	out := make([]int16, len(b)/27)
	for i := range out {
		var sum int32 // Seven full-scale signed 24-bit values fit in int32.
		for mic := range 7 {
			sum += packed24(b[i*27+mic*3:])
		}
		out[i] = int16(sum / (7 * 256))
	}
	return out, nil
}

func packed24(p []byte) int32 {
	v := int32(uint32(p[0]) | uint32(p[1])<<8 | uint32(p[2])<<16)
	return v << 8 >> 8
}

func configureMixer(m interface {
	SetEnum(string, string) error
	SetInt(string, uint32) error
}) error {
	// Select Flat before OpenPlayback loads the coefficients; Biscuit has
	// one speaker, not the Radar crossover the codec defaults to.
	for _, c := range []struct{ name, value string }{
		{"Ext_Speaker_Amp_Switch", "Off"}, {"Speaker Codec Profile", "Flat"},
		{"Audio_DacMux_Setting", "On"},
		{"Ignore Ramp Up", "Off"},
	} {
		if err := m.SetEnum(c.name, c.value); err != nil {
			return err
		}
	}
	for _, c := range []struct {
		name  string
		value uint32
	}{
		{"HP Driver Gain Volume", 6}, {"HPL Output Mixer L_DAC Switch", 1}, {"HPR Output Mixer R_DAC Switch", 1},
	} {
		if err := m.SetInt(c.name, c.value); err != nil {
			return err
		}
	}
	for _, c := range []struct{ name, value string }{
		{"Audio_DacMux_Setting", "Off"}, {"Right Channel Only", "On"},
	} {
		if err := m.SetEnum(c.name, c.value); err != nil {
			return err
		}
	}
	return nil
}

func configureMic(m *alsa.Mixer) error {
	for _, adc := range []string{"A", "B", "C", "D"} {
		for _, c := range []struct {
			suffix string
			value  uint32
		}{
			{"DIF1_L Input Gain", 0}, {"DIF1_R Input Gain", 0},
			{fmt.Sprintf("Left Ip Select ADC_%s DIF1_L switch", adc), 1},
			{fmt.Sprintf("Right Ip Select ADC_%s DIF1_R switch", adc), 1},
			{"Left Mute", 0}, {"Right Mute", 0},
		} {
			name := "ADC_" + adc + " " + c.suffix
			if err := m.SetInt(name, 1-c.value); err != nil {
				return err
			}
			if err := m.SetInt(name, c.value); err != nil {
				return err
			}
		}
		name := "ADC_" + adc + " MICPGA Volume Ctrl"
		if err := m.SetInt(name, 39); err != nil {
			return err
		}
		if err := m.SetInt(name, 40); err != nil {
			return err
		}
	}
	return nil
}

// capture stays on this goroutine: nonblocking acquisition is gated before AND
// after the syscall. On any privacy change/error the kernel ring is discarded;
// unmute starts a fresh stream, never delivers audio buffered before the cut.
type capturePCM interface {
	Start() error
	Read([]byte) (int, error)
	Rewind() error
}

type captureControls interface {
	Lookup(string) (alsa.Control, error)
	Get(alsa.Control) ([]uint32, error)
}

// One scalar and its observation time, never samples or direction. The capture
// owner publishes only after its final privacy/loss/delivery checks. A single
// atomic word makes reads coherent without blocking acquisition or inference.
type captureMeter struct {
	origin time.Time
	value  atomic.Uint64
}

type captureReading struct {
	level float64
	at    time.Time
	ready bool
}

func (m *captureMeter) clear() { m.value.Store(0) }

func (m *captureMeter) observe(samples []int16, now time.Time) {
	var sum float64
	for _, sample := range samples {
		x := float64(sample)
		sum += x * x
	}
	level := uint64(math.Sqrt(sum/float64(len(samples))) * 65535 / 32768)
	stamp := uint64(now.Sub(m.origin)/time.Millisecond) + 1
	m.value.Store(stamp<<16 | level)
}

func (m *captureMeter) snapshot(now time.Time) captureReading {
	v := m.value.Load()
	if v == 0 {
		return captureReading{}
	}
	at := m.origin.Add(time.Duration((v>>16)-1) * time.Millisecond)
	// Fresh silent PCM is healthy; missing PCM is not silence. This is only
	// visual freshness (12.5 normal blocks), never a new audio/network watchdog.
	if age := now.Sub(at); age < 0 || age > 250*time.Millisecond {
		return captureReading{}
	}
	return captureReading{level: float64(v&65535) / 65535, at: at, ready: true}
}

func captureLossMonitor(m captureControls) (func() (bool, error), error) {
	var controls []alsa.Control
	for _, name := range []string{"FPGA Overruns", "Kernel Overruns"} {
		c, err := m.Lookup(name)
		if err != nil {
			return nil, fmt.Errorf("dot: capture loss control %q: %w", name, err)
		}
		if c.Type != alsa.TypeInteger || c.Count != 1 {
			return nil, fmt.Errorf("dot: invalid capture loss control %q", name)
		}
		controls = append(controls, c)
	}
	return func() (bool, error) {
		lost := false
		for _, c := range controls {
			values, err := m.Get(c)
			if err != nil {
				return false, fmt.Errorf("dot: capture loss control %q: %w", c.Name, err)
			}
			if len(values) != 1 {
				return false, fmt.Errorf("dot: invalid capture loss report %q", c.Name)
			}
			// Both driver getters read AND clear under the same mutex as
			// their increments. Every nonzero report is new loss, not a
			// cumulative total to compare with the preceding report.
			lost = lost || values[0] != 0
		}
		return lost, nil
	}, nil
}

func capture(ctx context.Context, pcm capturePCM, loss func() (bool, error), gate *linux.Privacy, voice *incubator.Server, meter *captureMeter, probe *referenceProbe, aec *loopbackAEC, ready func(), privacy ...func(bool)) error {
	engine := voice.Engine
	defer meter.clear()
	if probe != nil {
		defer probe.reset()
	}
	if aec != nil {
		defer func() { *aec = loopbackAEC{} }()
	}
	started, discontinuity, announced := false, false, false
	var arm *referenceArm // capture owner's per-block view; no pre-arm samples
	stopObservation := func(err error) {
		if arm != nil {
			arm.stop(err)
			probe.reset()
			arm, probe = nil, nil
		}
	}
	reportedUnavailable := false
	setPrivacy := func(muted bool, err error) {
		if muted {
			stopObservation(errReferencePrivacy)
			if aec != nil && started {
				*aec = loopbackAEC{}
			}
			engine.SetMuted(true)
			meter.clear()
		}
		for _, notify := range privacy {
			notify(muted)
		}
		if err != nil && !reportedUnavailable {
			log.Printf("dot: privacy state unavailable; microphone capture stays off while unreadable: %v", err)
			reportedUnavailable = true
		}
	}
	b := make([]byte, 320*27)
	defer clear(b)
	var sequence uint64
	opened := time.Now()
	for ctx.Err() == nil {
		if probe != nil {
			arm = probe.arm.Load()
			if arm != nil && arm.ctx.Err() != nil {
				stopObservation(context.Cause(arm.ctx))
			}
		}
		var n int
		var readErr error
		// Control reads do not acquire microphone audio. Require them even
		// during muted startup, before advertising capture readiness.
		lost, lossErr := loss()
		muted, privacyErr := gate.Check(func() {
			if lost || lossErr != nil {
				return
			}
			engine.SetMuted(false)
			if !started {
				readErr = pcm.Start()
				started = readErr == nil
				if readErr != nil {
					return
				}
			}
			n, readErr = pcm.Read(b)
			// The driver may report loss while copying this very block,
			// even when Read succeeds. Never publish before this check.
			lost, lossErr = loss()
		})
		setPrivacy(muted, privacyErr)
		if muted {
			clear(b)
		}
		if muted && started {
			if err := pcm.Rewind(); err != nil {
				return err
			}
			started, discontinuity = false, true
		}
		if lossErr != nil {
			clear(b)
			stopObservation(lossErr)
			engine.SetMuted(true)
			return lossErr
		}
		if readErr != nil && !errors.Is(readErr, syscall.EAGAIN) && !errors.Is(readErr, syscall.EINTR) {
			if !errors.Is(readErr, alsa.ErrOverrun) {
				stopObservation(readErr)
				engine.SetMuted(true)
				return readErr
			}
			lost = true
		}
		if lost {
			clear(b)
			meter.clear()
			stopObservation(errReferenceLoss)
			if aec != nil {
				*aec = loopbackAEC{}
			}
			engine.SetMuted(true) // invalidate active speech immediately
			if err := pcm.Rewind(); err != nil {
				return err
			}
			started, discontinuity = false, true
			sequence++ // also leave a gap for queued model inference
			n = 0
		}
		if n > 0 && readErr == nil {
			samples, err := AveragePCM(b[:n])
			if err != nil {
				stopObservation(err)
				engine.SetMuted(true)
				return err
			}
			var captureErr error
			var report referenceReport
			var reportReady bool
			var capturedAt time.Duration
			if arm != nil && arm.ctx.Err() == nil && !discontinuity {
				capturedAt = time.Since(opened)
				report, reportReady = probe.observe(b[:n], samples, time.Since(arm.started))
			}
			if aec != nil && !discontinuity {
				if capturedAt == 0 {
					capturedAt = time.Since(opened)
				}
				// Do the DSP outside the privacy lock; the second check
				// discards the output and resets the filter if mute raced it.
				phase := engine.Snapshot().Phase
				aec.process(b[:n], samples, phase != "tentative" && phase != "listening")
			}
			muted, privacyErr = gate.Check(func() {
				engine.SetMuted(false)
				frameAt := time.Since(opened)
				if capturedAt != 0 {
					frameAt = capturedAt // do not include DSP or diagnostic scan time
				}
				captureErr = voice.Capture(audio.Frame{Samples: samples, Sequence: sequence, CapturedAt: frameAt, Discontinuity: discontinuity})
				if captureErr == nil && !discontinuity && ctx.Err() == nil {
					meter.observe(samples, time.Now())
					if reportReady && arm.ctx.Err() == nil {
						probe.publish(arm, report)
					}
				} else {
					meter.clear()
				}
			})
			if captureErr != nil {
				stopObservation(captureErr)
				if aec != nil {
					*aec = loopbackAEC{}
				}
			}
			setPrivacy(muted, privacyErr)
			clear(samples)
			clear(b)
			if muted {
				if err := pcm.Rewind(); err != nil {
					return err
				}
				started, discontinuity = false, true
			} else {
				sequence++
				discontinuity = captureErr != nil
				if !announced {
					ready()
					announced = true
				}
			}
		}
		// The app can be ready without sampling when the privacy gate denies
		// capture. This is not microphone readiness; keep polling the gate.
		if muted && !announced {
			ready()
			announced = true
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Millisecond):
		}
	}
	return nil
}
