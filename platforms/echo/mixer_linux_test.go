package echo_test

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/platforms/echo"
)

// Clocked by the test, not by Write: accepting PCM must never count as playing.
type mixerPCM struct {
	mu          sync.Mutex
	ring, heard []byte
	closed      bool
	delayErr    error
	writeErr    error
	dropConsume int
}

func (p *mixerPCM) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return 0, errors.New("closed")
	}
	if p.writeErr != nil {
		err := p.writeErr
		p.writeErr = nil
		return 0, err
	}
	p.ring = append(p.ring, b...)
	return len(b), nil
}
func (*mixerPCM) Drain() error { return errors.New("shared clock must not be drained") }
func (p *mixerPCM) Drop() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	// DMA can advance after DELAY but before DROP. These samples were heard
	// and must not be replayed from the earlier delay snapshot.
	n := min(p.dropConsume*4, len(p.ring))
	p.heard = append(p.heard, p.ring[:n]...)
	p.ring = nil
	p.delayErr = nil
	return nil
}
func (p *mixerPCM) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.closed = true
	p.ring = nil
	return nil
}
func (p *mixerPCM) Delay() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.ring) / 4, p.delayErr
}
func (p *mixerPCM) waitBuffered(t *testing.T, frames int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		n, err := p.Delay()
		if err != nil {
			t.Fatal(err)
		}
		if n >= frames {
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("PCM buffered %d frames, want at least %d", n, frames)
		}
		time.Sleep(time.Millisecond)
	}
}
func (p *mixerPCM) consume(n int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	n = min(n*4, len(p.ring))
	p.heard = append(p.heard, p.ring[:n]...)
	p.ring = p.ring[n:]
}
func (p *mixerPCM) heardPCM() []int16 {
	p.mu.Lock()
	defer p.mu.Unlock()
	s := make([]int16, len(p.heard)/2)
	for i := range s {
		s[i] = int16(binary.LittleEndian.Uint16(p.heard[i*2:]))
	}
	p.heard = nil
	return s
}
func fillPCM(n int, l, r int16) []byte {
	b := make([]byte, n*4)
	for i := 0; i < n; i++ {
		binary.LittleEndian.PutUint16(b[i*4:], uint16(l))
		binary.LittleEndian.PutUint16(b[i*4+2:], uint16(r))
	}
	return b
}
func pump(p *mixerPCM, frames int) {
	for frames > 0 {
		time.Sleep(3 * time.Millisecond)
		p.consume(min(120, frames))
		frames -= min(120, frames)
	}
	time.Sleep(4 * time.Millisecond)
}
func TestMixerIndependentFlushDrainGainAndConsumption(t *testing.T) {
	p := &mixerPCM{}
	m := echo.NewMixer(p)
	defer m.Close()
	m.SetVolume(1)
	var played atomic.Uint32
	var stamp atomic.Int64
	fault := make(chan struct{}, 1)
	m.MusicStart(7, func(n uint32, us int64) { played.Add(n); stamp.Store(us) }, func() { fault <- struct{}{} })
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	if err := m.MusicWrite(ctx, 7, fillPCM(4800, 10000, -6000)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if played.Load() != 0 {
		t.Fatal("accepted ALSA frames were reported as consumed")
	}
	voice := make([]int16, 320)
	for i := range voice {
		voice[i] = 1200
	}
	if err := m.Write(ctx, voice); err != nil {
		t.Fatal(err)
	}
	pump(p, 1200)
	if played.Load() == 0 || stamp.Load() <= 0 {
		t.Fatal("no real consumption callback")
	}
	mixed := false
	for _, v := range p.heardPCM() {
		if v >= 11198 && v <= 11202 {
			mixed = true
		}
	}
	if !mixed {
		t.Fatal("voice did not mix independently with music")
	}
	// This driver cannot preserve a shared DMA clock across voice cancellation.
	// Fence music rather than replaying uncertain frames or claiming consumption.
	m.Flush()
	select {
	case <-fault:
	default:
		t.Fatal("DMA drop did not request music resynchronization")
	}
	if err := m.MusicWrite(ctx, 7, fillPCM(1, 10000, -6000)); !errors.Is(err, context.Canceled) {
		t.Fatal("accepted stale epoch after DMA drop")
	}
	m.MusicStart(8, nil)
	if err := m.MusicWrite(ctx, 8, fillPCM(4800, 10000, -6000)); err != nil {
		t.Fatal(err)
	}
	p.heardPCM()
	pump(p, 1440)
	for i, v := range p.heardPCM() {
		if v == 0 { // bounded startup silence is not replayed voice/music
			continue
		}
		want := int16(10000)
		if i%2 == 1 {
			want = -6000
		}
		if v != want {
			t.Fatalf("fresh stream contains old voice: sample %d got %d want %d", i, v, want)
		}
	}
	// Voice drains while music keeps playing, without invoking shared PCM Drain.
	if err := m.Write(ctx, voice); err != nil {
		t.Fatal(err)
	}
	drained := make(chan error, 1)
	go func() { drained <- m.Drain(ctx) }()
	select {
	case <-drained:
		t.Fatal("voice drained before hardware progress")
	case <-time.After(10 * time.Millisecond):
	}
	pump(p, 2400)
	select {
	case err := <-drained:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("music prevented voice drain")
	}
	if played.Load() >= 4800 {
		t.Fatal("test ran out of music before independent voice drain")
	}
	// Volume and duck gain compose; current user gain survives release.
	m.MusicStart(8, nil)
	if err := m.MusicWrite(ctx, 8, fillPCM(4800, 10000, -6000)); err != nil {
		t.Fatal(err)
	}
	p.waitBuffered(t, 960)
	m.SetVoiceActive(true)
	m.MusicVolume(50, false)
	p.heardPCM()
	// Retire the existing 20-ms DMA queue, then the full 20-ms gain ramp.
	pump(p, 960+960)
	p.heardPCM()
	pump(p, 240)
	samples := p.heardPCM()
	if len(samples) != 480 {
		t.Fatalf("ducked PCM samples %d want 480", len(samples))
	}
	for i, v := range samples {
		want := int16(1000)
		if i%2 == 1 {
			want = -600
		}
		if v != want {
			t.Fatalf("ducked gain %d want %d", v, want)
		}
	}
	m.MusicStart(9, nil)
	if err := m.MusicWrite(ctx, 9, fillPCM(4800, 10000, -6000)); err != nil {
		t.Fatal(err)
	}
	p.waitBuffered(t, 960)
	m.MusicVolume(35, false)
	m.SetVoiceActive(false)
	pump(p, 960+960) // queued ducked audio plus the release ramp
	p.heardPCM()
	pump(p, 240)
	samples = p.heardPCM()
	if len(samples) != 480 {
		t.Fatalf("released PCM samples %d want 480", len(samples))
	}
	for i, v := range samples {
		want := int16(3500)
		if i%2 == 1 {
			want = -2100
		}
		if v != want {
			t.Fatalf("voice release restored stale gain: %d want %d", v, want)
		}
	}
	m.MusicVolume(100, true)
	m.SetVoiceActive(true)
	m.SetVoiceActive(false)
	p.heardPCM()
	pump(p, 960) // gain changes do not rewrite the bounded 20-ms DMA window
	p.heardPCM()
	pump(p, 480)
	for _, v := range p.heardPCM() {
		if v != 0 {
			t.Fatal("duck release cleared user mute")
		}
	}
}

func TestMixerStopPrivacyAndEpochCannotAutoplay(t *testing.T) {
	p := &mixerPCM{}
	m := echo.NewMixer(p)
	defer m.Close()
	m.SetVolume(1)
	ctx := context.Background()
	var old atomic.Uint32
	m.MusicStart(1, func(n uint32, _ int64) { old.Add(n) })
	if err := m.MusicWrite(ctx, 1, fillPCM(4800, 5000, -9000)); err != nil {
		t.Fatal(err)
	}
	pump(p, 480)
	p.heardPCM()
	m.SetMusicPrivacy(true)
	m.SetMusicPrivacy(false)
	m.SetVoiceActive(false)
	pump(p, 480)
	for _, v := range p.heardPCM() {
		if v != 0 {
			t.Fatal("privacy release restored music")
		}
	}
	consumed := old.Load()
	pump(p, 240)
	if old.Load() != consumed {
		t.Fatal("discarded epoch reported consumption")
	}
	m.MusicStart(2, nil)
	if err := m.MusicWrite(ctx, 1, fillPCM(1, 200, 300)); !errors.Is(err, context.Canceled) {
		t.Fatal("old stream accepted")
	}
	m.MusicEnd(1)
	if err := m.MusicWrite(ctx, 2, fillPCM(4800, 5000, -9000)); err != nil {
		t.Fatal(err)
	}
	pump(p, 480)
	for _, v := range p.heardPCM() {
		if v != 0 {
			t.Fatal("new stream cleared stop latch")
		}
	}
	m.PlayMusic()
	pump(p, 1440)
	s := p.heardPCM()
	if len(s) == 0 || s[len(s)-2] != 5000 || s[len(s)-1] != -9000 {
		t.Fatal("explicit Play did not resume stereo music")
	}
	m.StopMusic()
	m.SetVoiceActive(true)
	m.SetVoiceActive(false)
	p.heardPCM()
	pump(p, 480)
	for _, v := range p.heardPCM() {
		if v != 0 {
			t.Fatal("voice completion cleared Stop")
		}
	}
	m.MusicEnd(2)
}

func TestMixerVoiceInterpolationRejectsImagingAndFailsClosed(t *testing.T) {
	p := &mixerPCM{}
	m := echo.NewMixer(p)
	defer m.Close()
	m.SetVolume(1)
	samples := make([]int16, 1600)
	for i := range samples {
		samples[i] = int16(10000 * math.Sin(2*math.Pi*1000*float64(i)/16000))
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := m.Write(ctx, samples); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- m.Drain(ctx) }()
	pump(p, 5500)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	s := p.heardPCM()
	var signal, image complex128
	// Ignore filter startup/tail. DFT bins derived from the input, not the filter.
	for i := 200; i < 4200; i++ {
		v := float64(s[2*i])
		a := 2 * math.Pi * float64(i) / 48000
		signal += complex(v*math.Cos(a*1000), v*math.Sin(a*1000))
		image += complex(v*math.Cos(a*15000), v*math.Sin(a*15000))
	}
	power := func(v complex128) float64 { return real(v)*real(v) + imag(v)*imag(v) }
	if power(signal) < 1e12 || power(image)/power(signal) > 1e-5 {
		t.Fatalf("voice imaging rejection <50dB: signal=%v image=%v", signal, image)
	}
	p.mu.Lock()
	p.delayErr = errors.New("route lost")
	p.mu.Unlock()
	select {
	case <-m.Done():
	case <-time.After(time.Second):
		t.Fatal("lost idle output not reported")
	}
	if err := m.Write(ctx, []int16{1}); err == nil {
		t.Fatal("accepted audio after route loss")
	}
}

func TestMixerUnderrunRecoversAndFencesUncertainAudio(t *testing.T) {
	for _, where := range []string{"delay", "write"} {
		t.Run(where, func(t *testing.T) {
			p := &mixerPCM{}
			m := echo.NewMixer(p)
			defer m.Close()
			m.SetVolume(1)
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			fault := make(chan struct{}, 1)
			var played atomic.Uint32
			m.MusicStart(1, func(n uint32, _ int64) { played.Add(n) }, func() { fault <- struct{}{} })
			if err := m.MusicWrite(ctx, 1, fillPCM(1000, 1234, -4321)); err != nil {
				t.Fatal(err)
			}
			voice := make([]int16, 1600)
			if err := m.Write(ctx, voice); err != nil {
				t.Fatal(err)
			}
			drained := make(chan error, 1)
			go func() { drained <- m.Drain(ctx) }()
			time.Sleep(20 * time.Millisecond)
			p.mu.Lock()
			if where == "delay" {
				p.delayErr = syscall.EPIPE
			} else {
				p.writeErr = syscall.EPIPE
				p.ring = nil // hardware exhausted before the next WRITE
			}
			p.mu.Unlock()
			select {
			case <-fault:
			case <-ctx.Done():
				t.Fatal("underrun did not fence music")
			}
			if err := <-drained; !errors.Is(err, context.Canceled) {
				t.Fatalf("uncertain voice drain reported success: %v", err)
			}
			select {
			case <-m.Done():
				t.Fatalf("recoverable underrun killed mixer: %v", m.Err())
			default:
			}
			before := played.Load()
			pump(p, 1200)
			if played.Load() != before {
				t.Fatal("discarded frames reported as played")
			}
			if err := m.MusicWrite(ctx, 1, fillPCM(1, 1, 2)); !errors.Is(err, context.Canceled) {
				t.Fatal("stale music accepted")
			}
			m.MusicStart(2, nil)
			if err := m.MusicWrite(ctx, 2, fillPCM(1000, 2000, -3000)); err != nil {
				t.Fatal(err)
			}
			if err := m.Write(ctx, []int16{100, 200}); err != nil {
				t.Fatal("fresh voice failed", err)
			}
			go func() { drained <- m.Drain(ctx) }()
			pump(p, 2200)
			if err := <-drained; err != nil {
				t.Fatal("fresh voice did not drain after recovery", err)
			}
			found := false
			for _, v := range p.heardPCM() {
				found = found || v == -3000
			}
			if !found {
				t.Fatal("fresh stream did not play after recovery")
			}
		})
	}
}

func TestMixerDropDoesNotReplayDMARace(t *testing.T) {
	p := &mixerPCM{dropConsume: 16}
	m := echo.NewMixer(p)
	defer m.Close()
	m.SetVolume(1)
	fault := make(chan struct{}, 1)
	var played atomic.Uint32
	m.MusicStart(1, func(n uint32, _ int64) { played.Add(n) }, func() { fault <- struct{}{} })
	ctx := context.Background()
	b := fillPCM(4800, 0, 0)
	for i := 0; i < 4800; i++ {
		binary.LittleEndian.PutUint16(b[4*i:], uint16(i+100))
	}
	if err := m.MusicWrite(ctx, 1, b); err != nil {
		t.Fatal(err)
	}
	voice := make([]int16, 320)
	if err := m.Write(ctx, voice); err != nil {
		t.Fatal(err)
	}
	time.Sleep(15 * time.Millisecond)
	m.Flush()
	select {
	case <-fault:
	default:
		t.Fatal("uncertain shared clock was retained")
	}
	pump(p, 1200)
	s := p.heardPCM()
	if len(s) < 32 || s[0] != 100 || s[30] != 115 {
		t.Fatalf("probe did not hear raced DMA frames: %v", s)
	}
	for _, v := range s[32:] {
		if v != 0 {
			t.Fatalf("uncertain music replayed after DROP: %d", v)
		}
	}
	if played.Load() != 0 {
		t.Fatal("uncertain DROP frames counted as consumed")
	}
}

func TestMixerCombinedSignalRespectsPhysicalCeiling(t *testing.T) {
	for _, polarity := range []int16{32767, -32768} {
		p := &mixerPCM{}
		m := echo.NewMixer(p)
		m.SetVolume(.25)
		m.SetVoiceActive(true)
		m.MusicStart(1, nil)
		ctx := context.Background()
		if err := m.MusicWrite(ctx, 1, fillPCM(4800, polarity, polarity)); err != nil {
			t.Fatal(err)
		}
		voice := make([]int16, 1600)
		for i := range voice {
			voice[i] = polarity
		}
		if err := m.Write(ctx, voice); err != nil {
			t.Fatal(err)
		}
		pump(p, 6000)
		samples := p.heardPCM()
		m.Close()
		peak := 0
		for _, sample := range samples {
			v := int(sample)
			if v < 0 {
				v = -v
			}
			peak = max(peak, v)
		}
		if peak != 8192 {
			t.Fatalf("combined source/filter peak %d; absolute quarter-scale ceiling 8192", peak)
		}
	}
}

func TestMixerGainChangesDoNotDropMusicClock(t *testing.T) {
	p := &mixerPCM{}
	m := echo.NewMixer(p)
	defer m.Close()
	fault := make(chan struct{}, 1)
	var played atomic.Uint32
	m.MusicStart(1, func(n uint32, _ int64) { played.Add(n) }, func() { fault <- struct{}{} })
	ctx := context.Background()
	if err := m.MusicWrite(ctx, 1, fillPCM(4800, 3000, -7000)); err != nil {
		t.Fatal(err)
	}
	pump(p, 1200)
	for _, gain := range []float64{.25, 0, .1} {
		m.SetVolume(gain)
		m.MusicVolume(35, gain == 0)
	}
	if err := m.MusicWrite(ctx, 1, fillPCM(1, 1, 2)); err != nil {
		t.Fatal("gain change fenced timed stream", err)
	}
	pump(p, 240)
	select {
	case <-fault:
		t.Fatal("gain change dropped DMA clock")
	default:
	}
	if played.Load() == 0 {
		t.Fatal("music clock did not advance")
	}
}
