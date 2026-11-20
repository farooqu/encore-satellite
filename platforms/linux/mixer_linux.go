package linux

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

	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/platforms/linux/alsa"
)

// TimedPCM is a single nonblocking 48 kHz stereo PCM16 output. Delay is the
// hardware's outstanding frame count, not a userspace estimate. Calls serialize.
type TimedPCM interface {
	PCM
	Delay() (int, error)
}

type pacedPCM interface {
	PlaybackPaced() bool
	WaitPlayback() (hardware bool, err error)
	WakePlayback()
}

const (
	purgeVoice = 1 << iota
	purgeMusic
)

const (
	voiceChunk    = 320  // 20 ms at 16 kHz, independent of the DMA lead.
	voicePreroll  = 1920 // 40 ms of converted voice before starting or resuming.
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
	mu                  sync.Mutex
	epochMu             sync.Mutex // serialize music replacement across its purge acknowledgement
	pcm                 TimedPCM
	pacer               pacedPCM
	purge               uint8
	purging             bool
	mixAhead            int
	wake                chan struct{}
	stop                chan struct{}
	done                chan struct{}
	serial              chan struct{}
	err                 error
	voice               []float64
	music               []stereo
	hardware, pending   []mixFrame
	voicePending        int
	voiceStarted        bool
	generation          uint64
	voiceCancel         context.CancelCauseFunc
	resample            interpolator
	tail                bool
	epoch               uint64
	played              func(uint32, int64)
	musicFailed         func()
	volume              float64
	musicGain           float64
	musicMuted, stopped bool
	privacy             atomic.Bool // transitions and purge serialized by mu
	active              atomic.Bool
	duck                float64
}

// NewMixer starts silent; device assembly selects physical gain and the
// positive refill lead in PCM frames, no larger than the hardware ring.
func NewMixer(pcm TimedPCM, mixAhead int) *Mixer {
	m := &Mixer{pcm: pcm, mixAhead: mixAhead, wake: make(chan struct{}), stop: make(chan struct{}), done: make(chan struct{}), serial: make(chan struct{}, 1), musicGain: 1, duck: 1}
	if p, ok := pcm.(pacedPCM); ok && p.PlaybackPaced() {
		m.pacer = p
	}
	m.resample.init()
	go m.run()
	return m
}

// SetVoiceActive is safe inside the engine lock: no I/O, waiting or reentry.
func (m *Mixer) SetVoiceActive(active bool) { m.active.Store(active) }

func (m *Mixer) signal() { close(m.wake); m.wake = make(chan struct{}) }

// notifyOwner is called under mu, which also protects shutdown from a wake
// hitting a closed/reused descriptor. It never waits for the owner.
func (m *Mixer) notifyOwner() {
	if m.pacer != nil && m.err == nil {
		m.pacer.WakePlayback()
	}
}

func (m *Mixer) requestPurge(parts uint8) {
	if m.err == nil {
		m.purge |= parts
		m.notifyOwner()
	}
	m.signal()
}

func (m *Mixer) waitPurge() {
	for m.err == nil && (m.purge != 0 || m.purging) {
		wake := m.wake
		m.mu.Unlock()
		<-wake
		m.mu.Lock()
	}
}

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
		if m.purge == 0 && !m.purging && ready() {
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

type voiceStreamKey struct{}

func (*Mixer) StreamContext(parent context.Context) context.Context {
	ctx, cancel := context.WithCancelCause(parent)
	return context.WithValue(ctx, voiceStreamKey{}, cancel)
}

func (m *Mixer) invalidateVoice(cause error) {
	m.generation++
	if m.voiceCancel != nil {
		m.voiceCancel(cause)
		m.voiceCancel = nil
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
	// Bind only when playback starts, not during HTTP setup. Keep the fence
	// between operations so a later Write or Drain cannot adopt a new generation.
	if cancel, ok := ctx.Value(voiceStreamKey{}).(context.CancelCauseFunc); ok {
		m.voiceCancel = cancel
	}
	return op(g)
}

func (m *Mixer) Write(ctx context.Context, samples []int16) error {
	if len(samples) == 0 || len(samples) > 2048 {
		return errors.New("linux: invalid voice block")
	}
	return m.voiceOp(ctx, func(g uint64) error {
		for len(samples) > 0 {
			// Convert at most 20 ms while earlier voice is still
			// queued. A maximum wire block must not wait for the queue to empty
			// and leave fill padding its last partial block with silence.
			n := min(len(samples), voiceChunk)
			// Serial owns the FIR snapshot while the hardware clock keeps
			// running; Flush/recovery fence each bounded chunk's commit.
			r := m.resample
			m.mu.Unlock()
			converted := r.append(make([]float64, 0, n*3), samples[:n])
			m.mu.Lock()
			if len(m.voice)+len(converted) > voiceCapacity {
				m.notifyOwner()
			}
			if err := m.wait(ctx, g, func() bool { return len(m.voice)+len(converted) <= voiceCapacity }); err != nil {
				return err
			}
			m.resample = r
			m.voice = append(m.voice, converted...)
			m.voicePending += len(converted)
			m.tail = false
			samples = samples[n:]
		}
		m.notifyOwner()
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
			m.notifyOwner()
		}
		if err := m.wait(ctx, g, func() bool { return m.voicePending == 0 }); err != nil {
			return err
		}
		m.voiceCancel = nil
		m.voiceStarted = false
		m.resample.reset()
		return nil
	})
}

// Flush fences this generation synchronously. Only the PCM owner can finish
// the purge, after any in-flight ioctl; new writes wait for that acknowledgement.
func (m *Mixer) Flush() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.invalidateVoice(audio.ErrPlaybackInterrupted)
	m.voice = m.voice[:0]
	m.resample.reset()
	m.tail = false
	m.requestPurge(purgeVoice)
}

