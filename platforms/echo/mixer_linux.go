package echo

import (
	"context"
	"encoding/binary"
	"errors"
	"io"
	"math"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"golang.org/x/sys/unix"

	"github.com/farooqu/encore-satellite/platforms/echo/alsa"
)

// TimedPCM is a single nonblocking 48 kHz stereo PCM16 output. Delay is the
// hardware's outstanding frame count, not a userspace estimate. Calls serialize.
type TimedPCM interface {
	PCM
	Delay() (int, error)
}

const (
	mixBlock      = 240 // 5 ms, aligned to the driver's 16-frame DMA quantum.
	mixAhead      = 960 // Prime/refill up to 20 ms; recover even if this is exhausted.
	voiceCapacity = 3 * 2048
	musicCapacity = 4800 // 100 ms, in addition to bounded companion IPC.
)

type stereo struct{ l, r int16 }
type mixFrame struct {
	voice  float64
	voiced bool
	music  stereo
	epoch  uint64
}

// Mixer owns the one speaker clock. Voice is PCM16/16k mono; music is negotiated
// PCM16/48k stereo. DMA drops are discontinuities: uncertain hardware frames
// are never replayed or reported consumed, and affected music must resynchronize.
type Mixer struct {
	mu                           sync.Mutex
	pcm                          TimedPCM
	wake                         chan struct{}
	stop                         chan struct{}
	done                         chan struct{}
	serial                       chan struct{}
	err                          error
	voice                        []float64
	music                        []stereo
	hardware, pending            []mixFrame
	voicePending                 int
	generation                   uint64
	resample                     interpolator
	tail                         bool
	epoch                        uint64
	played                       func(uint32, int64)
	musicFailed                  func()
	volume                       float64
	musicGain                    float64
	musicMuted, privacy, stopped bool
	active                       atomic.Bool
	duck                         float64
}

func NewMixer(pcm TimedPCM) *Mixer {
	m := &Mixer{pcm: pcm, wake: make(chan struct{}), stop: make(chan struct{}), done: make(chan struct{}), serial: make(chan struct{}, 1), volume: 5.0 / 120, musicGain: 1, duck: 1}
	m.resample.init()
	go m.run()
	return m
}

// SetVoiceActive is safe inside the engine lock: no I/O, waiting or reentry.
func (m *Mixer) SetVoiceActive(active bool) { m.active.Store(active) }

func (m *Mixer) signal() { close(m.wake); m.wake = make(chan struct{}) }

func (m *Mixer) wait(ctx context.Context, generation uint64, ready func() bool) error {
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.err != nil {
			return m.err
		}
		if generation != m.generation {
			return context.Canceled
		}
		if ready() {
			return nil
		}
		wake := m.wake
		m.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-wake:
		}
		m.mu.Lock()
	}
}

func (m *Mixer) voiceOp(ctx context.Context, op func(uint64) error) error {
	m.mu.Lock()
	g := m.generation
	m.mu.Unlock()
	select {
	case m.serial <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-m.serial }()
	m.mu.Lock()
	defer m.mu.Unlock()
	if err := m.wait(ctx, g, func() bool { return true }); err != nil {
		return err
	}
	return op(g)
}

func (m *Mixer) Write(ctx context.Context, samples []int16) error {
	if len(samples) == 0 || len(samples) > 2048 {
		return errors.New("echo: invalid voice block")
	}
	return m.voiceOp(ctx, func(g uint64) error {
		if err := m.wait(ctx, g, func() bool { return len(m.voice)+len(samples)*3 <= voiceCapacity }); err != nil {
			return err
		}
		m.voice = m.resample.append(m.voice, samples)
		m.voicePending += len(samples) * 3
		m.tail = false
		return nil
	})
}

// Drain waits for this voice's last filtered sample to pass the DMA pointer. A
// continuously playing music stream neither delays nor prematurely ends it.
func (m *Mixer) Drain(ctx context.Context) error {
	return m.voiceOp(ctx, func(g uint64) error {
		if !m.tail {
			if err := m.wait(ctx, g, func() bool { return len(m.voice)+63 <= voiceCapacity }); err != nil {
				return err
			}
			m.voice = m.resample.append(m.voice, make([]int16, 21))
			m.voicePending += 63
			m.tail = true
		}
		if err := m.wait(ctx, g, func() bool { return m.voicePending == 0 }); err != nil {
			return err
		}
		m.resample.reset()
		return nil
	})
}

func (m *Mixer) Flush() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generation++
	m.voice = m.voice[:0]
	m.resample.reset()
	m.tail = false
	m.rebuild(true, false)
	m.voicePending = 0
	m.signal()
}

// SetVolume applies the model's conservative physical gain to both contributors.
func (m *Mixer) SetVolume(gain float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.volume = max(0, min(1, gain))
}

