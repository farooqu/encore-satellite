package linux_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/core/session"
	"github.com/farooqu/encore-satellite/platforms/linux"
)

type pacedMixerPCM struct {
	mixerPCM
	ready, control   chan struct{}
	checks           atomic.Int32
	wakeCalls        atomic.Int32
	shortWrite       atomic.Int32
	blocked, release chan struct{}
	blockWrite       atomic.Bool
}

func newPacedTestPCM() *pacedMixerPCM {
	return &pacedMixerPCM{ready: make(chan struct{}, 1), control: make(chan struct{}, 1), blocked: make(chan struct{}), release: make(chan struct{})}
}

func (*pacedMixerPCM) PlaybackPaced() bool { return true }
func (p *pacedMixerPCM) WakePlayback() {
	p.wakeCalls.Add(1)
	select {
	case p.control <- struct{}{}:
	default:
	}
}
func (p *pacedMixerPCM) WaitPlayback() (bool, error) {
	select {
	case <-p.control:
		return false, nil
	default:
	}
	select {
	case <-p.control:
		return false, nil
	case <-p.ready:
		return true, nil
	}
}
func (p *pacedMixerPCM) Delay() (int, error) {
	p.checks.Add(1)
	return p.mixerPCM.Delay()
}
func (p *pacedMixerPCM) Write(b []byte) (int, error) {
	if p.blockWrite.Swap(false) {
		close(p.blocked)
		<-p.release
	}
	if frames := p.shortWrite.Swap(0); frames > 0 {
		b = b[:min(int(frames)*4, len(b))]
	}
	return p.mixerPCM.Write(b)
}
func (p *pacedMixerPCM) advance(frames int) {
	p.consume(frames)
	if n, _ := p.mixerPCM.Delay(); n <= 1680 {
		select {
		case p.ready <- struct{}{}:
		default:
		}
	}
}

