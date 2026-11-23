package biscuit

import (
	"context"
	"embed"
	"encoding/binary"
	"errors"
	"log"
	"os"
	"sync"
	"time"

	"github.com/farooqu/encore-satellite/core/presentation"
	"github.com/farooqu/encore-satellite/internal/esphomepairing"
	"github.com/farooqu/encore-satellite/platforms/linux/alsa"
)

//go:embed onboarding/*.wav
var onboardingClips embed.FS

type setupCue string

const (
	improvReady      setupCue = "improv-ready"
	wifiConnected    setupCue = "wifi-connected"
	haReady          setupCue = "ha-ready"
	pairingOpen      setupCue = "pairing-open"
	pairingComplete  setupCue = "pairing-complete"
	wifiFailed       setupCue = "wifi-failed"
	appFailed        setupCue = "app-failed"
	pairingTimeout   setupCue = "pairing-timeout"
	setupFlash                = 2 * time.Second
	setupSpeechLimit          = 7 * time.Second
)

func (cue setupCue) stream(ctx context.Context, write func([]int16) error) error {
	wav, err := onboardingClips.ReadFile("onboarding/" + string(cue) + ".wav")
	if err != nil {
		return err
	}
	// The bundled renders have a canonical PCM16 mono 16 kHz WAV header.
	for pcm := wav[44:]; len(pcm) > 0; {
		if err = ctx.Err(); err != nil {
			return err
		}
		var samples [1024]int16
		n := min(len(samples), len(pcm)/2)
		for i := range n {
			samples[i] = int16(binary.LittleEndian.Uint16(pcm[2*i:]))
		}
		if err = write(samples[:n]); err != nil {
			return err
		}
		pcm = pcm[2*n:]
	}
	return nil
}

func openSpeakerPCM() (*alsa.Playback, error) {
	// Keep the wake threshold 5 ms below the refill target.
	return alsa.OpenPlayback(0, 23, alsa.Config{Channels: 2, Rate: 48000, Format: alsa.FormatS16_LE, Bits: 16, PeriodSize: speakerPeriodFrames, Periods: speakerPeriods, PlaybackWakeLead: speakerLeadFrames - 240})
}

// Before pairing there is no normal mixer or capture. Own only the speaker,
// and return it before the normal runtime opens ALSA.
func playSetupCue(ctx context.Context, cue setupCue) error {
	mixer, err := alsa.OpenMixer(0)
	if err != nil {
		return err
	}
	defer mixer.Close()
	defer mixer.SetEnum("Ext_Speaker_Amp_Switch", "Off")
	if err = configureMixer(mixer); err != nil {
		return err
	}
	pcm, err := openSpeakerPCM()
	if err != nil {
		return err
	}
	player := newSpeaker(pcm)
	defer player.Close()
	if err = mixer.SetEnum("Ext_Speaker_Amp_Switch", "On"); err != nil {
		return err
	}
	if err = cue.stream(ctx, func(p []int16) error { return player.Write(ctx, p) }); err != nil {
		return err
	}
	return player.Drain(ctx)
}

type setupEvent struct {
	cue setupCue
	at  time.Time
}

func (e setupEvent) frame(now time.Time, muted bool, options RingOptions) [36]byte {
	if muted {
		return RingFrame(presentation.State{Muted: true}, 0, 0, options)
	}
	var frame [36]byte
	_, cap := ringDrive(presentation.State{}, options)
	level := byte(max(2, cap/4))
	count, color := 12, [3]byte{level, level / 2, 0}
	age := max(0, now.Sub(e.at))
	switch e.cue {
	case improvReady:
	case haReady:
		color = [3]byte{level, level, level}
	case pairingOpen:
		count = max(1, 12-int(age/(esphomepairing.Window/12)))
		color = [3]byte{level, level, level}
	case wifiConnected, pairingComplete:
		if age >= setupFlash {
			return frame
		}
		if !options.ReducedMotion {
			count = min(12, 1+int(age/(setupFlash/24)))
		}
		color = [3]byte{0, level, 0}
	case wifiFailed, pairingTimeout, appFailed:
		if age < setupFlash {
			// Visually distinct from steady full-ring mic mute.
			count, color = 6, [3]byte{level, 0, 0}
		} else if e.cue == pairingTimeout {
			color = [3]byte{level, level, level}
		} else if e.cue == appFailed {
			return frame
		}
	default:
		return frame
	}
	for i := range count {
		copy(frame[3*i:], color[:])
	}
	return frame
}

// A setup transition publishes the latest cue without waiting on ALSA or sysfs.
// A new cue cancels stale speech. The independent ring worker can show privacy
// or countdown changes even while the speaker is busy.
type setupFeedback struct {
	mu    sync.Mutex
	event setupEvent
	stop  context.CancelFunc
	wake  chan struct{}
}

func (f *setupFeedback) emit(cue setupCue) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.event = setupEvent{cue, time.Now()}
	if f.stop != nil {
		f.stop()
	}
	select {
	case f.wake <- struct{}{}:
	default:
	}
}

func (f *setupFeedback) speak(ctx context.Context, play func(context.Context, setupCue) error) {
	var previous setupEvent
	for {
		select {
		case <-ctx.Done():
			return
		case <-f.wake:
		}
		f.mu.Lock()
		e := f.event
		if e == previous || ctx.Err() != nil {
			f.mu.Unlock()
			continue
		}
		previous = e
		clipCtx, stop := context.WithTimeout(ctx, setupSpeechLimit)
		f.stop = stop
		f.mu.Unlock()
		if err := play(clipCtx, e.cue); err != nil && !errors.Is(err, context.Canceled) {
			log.Printf("dot: setup speech %s: %v", e.cue, err)
		}
		stop()
	}
}

func startSetupFeedback(parent context.Context, muted func() bool) (*setupFeedback, func()) {
	ctx, cancel := context.WithCancel(parent)
	f := &setupFeedback{wake: make(chan struct{}, 1)}
	var workers sync.WaitGroup
	workers.Go(func() { f.speak(ctx, playSetupCue) })
	workers.Go(func() {
		tick := time.NewTicker(50 * time.Millisecond)
		defer tick.Stop()
		ring := ringRenderer{write: writeScaledRing}
		options := RingOptions{Brightness: 50}
		if policy, err := loadRingSettings(ringSettingsPath); err == nil {
			options = policy.snapshot()
		}
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-tick.C:
				f.mu.Lock()
				e := f.event
				f.mu.Unlock()
				if e.cue == "" {
					continue
				}
				if !ring.written {
					_ = os.WriteFile(ringPath+"boot_animation", []byte("0"), 0644)
				}
				if err := ring.writeFrame(e.frame(now, muted(), options)); err != nil {
					log.Printf("dot: setup ring: %v", err)
					return
				}
			}
		}
	})
	return f, sync.OnceFunc(func() { cancel(); workers.Wait() })
}

func writeScaledRing(frame []byte) error {
	return os.WriteFile(ringPath+"scaled_frame", scaledRingFrame(frame), 0644)
}

// Startup already failed. Allow the terminal cue to play before exiting; no
// setup, listener, or readiness transition waits for an announcement.
func reportSetupStartupFailure(ctx context.Context, muted func() bool) {
	ctx, cancel := context.WithTimeout(ctx, setupSpeechLimit)
	defer cancel()
	f, stop := startSetupFeedback(ctx, muted)
	defer stop()
	f.emit(appFailed)
	<-ctx.Done()
}