// Privacy release deliberately does not release the music Stop latch. Only an
// explicit operator Play may do that, never voice completion or a fresh stream.
func (m *Mixer) SetMusicPrivacy(muted bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if muted == m.privacy {
		return
	}
	m.privacy = muted
	if muted {
		m.stopped = true
		m.silenceMusic()
	}
}
func (m *Mixer) StopMusic() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = true
	m.silenceMusic()
}
func (m *Mixer) PlayMusic() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.privacy {
		m.stopped = false
	}
}
func (m *Mixer) silenceMusic() { m.rebuild(false, true) }

// MusicStart/Write/End implement the companion output contract. The epoch is
// scoped to a child run by its Go bridge; it must not be reused on child restart.
func (m *Mixer) MusicStart(epoch uint64, played func(uint32, int64), failed ...func()) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.music = m.music[:0]
	m.epoch, m.played, m.musicFailed = 0, nil, nil
	m.rebuild(false, true)
	m.epoch, m.played = epoch, played
	m.musicFailed = nil
	if len(failed) != 0 {
		m.musicFailed = failed[0]
	}
	m.signal()
}
func (m *Mixer) MusicWrite(ctx context.Context, epoch uint64, b []byte) error {
	if len(b) == 0 || len(b)%4 != 0 || len(b)/4 > musicCapacity {
		return errors.New("echo: invalid music block")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if m.err != nil {
			return m.err
		}
		if epoch == 0 || m.epoch != epoch {
			return context.Canceled
		}
		if len(m.music)+len(b)/4 <= musicCapacity {
			break
		}
		wake := m.wake
		m.mu.Unlock()
		select {
		case <-ctx.Done():
		case <-wake:
		}
		m.mu.Lock()
	}
	for i := 0; i < len(b); i += 4 {
		m.music = append(m.music, stereo{int16(binary.LittleEndian.Uint16(b[i:])), int16(binary.LittleEndian.Uint16(b[i+2:]))})
	}
	return nil
}
func (m *Mixer) MusicEnd(epoch uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.epoch != epoch {
		return
	}
	m.music = m.music[:0]
	m.epoch, m.played, m.musicFailed = 0, nil, nil
	m.rebuild(false, true)
	m.signal()
}
func (m *Mixer) MusicVolume(volume uint8, muted bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.musicGain, m.musicMuted = float64(min(volume, 100))/100, muted
}

// progress retires only frames the kernel says it consumed. The raw monotonic
// epoch matches SendspinClient::now_us() on Linux. Callbacks must not block.
func (m *Mixer) progress() error {
	delay, err := m.pcm.Delay()
	if err != nil {
		return err
	}
	if delay < 0 || delay > len(m.hardware) {
		return errors.New("echo: invalid PCM delay")
	}
	var ts unix.Timespec
	if err = unix.ClockGettime(unix.CLOCK_MONOTONIC_RAW, &ts); err != nil {
		return err
	}
	n := len(m.hardware) - delay
	count, last := uint32(0), 0
	for i, f := range m.hardware[:n] {
		if f.voiced {
			m.voicePending--
		}
		if f.epoch != 0 && f.epoch == m.epoch {
			count++
			last = i
		}
	}
	if count > 0 && m.played != nil {
		m.played(count, ts.Nano()/1000-int64(n-1-last)*1000000/48000)
	}
	copy(m.hardware, m.hardware[n:])
	m.hardware = m.hardware[:delay]
	if n > 0 {
		m.signal()
	}
	return nil
}

// The driver cannot atomically stop and snapshot DMA. Retire only the last
// known consumed frames, then discard (never replay) the uncertain remainder.
// The music bridge receives a fault and reconnects with a new epoch/clock.
func (m *Mixer) rebuild(voice, music bool) {
	if m.err != nil {
		return
	}
	if err := m.progress(); err != nil {
		m.recover(err)
		return
	}
	drop := false
	for _, f := range m.hardware {
		if (voice && f.voiced) || (music && f.epoch != 0) {
			drop = true
			break
		}
	}
	if drop {
		if err := m.pcm.Drop(); err != nil {
			m.fail(err)
			return
		}
	}
	for _, f := range m.hardware {
		if drop && f.voiced {
			m.generation++
			m.voice = m.voice[:0]
			m.resample.reset()
			m.tail = false
			voice = true
			break
		}
	}
	if drop && m.epoch != 0 {
		m.invalidateMusic()
		music = true
	}
	p := m.pending
	for i := range p {
		if voice {
			p[i].voice, p[i].voiced = 0, false
		}
		if music {
			p[i].music = stereo{}
			p[i].epoch = 0
		}
	}
	m.pending = p
	if drop {
		m.hardware = m.hardware[:0]
	}
	if voice {
		m.voicePending = 0
	}
	m.signal()
}

func (m *Mixer) invalidateMusic() {
	failed := m.musicFailed
	m.epoch, m.played, m.musicFailed = 0, nil, nil
	m.music = m.music[:0]
	if failed != nil {
		failed() // bounded notification only; never reenter the mixer
	}
}