func TestMixerPacedWaitsForWatermarkAndDrainsAtDMA(t *testing.T) {
	p := newPacedTestPCM()
	m := linux.NewMixer(p, 1920)
	defer m.Close()
	m.SetVolume(1) // software wake primes an otherwise idle fake device
	p.waitBuffered(t, 1920)
	checks := p.checks.Load()
	p.advance(239)
	// Twenty ms spans ten of the old timer's periods, without a ready/control edge.
	time.Sleep(20 * time.Millisecond)
	if p.checks.Load() != checks {
		t.Fatal("owner polled a partly filled ring without a readiness or software wake")
	}
	p.advance(1)
	p.waitBuffered(t, 1920)
	voice := make([]int16, 320)
	for i := range voice {
		voice[i] = 3200
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err := m.Write(ctx, voice); err != nil {
		t.Fatal(err)
	}
	wakes := p.wakeCalls.Load()
	drained := make(chan error, 1)
	go func() { drained <- m.Drain(ctx) }()
	// A sub-threshold clip starts when Drain queues the finite tail.
	for p.wakeCalls.Load() == wakes {
		select {
		case <-ctx.Done():
			t.Fatal("Drain did not release the short clip")
		case <-time.After(time.Millisecond):
		}
	}
	for range 8 { // 1920 originals + 1023 voice & FIR tail; first 1920 were idle
		select {
		case err := <-drained:
			t.Fatalf("Drain completed before all voice frames were heard: %v", err)
		default:
		}
		p.advance(360)
		p.waitBuffered(t, 1920)
	}
	select {
	case err := <-drained:
		t.Fatalf("Drain returned before the FIR tail reached the DMA pointer: %v", err)
	default:
	}
	p.advance(360)
	select {
	case err := <-drained:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("Drain did not observe the consumed FIR tail")
	}
	var steady int
	for _, v := range p.heardPCM() {
		if v == 3200 {
			steady++
		}
	}
	if steady < 1500 || steady > 1920 {
		t.Fatalf("unexpected number of heard plateau samples: %d", steady)
	}
}

func TestMixerPacedUrgentPurgeFencesInFlightWrite(t *testing.T) {
	p := newPacedTestPCM()
	m := linux.NewMixer(p, 1920)
	defer m.Close()
	release := sync.OnceFunc(func() { close(p.release) })
	defer release()
	m.SetVolume(1)
	p.waitBuffered(t, 1920)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	fault := make(chan struct{}, 1)
	m.MusicStart(1, nil, func() { fault <- struct{}{} })
	if err := m.MusicWrite(ctx, 1, fillPCM(4800, 10000, -6000)); err != nil {
		t.Fatal(err)
	}
	voice := make([]int16, 640)
	for i := range voice {
		voice[i] = 3000
	}
	if err := m.Write(ctx, voice); err != nil {
		t.Fatal(err)
	}
	p.blockWrite.Store(true)
	p.advance(480)
	select {
	case <-p.blocked:
	case <-ctx.Done():
		t.Fatal("no in-flight write")
	}
	// Queue voice after the owner prepared its stale write; it may not escape
	// the purge that arrives while the owner is in the fake ALSA call.
	control := make(chan error, 1)
	go func() {
		err := m.Write(ctx, voice[:320])
		m.SetMusicPrivacy(true)
		m.Flush()
		control <- err
	}()
	select {
	case err := <-control:
		if err != nil {
			t.Fatal(err)
		}
	case <-ctx.Done():
		t.Fatal("privacy/Flush waited for in-flight PCM write")
	}
	if m.TryCue([]int16{1}) {
		t.Fatal("cue admitted after privacy fence")
	}
	release()
	select {
	case <-fault:
	case <-ctx.Done():
		t.Fatal("owner did not purge the stale write")
	}
	if err := m.MusicWrite(ctx, 1, fillPCM(1, 1, 2)); !errors.Is(err, context.Canceled) {
		t.Fatal("old epoch survived purge", err)
	}
	p.waitBuffered(t, 1920)
	p.advance(960)
	for _, v := range p.heardPCM() {
		if v != 0 {
			t.Fatal("stale voice or music played after purge")
		}
	}
}

func TestMixerPacedWAVPrerollBeforeSecondCallback(t *testing.T) {
	p := newPacedTestPCM()
	m := linux.NewMixer(p, testMixAhead)
	defer m.Close()
	m.SetVolume(.5)
	p.waitBuffered(t, testMixAhead)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	// Two PCM16/16 kHz WAV callbacks. Keep the second callback behind a
	// channel until the mixer has actually committed the first refill.
	wav := []byte("RIFF$\x05\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\x80\x3e\x00\x00\x00\x7d\x00\x00\x02\x00\x10\x00data\x00\x05\x00\x00")
	wav = append(wav, make([]byte, 1280)...)
	for i := 44; i < len(wav); i += 2 {
		binary.LittleEndian.PutUint16(wav[i:], 12000)
	}
	first, resume, done := make(chan struct{}), make(chan struct{}), make(chan error, 1)
	go func() {
		calls := 0
		done <- audio.ReadWAV(bytes.NewReader(wav), func(samples []int16) error {
			if err := m.Write(ctx, samples); err != nil {
				return err
			}
			calls++
			if calls == 1 {
				close(first)
				select {
				case <-resume:
				case <-ctx.Done():
					return ctx.Err()
				}
			}
			return nil
		})
	}()
	select {
	case <-first:
	case <-ctx.Done():
		t.Fatal("no first WAV callback")
	}
	checkStereoRefill(t, p, 1024, 0, 1024, 0)
	close(resume)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	// Accept only 384 frames on the first write; the retry must play the
	// remaining 640 after it, not replay the FIR startup or omit frames.
	p.shortWrite.Store(384)
	p.advance(1024)
	p.waitBuffered(t, testMixAhead-640)
	p.WakePlayback()
	checkStereoRefill(t, p, 0, 100, 1024, 6000)
	// The 896 remaining voice frames must survive, with only the actual
	// missing 128 frames padded on this refill if no callback arrives.
	checkStereoRefill(t, p, 1024, 0, 896, 6000)
	checkStereoRefill(t, p, 0, 896, 1024, 0)
}

// Inspect the newly accepted tail without advancing the fake DMA clock twice.
func checkStereoRefill(t *testing.T, p *pacedMixerPCM, advance, from, to int, want int16) {
	t.Helper()
	if advance > 0 {
		p.advance(advance)
	}
	p.waitBuffered(t, testMixAhead)
	p.mu.Lock()
	defer p.mu.Unlock()
	for frame := from; frame < to; frame++ {
		i := (testMixAhead - 1024 + frame) * 4
		l, r := int16(binary.LittleEndian.Uint16(p.ring[i:])), int16(binary.LittleEndian.Uint16(p.ring[i+2:]))
		if l != want || r != want {
			t.Fatalf("refill frame %d: got %d/%d, want %d", frame, l, r, want)
		}
	}
}

func TestMixerPacedResumesAfterVoiceStallWithContinuousMusic(t *testing.T) {
	p := newPacedTestPCM()
	m := linux.NewMixer(p, testMixAhead)
	defer m.Close()
	m.SetVolume(1)
	p.waitBuffered(t, testMixAhead)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	m.MusicStart(1, nil)
	if err := m.MusicWrite(ctx, 1, fillPCM(4800, -1000, -1000)); err != nil {
		t.Fatal(err)
	}
	voice := make([]int16, 640)
	for i := range voice {
		voice[i] = 4000
	}
	if err := m.Write(ctx, voice); err != nil {
		t.Fatal(err)
	}
	checkStereoRefill(t, p, 1024, 100, 1024, 3000)
	checkStereoRefill(t, p, 1024, 0, 896, 3000)
	checkStereoRefill(t, p, 0, 896, 1024, -1000)
	checkStereoRefill(t, p, 1024, 0, 1024, -1000) // stall outlasts all voice
	if err := m.Write(ctx, voice[:320]); err != nil {
		t.Fatal(err)
	}
	checkStereoRefill(t, p, 1024, 0, 1024, -1000) // short resume still buffered
	if err := m.Write(ctx, voice[:320]); err != nil {
		t.Fatal(err)
	}
	checkStereoRefill(t, p, 1024, 0, 704, 3000)
	checkStereoRefill(t, p, 0, 704, 1024, 4000) // music ends without affecting the voice
}

func TestMixerPacedBufferedVoiceFences(t *testing.T) {
	for _, name := range []string{"music-stop", "voice-stop", "physical-privacy"} {
		t.Run(name, func(t *testing.T) {
			p := newPacedTestPCM()
			m := linux.NewMixer(p, testMixAhead)
			defer m.Close()
			m.SetVolume(1)
			p.waitBuffered(t, testMixAhead)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			fault := make(chan struct{}, 1)
			m.MusicStart(1, nil, func() { fault <- struct{}{} })
			if err := m.MusicWrite(ctx, 1, fillPCM(2048, -1000, -1000)); err != nil {
				t.Fatal(err)
			}
			voice := make([]int16, 320)
			for i := range voice {
				voice[i] = 4000
			}
			if err := m.Write(ctx, voice); err != nil {
				t.Fatal(err)
			}
			checkStereoRefill(t, p, 1024, 0, 1024, -1000)
			switch name {
			case "music-stop":
				m.StopMusic()
			case "voice-stop":
				m.Flush()
			case "physical-privacy":
				m.SetMusicPrivacy(true)
				m.Flush() // The engine's voice fence when the host mutes capture.
				m.SetMusicPrivacy(false)
			}
			if name != "voice-stop" {
				select {
				case <-fault: // Stop/privacy purged the music DMA.
				case <-ctx.Done():
					t.Fatal("music DMA was not purged")
				}
				p.waitBuffered(t, testMixAhead)
			}
			want := int16(0)
			if name == "music-stop" {
				want = 4000 // This second callback must complete the preserved voice preroll.
			}
			if err := m.Write(ctx, voice); err != nil {
				t.Fatal(err)
			}
			if name == "voice-stop" {
				want = -1000 // Voice stop must not discard trusted music.
			}
			checkStereoRefill(t, p, 1024, 100, 1024, want)
		})
	}
}

func TestMixerPacedShortCueDoesNotWaitForPreroll(t *testing.T) {
	p := newPacedTestPCM()
	m := linux.NewMixer(p, testMixAhead)
	defer m.Close()
	m.SetVolume(1)
	p.waitBuffered(t, testMixAhead)
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	cue := make([]int16, 320)
	for i := range cue {
		cue[i] = 4000
	}
	for !m.TryCue(cue) {
		select {
		case <-ctx.Done():
			t.Fatal("idle cue was not admitted")
		case <-time.After(time.Millisecond):
		}
	}
	// A short following Write clears the cue's tail flag, but cannot hold
	// the already admitted cue until more voice or a Drain arrives.
	if err := m.Write(ctx, []int16{500}); err != nil {
		t.Fatal(err)
	}
	checkStereoRefill(t, p, 1024, 100, 960, 4000)
}

func TestMixerPacedFalseReadinessFailsClosed(t *testing.T) {
	p := newPacedTestPCM()
	m := linux.NewMixer(p, 1920)
	defer m.Close()
	m.SetVolume(1)
	p.waitBuffered(t, 1920)
	checks := p.checks.Load()
	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	for range 3 {
		select {
		case p.ready <- struct{}{}:
		case <-ctx.Done():
			t.Fatal("owner did not process readiness")
		}
	}
	select {
	case <-m.Done():
		if p.checks.Load()-checks != 3 || m.Err() == nil {
			t.Fatal("false readiness was not bounded")
		}
	case <-ctx.Done():
		t.Fatal("owner did not fail closed on repeated readiness at full target")
	}
}

type announcementLink struct{ events []string }

func (*announcementLink) Close() error { return nil }
func (l *announcementLink) Send(c session.Command) error {
	l.events = append(l.events, c.Kind)
	return nil
}

func TestAnnouncementDoesNotCompleteAfterMixerLosesAcceptedPCM(t *testing.T) {
	for _, name := range []string{"before drain", "during drain", "before next write", "blocked source"} {
		t.Run(name, func(t *testing.T) {
			p := newPacedTestPCM()
			m := linux.NewMixer(p, testMixAhead)
			defer m.Close()
			m.SetVolume(1)
			p.waitBuffered(t, testMixAhead)
			e := session.New(m)
			link := &announcementLink{}
			if _, err := e.Attach(link); err != nil {
				t.Fatal(err)
			}
			wantDrain := "not_attempted"
			if name == "during drain" {
				wantDrain = "canceled"
			}
			ctx, cancel := context.WithTimeout(t.Context(), 2*time.Second)
			defer cancel()
			fault := make(chan struct{}, 1)
			m.MusicStart(1, nil, func() { fault <- struct{}{} })
			accepted, resume := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() {
				result, err := e.Announce(ctx, func(out context.Context, write func([]int16) error) error {
					if err := write(make([]int16, 320)); err != nil {
						return err
					}
					close(accepted)
					if name == "blocked source" {
						select {
						case <-out.Done():
							return out.Err()
						case <-ctx.Done():
							return ctx.Err()
						}
					}
					select {
					case <-resume:
					case <-ctx.Done():
						return ctx.Err()
					}
					if name == "before next write" {
						return write([]int16{-1000, 2000})
					}
					return nil
				})
				if result != (session.AnnouncementResult{AcceptedBlocks: 1, AcceptedSamples: 320, Outcome: "playback_underrun", Drain: wantDrain}) {
					t.Error("wrong interruption receipt", result)
				}
				done <- err
			}()
			select {
			case <-accepted:
			case <-ctx.Done():
				t.Fatal("first block was not accepted")
			}
			if name == "during drain" {
				wakes := p.wakeCalls.Load()
				close(resume)
				// Drain queues the filter tail and wakes the owner. The fake
				// clock hasn't advanced, so Drain cannot retire that tail.
				for p.wakeCalls.Load() == wakes {
					select {
					case <-ctx.Done():
						t.Fatal("Drain did not queue its tail")
					case <-time.After(time.Millisecond):
					}
				}
			}
			// The first block is still pending when the owner recovers.
			p.mu.Lock()
			p.delayErr = syscall.EPIPE
			p.mu.Unlock()
			p.WakePlayback()
			select {
			case <-fault:
			case <-ctx.Done():
				t.Fatal("PCM owner did not recover")
			}
			if name != "during drain" {
				close(resume)
			}
			tick := time.NewTicker(time.Millisecond)
			defer tick.Stop()
			for {
				select {
				case err := <-done:
					if !errors.Is(err, audio.ErrPlaybackUnderrun) {
						t.Fatalf("announcement did not report loss of accepted PCM: %v", err)
					}
					if len(link.events) != 1 || link.events[0] != "announcing" {
						t.Fatal("interrupted playback sent unexpected native events", link.events)
					}
					return
				case <-tick.C:
					p.advance(1024)
				case <-ctx.Done():
					t.Fatal("announcement stuck after recovery")
				}
			}
		})
	}
}
