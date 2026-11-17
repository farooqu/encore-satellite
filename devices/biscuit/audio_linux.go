// Package biscuit assembles the source-backed Echo Dot (2nd gen) wiring. Hardware
// behavior is not verified on an Encore unit. See platforms/linux/NOTICE for sources.
package biscuit

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
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

const privacyState = "/sys/devices/soc/10010000.keypad/amz_privacy/privacy_state"

type speaker struct {
	*linux.Mixer
	volumeMu      sync.Mutex
	volume        int32
	outputMuted   bool
	volumeChanged chan struct{}
	volumeAt      time.Time
}

func newSpeaker(pcm linux.TimedPCM) *speaker {
	s := &speaker{Mixer: linux.NewMixer(pcm), volume: 30, volumeChanged: make(chan struct{}, 1)}
	s.Mixer.SetVolume(30.0 / 120)
	return s
}

// Untuned 0..30 physical software range: silence..quarter scale, initially 25%.
// State and real mixer gain change under one lock, never CAS followed by a
// potentially reordered gain write. Network delivery stays off the key path.
func (s *speaker) adjust(delta int32) {
	s.volumeMu.Lock()
	defer s.volumeMu.Unlock()
	s.volume = int32(min(30, max(0, int64(s.volume)+int64(delta))))
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
	if math.IsNaN(float64(percent)) || percent < 0 || percent > 25 {
		return errors.New("dot: speaker volume must be from 0 to 25 percent")
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

// CenterPCM converts packed signed 24-bit channel 6 only, discarding the low
// eight bits. Channels 7/8 are references, never microphone or AEC streams.
func CenterPCM(b []byte) ([]int16, error) {
	if len(b) == 0 || len(b)%27 != 0 || len(b)/27 > audio.MaxSamples {
		return nil, errors.New("dot: invalid nine-channel packed24 block")
	}
	out := make([]int16, len(b)/27)
	for i := range out {
		out[i] = int16(binary.LittleEndian.Uint16(b[i*27+19:]))
	}
	return out, nil
}

func configureMixer(m *alsa.Mixer) error {
	// Speaker-only route. No headphone switching, vendor DSP or boosted gain.
	for _, c := range []struct{ name, value string }{
		{"Ext_Speaker_Amp_Switch", "Off"}, {"Audio_DacMux_Setting", "On"},
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

func capture(ctx context.Context, pcm capturePCM, loss func() (bool, error), gate *linux.Privacy, voice *incubator.Server, meter *captureMeter, ready func(), privacy ...func(bool)) error {
	engine := voice.Engine
	defer meter.clear()
	setPrivacy := func(muted bool) {
		if muted {
			meter.clear()
		}
		for _, notify := range privacy {
			notify(muted)
		}
	}
	b := make([]byte, 320*27)
	defer clear(b)
	started, discontinuity, announced := false, false, false
	var sequence uint64
	opened := time.Now()
	for ctx.Err() == nil {
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
		setPrivacy(muted || privacyErr != nil)
		if muted {
			clear(b)
			engine.SetMuted(true)
		}
		if muted && started {
			if err := pcm.Rewind(); err != nil {
				return err
			}
			started, discontinuity = false, true
		}
		if privacyErr != nil {
			return errors.New("dot: privacy state unavailable; capture closed")
		}
		if lossErr != nil {
			clear(b)
			engine.SetMuted(true)
			return lossErr
		}
		if readErr != nil && !errors.Is(readErr, syscall.EAGAIN) && !errors.Is(readErr, syscall.EINTR) {
			if !errors.Is(readErr, alsa.ErrOverrun) {
				return readErr
			}
			lost = true
		}
		if lost {
			clear(b)
			meter.clear()
			engine.SetMuted(true) // invalidate active speech immediately
			if err := pcm.Rewind(); err != nil {
				return err
			}
			started, discontinuity = false, true
			sequence++ // also leave a gap for queued model inference
			n = 0
		}
		if n > 0 && readErr == nil {
			samples, err := CenterPCM(b[:n])
			if err != nil {
				return err
			}
			var captureErr error
			muted, privacyErr = gate.Check(func() {
				engine.SetMuted(false)
				captureErr = voice.Capture(audio.Frame{Samples: samples, Sequence: sequence, CapturedAt: time.Since(opened), Discontinuity: discontinuity})
				if captureErr == nil && !discontinuity && ctx.Err() == nil {
					meter.observe(samples, time.Now())
				} else {
					meter.clear()
				}
			})
			setPrivacy(muted || privacyErr != nil)
			if muted {
				engine.SetMuted(true)
			}
			clear(samples)
			clear(b)
			if privacyErr != nil {
				return errors.New("dot: privacy state unavailable; capture closed")
			}
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
		// A muted startup may be ready without sampling the microphones: the
		// prepared PCM, mixer and controls were opened, the physical cut is read.
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