// An underrun loses the clock, not the route. Fence every affected operation,
// prepare an empty stream, and keep the daemon available for fresh audio.
func (m *Mixer) recover(err error) {
	if !errors.Is(err, syscall.EPIPE) && !errors.Is(err, alsa.ErrUnderrun) {
		m.fail(err)
		return
	}
	if err = m.pcm.Drop(); err != nil {
		m.fail(err)
		return
	}
	m.generation++
	m.voice, m.hardware, m.pending = m.voice[:0], m.hardware[:0], m.pending[:0]
	m.voicePending, m.tail = 0, false
	m.resample.reset()
	m.invalidateMusic()
	m.signal()
}

func (m *Mixer) fail(err error) {
	if m.err == nil {
		m.err = err
		_ = m.pcm.Close()
		m.signal()
	}
}

func (m *Mixer) run() {
	defer close(m.done)
	tick := time.NewTicker(2 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-m.stop:
			return
		case <-tick.C:
		}
		m.mu.Lock()
		if m.err != nil {
			m.mu.Unlock()
			return
		}
		if err := m.progress(); err != nil {
			m.recover(err)
			m.mu.Unlock()
			continue
		}
		if len(m.hardware) < mixAhead {
			for len(m.pending) < mixAhead-len(m.hardware) {
				m.fill()
			}
			n := min(mixAhead-len(m.hardware), len(m.pending))
			b := make([]byte, n*4)
			duck := m.duck
			target := 1.0
			if m.active.Load() {
				target = 0.2
			}
			for i, f := range m.pending[:n] {
				// 20 ms full-range gain ramp, independent of user music gain.
				duck += max(-0.8/960, min(0.8/960, target-duck))
				gain := m.musicGain * duck
				if m.musicMuted || m.privacy || m.stopped {
					gain = 0
				}
				for ch, sample := range []int16{f.music.l, f.music.r} {
					v := max(-32768, min(32767, f.voice+float64(sample)*gain)) * m.volume
					binary.LittleEndian.PutUint16(b[i*4+ch*2:], uint16(int16(max(-32768, min(32767, math.Round(v))))))
				}
			}
			written, err := m.pcm.Write(b)
			if written < 0 || written > len(b) || written%4 != 0 {
				err = io.ErrShortWrite
				written = 0
			}
			step := float64(written/4) * 0.8 / 960
			m.duck += max(-step, min(step, target-m.duck))
			m.hardware = append(m.hardware, m.pending[:written/4]...)
			copy(m.pending, m.pending[written/4:])
			m.pending = m.pending[:len(m.pending)-written/4]
			if err != nil && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
				m.recover(err)
			}
		}
		m.mu.Unlock()
	}
}

func (m *Mixer) fill() {
	for i := 0; i < mixBlock; i++ {
		f := mixFrame{}
		if i < len(m.voice) {
			f.voice, f.voiced = m.voice[i], true
		}
		if i < len(m.music) {
			f.music, f.epoch = m.music[i], m.epoch
		}
		m.pending = append(m.pending, f)
	}
	n := min(mixBlock, len(m.voice))
	copy(m.voice, m.voice[n:])
	m.voice = m.voice[:len(m.voice)-n]
	n = min(mixBlock, len(m.music))
	copy(m.music, m.music[n:])
	m.music = m.music[:len(m.music)-n]
	m.signal()
}

func (m *Mixer) Close() error {
	m.mu.Lock()
	select {
	case <-m.stop:
	default:
		close(m.stop)
	}
	m.fail(io.ErrClosedPipe)
	m.mu.Unlock()
	<-m.done
	return nil
}

// Done also closes on an output failure, so an idle host never overlooks a lost
// speaker route. Err is meaningful after Done; Close is normal shutdown.
func (m *Mixer) Done() <-chan struct{} { return m.done }
func (m *Mixer) Err() error {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.err
}

// Causal 63-tap Blackman-windowed sinc interpolator. Filtering the zero-inserted
// 3x stream removes the imaging that sample repetition would send to the DAC.
// Its 31-frame group delay is 0.65 ms; Drain feeds the finite tail, Flush resets it.
type interpolator struct {
	taps    [63]float64
	history [63]float64
	pos     int
}

func (r *interpolator) init() {
	var sum float64
	for i := range r.taps {
		x := float64(i - 31)
		v := 0.3
		if x != 0 {
			v = math.Sin(math.Pi*0.3*x) / (math.Pi * x)
		}
		v *= 0.42 - 0.5*math.Cos(2*math.Pi*float64(i)/62) + 0.08*math.Cos(4*math.Pi*float64(i)/62)
		r.taps[i] = v
		sum += v
	}
	for i := range r.taps {
		r.taps[i] *= 3 / sum
	}
}
func (r *interpolator) reset() { clear(r.history[:]); r.pos = 0 }
func (r *interpolator) append(out []float64, in []int16) []float64 {
	for _, sample := range in {
		for phase := 0; phase < 3; phase++ {
			r.history[r.pos] = 0
			if phase == 0 {
				r.history[r.pos] = float64(sample)
			}
			var v float64
			for j, tap := range r.taps {
				v += r.history[(r.pos-j+63)%63] * tap
			}
			r.pos = (r.pos + 1) % 63
			out = append(out, v)
		}
	}
	return out
}
