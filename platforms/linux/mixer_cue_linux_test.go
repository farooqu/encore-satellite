package linux_test

import (
	"context"
	"encoding/binary"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/platforms/linux"
)

// This PCM consumes immediately. Compare nonzero samples so the pump's idle
// silence and scheduling do not become part of the cue's waveform contract.
type cuePCM struct {
	mu      sync.Mutex
	written []byte
}

func (p *cuePCM) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.written = append(p.written, b...)
	return len(b), nil
}
func (*cuePCM) Drop() error         { return nil }
func (*cuePCM) Drain() error        { return nil }
func (*cuePCM) Close() error        { return nil }
func (*cuePCM) Delay() (int, error) { return 0, nil }

func TestMixerCueGainTailAndFollowingVoice(t *testing.T) {
	// An asymmetric last impulse forces the finite tail to be observable. Real
	// voice has a different sign/level so missing tail or contamination differs.
	cue := make([]int16, 320)
	for i := range cue {
		cue[i] = 4000
	}
	cue[len(cue)-1] = -7000
	voice := make([]int16, 113)
	for i := range voice {
		voice[i] = -1200
	}
	render := func(gain float64, useCue bool) []int16 {
		t.Helper()
		p := &cuePCM{}
		m := linux.NewMixer(p)
		defer m.Close()
		m.SetVolume(gain)
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		defer cancel()
		if useCue {
			until := time.Now().Add(time.Second)
			for !m.TryCue(cue) {
				if time.Now().After(until) {
					t.Fatal("idle cue never admitted")
				}
				time.Sleep(time.Millisecond)
			}
			if err := m.Write(ctx, voice); err != nil {
				t.Fatal(err)
			}
		} else {
			// Public voice path is the reference for resampling/gain. Explicit
			// silence independently specifies the required 21-input-sample tail.
			input := append(append(append([]int16{}, cue...), make([]int16, 21)...), voice...)
			if err := m.Write(ctx, input); err != nil {
				t.Fatal(err)
			}
		}
		if err := m.Drain(ctx); err != nil {
			t.Fatal("cue/voice bookkeeping did not drain:", err)
		}
		p.mu.Lock()
		defer p.mu.Unlock()
		var out []int16
		for i := 0; i < len(p.written); i += 2 {
			if v := int16(binary.LittleEndian.Uint16(p.written[i:])); v != 0 {
				out = append(out, v)
			}
		}
		return out
	}
	for _, gain := range []float64{0, .125, .25} {
		got, want := render(gain, true), render(gain, false)
		if !reflect.DeepEqual(got, want) {
			t.Fatalf("cue tail/following voice differed at gain %v: lengths %d/%d", gain, len(got), len(want))
		}
		if gain == 0 {
			if len(got) != 0 {
				t.Fatal("muted cue was audible")
			}
		} else {
			steady := int16(4000 * gain)
			count := 0
			for _, v := range got {
				if v == steady {
					count++
				}
			}
			if count < 100 {
				t.Fatal("cue did not use real master gain", gain, count)
			}
		}
	}
}

func TestMixerCueRejectsBusyPrivacyStopAndClosed(t *testing.T) {
	for _, condition := range []string{"active", "voice", "music", "privacy", "stopped", "closed"} {
		t.Run(condition, func(t *testing.T) {
			m := linux.NewMixer(&mixerPCM{})
			defer m.Close()
			switch condition {
			case "active":
				m.SetVoiceActive(true)
			case "voice":
				if err := m.Write(t.Context(), make([]int16, 800)); err != nil {
					t.Fatal(err)
				}
			case "music":
				m.MusicStart(7, nil)
			case "privacy":
				m.SetMusicPrivacy(true)
			case "stopped":
				m.StopMusic()
			case "closed":
				m.Close()
			}
			if m.TryCue([]int16{4000}) {
				t.Fatal("cue escaped admission gate", condition)
			}
		})
	}
	m := linux.NewMixer(&mixerPCM{})
	defer m.Close()
	for _, samples := range [][]int16{nil, {}, make([]int16, 801)} {
		if m.TryCue(samples) {
			t.Fatal("accepted malformed/oversized cue")
		}
	}
}

type cueLockedPCM struct {
	*cuePCM
	entered, release chan struct{}
	once             sync.Once
}

func (p *cueLockedPCM) Delay() (int, error) {
	p.once.Do(func() { close(p.entered); <-p.release })
	return 0, nil
}

func TestMixerCueNeverWaitsForOutputLock(t *testing.T) {
	p := &cueLockedPCM{cuePCM: &cuePCM{}, entered: make(chan struct{}), release: make(chan struct{})}
	m := linux.NewMixer(p)
	defer m.Close()
	defer close(p.release)
	select {
	case <-p.entered:
	case <-time.After(time.Second):
		t.Fatal("pump did not enter PCM delay")
	}
	result := make(chan bool, 1)
	go func() { result <- m.TryCue([]int16{4000}) }()
	select {
	case accepted := <-result:
		if accepted {
			t.Fatal("accepted cue while output lock busy")
		}
	case <-time.After(100 * time.Millisecond):
		t.Fatal("cue waited for blocked PCM")
	}
}