// SetVolume applies the model's conservative physical gain to both contributors.
func (m *Mixer) SetVolume(gain float64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.volume = max(0, min(1, gain))
	m.notifyOwner()
}

// Privacy release deliberately does not release the music Stop latch. Only an
// explicit operator Play may do that, never voice completion or a fresh stream.
func (m *Mixer) SetMusicPrivacy(muted bool) {
	if !muted && !m.privacy.Load() {
		return // A no-op must not stall capture behind playback recovery.
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if muted == m.privacy.Load() {
		return
	}
	m.privacy.Store(muted)
	if muted {
		m.stopped = true
		m.requestPurge(purgeMusic)
	}
}
func (m *Mixer) StopMusic() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped = true
	m.requestPurge(purgeMusic)
}
func (m *Mixer) PlayMusic() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.privacy.Load() {
		m.stopped = false
		m.notifyOwner()
	}
}

// MusicStart/Write/End implement the companion output contract. The epoch is
// scoped to a child run by its Go bridge; it must not be reused on child restart.
func (m *Mixer) MusicStart(epoch uint64, played func(uint32, int64), failed ...func()) {
	m.epochMu.Lock()
	defer m.epochMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	m.music = m.music[:0]
	m.epoch, m.played, m.musicFailed = 0, nil, nil
	m.requestPurge(purgeMusic)
	m.waitPurge()
	if m.err != nil {
		return
	}
	m.epoch, m.played = epoch, played
	m.musicFailed = nil
	if len(failed) != 0 {
		m.musicFailed = failed[0]
	}
	m.signal()
}
func (m *Mixer) MusicWrite(ctx context.Context, epoch uint64, b []byte) error {
	if len(b) == 0 || len(b)%4 != 0 || len(b)/4 > musicCapacity {
		return errors.New("linux: invalid music block")
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
		if m.purge == 0 && !m.purging && len(m.music)+len(b)/4 <= musicCapacity {
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
	m.notifyOwner()
	return nil
}
func (m *Mixer) MusicEnd(epoch uint64) {
	m.epochMu.Lock()
	defer m.epochMu.Unlock()
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.epoch != epoch {
		return
	}
	m.music = m.music[:0]
	m.epoch, m.played, m.musicFailed = 0, nil, nil
	m.requestPurge(purgeMusic)
	m.waitPurge()
}
func (m *Mixer) MusicVolume(volume uint8, muted bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.musicGain, m.musicMuted = float64(min(volume, 100))/100, muted
	m.notifyOwner()
}

// progress retires only frames the kernel says it consumed. The raw monotonic
// epoch matches SendspinClient::now_us() on Linux. Callbacks must not block.
func (m *Mixer) progress() error {
	m.mu.Unlock()
	delay, err := m.pcm.Delay()
	var ts unix.Timespec
	if err == nil {
		err = unix.ClockGettime(unix.CLOCK_MONOTONIC_RAW, &ts)
	}
	m.mu.Lock()
	if err != nil {
		return err
	}
	if delay < 0 || delay > len(m.hardware) {
		return errors.New("linux: invalid PCM delay")
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
		m.mu.Unlock()
		err := m.pcm.Drop()
		m.mu.Lock()
		if err != nil {
			m.fail(err)
			return
		}
	}
	for _, f := range m.hardware {
		if drop && f.voiced {
			m.invalidateVoice(audio.ErrPlaybackInterrupted)
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
		m.voiceStarted = false
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
	wasPurging := m.purging
	m.purging = true
	m.invalidateVoice(audio.ErrPlaybackUnderrun)
	m.voice, m.hardware, m.pending = m.voice[:0], m.hardware[:0], m.pending[:0]
	m.voicePending, m.tail = 0, false
	m.voiceStarted = false
	m.resample.reset()
	m.invalidateMusic()
	m.signal()
	m.mu.Unlock()
	err = m.pcm.Drop()
	m.mu.Lock()
	m.purging = wasPurging
	if err != nil {
		m.fail(err)
	}
	m.signal()
}

func (m *Mixer) fail(err error) {
	if m.err == nil {
		m.invalidateVoice(audio.ErrPlaybackInterrupted)
		m.notifyOwner()
		m.err = err
		m.signal()
	}
}

func (m *Mixer) run() {
	defer close(m.done)
	defer m.pcm.Close()
	var ticks <-chan time.Time
	if m.pacer == nil { // Test and other non-pollable PCM adapters.
		tick := time.NewTicker(2 * time.Millisecond)
		defer tick.Stop()
		ticks = tick.C
	}
	noProgress := 0
	for {
		var hardware bool
		var waitErr error
		if m.pacer != nil {
			hardware, waitErr = m.pacer.WaitPlayback()
		} else {
			select {
			case <-m.stop:
				return
			case <-ticks:
			}
		}
		m.mu.Lock()
		if waitErr != nil {
			m.fail(waitErr)
		}
		if m.err != nil {
			m.mu.Unlock()
			return
		}
		if parts := m.purge; parts != 0 {
			m.purge, m.purging = 0, true
			m.rebuild(parts&purgeVoice != 0, parts&purgeMusic != 0)
			m.purging = false
			m.signal()
			if m.purge != 0 || m.err != nil {
				m.mu.Unlock()
				continue
			}
		}
		if err := m.progress(); err != nil {
			m.recover(err)
			m.mu.Unlock()
			continue
		}
		if m.purge != 0 || m.err != nil {
			m.mu.Unlock()
			continue
		}
		accepted := 0
		if len(m.hardware) < m.mixAhead {
			n := m.mixAhead - len(m.hardware)
			if missing := n - len(m.pending); missing > 0 {
				m.fill(missing)
			}
			b := make([]byte, n*4)
			duck := m.duck
			silentMusic := m.musicMuted || m.privacy.Load() || m.stopped
			target := 1.0
			if m.active.Load() {
				target = 0.2
			}
			for i, f := range m.pending[:n] {
				// 20 ms full-range gain ramp, independent of user music gain.
				duck += max(-0.8/960, min(0.8/960, target-duck))
				gain := m.musicGain * duck
				if silentMusic {
					gain = 0
				}
				for ch, sample := range []int16{f.music.l, f.music.r} {
					v := max(-32768, min(32767, f.voice+float64(sample)*gain)) * m.volume
					binary.LittleEndian.PutUint16(b[i*4+ch*2:], uint16(int16(max(-32768, min(32767, math.Round(v))))))
				}
			}
			m.mu.Unlock()
			written, err := m.pcm.Write(b)
			m.mu.Lock()
			if written < 0 || written > len(b) || written%4 != 0 {
				err = io.ErrShortWrite
				written = 0
			}
			accepted = written / 4
			step := float64(written/4) * 0.8 / 960
			m.duck += max(-step, min(step, target-m.duck))
			// A purge can arrive while this write is in flight. Account for
			// what the kernel accepted so the next pass purges it as well.
			m.hardware = append(m.hardware, m.pending[:written/4]...)
			copy(m.pending, m.pending[written/4:])
			m.pending = m.pending[:len(m.pending)-written/4]
			if err != nil && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
				m.recover(err)
				noProgress = 0
				m.mu.Unlock()
				continue
			}
		}
		// Broken readiness must not turn into a tight loop. A healthy
		// hardware wake either accepts frames or reports an ALSA fault.
		if accepted > 0 {
			noProgress = 0
		} else if hardware {
			noProgress++
			if noProgress == 3 {
				m.fail(errors.New("linux: playback readiness without progress"))
			}
		}
		m.mu.Unlock()
	}
}

func (m *Mixer) fill(frames int) {
	// Hold a short initial/resume queue without holding the speaker clock.
	// Drain's finite tail can finish even if this clip never met the threshold.
	voiced := 0
	if m.voiceStarted || m.tail || len(m.voice) >= voicePreroll {
		voiced = min(frames, len(m.voice))
		m.voiceStarted = voiced == frames
	}
	music := min(frames, len(m.music))
	for i := 0; i < frames; i++ {
		f := mixFrame{}
		if i < voiced {
			f.voice, f.voiced = m.voice[i], true
		}
		if i < music {
			f.music, f.epoch = m.music[i], m.epoch
		}
		m.pending = append(m.pending, f)
	}
	if voiced > 0 {
		copy(m.voice, m.voice[voiced:])
		m.voice = m.voice[:len(m.voice)-voiced]
	}
	if music > 0 {
		copy(m.music, m.music[music:])
		m.music = m.music[:len(m.music)-music]
	}
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
