package alsa

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"syscall"
	"unsafe"

	"golang.org/x/sys/unix"
)

// FormatS16_LE is 16-bit little endian, the only format the playback codec accepts.
const FormatS16_LE = 2

// FormatS24_LE is 24-bit samples in the low three bytes of a 32-bit container: Bits is the container's
// 32, and the sample width is left for the kernel to take from the format.
const FormatS24_LE = 6

// Native STATUS uses the legacy time32 layout on 32-bit kernels, not libc's
// optional time64 ABI. Only the first field (the stream state) is read.
const statusSize = 64 + 11*longSize

var (
	ioctlWritei = ioc(1, 'A', 0x50, xferiSize)
	ioctlDrain  = ioc(0, 'A', 0x44, 0)
	ioctlStatus = ioc(2, 'A', 0x20, statusSize)

	// SNDRV_PCM_IOCTL_DELAY: _IOR('A', 0x21, snd_pcm_sframes_t), a signed long, so word sized.
	ioctlDelay = ioc(2, 'A', 0x21, longSize)

	ioctlSwParams = ioc(3, 'A', 0x13, unsafe.Sizeof(swParams{}))
)

// swParams is struct snd_pcm_sw_params. The frame counts are snd_pcm_uframes_t, the kernel's
// unsigned long, which is Go's uint on both ABIs: 104 bytes on a 32-bit build, 136 on a 64-bit one,
// where the counts start four bytes later.
type swParams struct {
	tstampMode       int32
	periodStep       uint32
	sleepMin         uint32
	availMin         uint
	xferAlign        uint
	startThreshold   uint
	stopThreshold    uint
	silenceThreshold uint
	silenceSize      uint
	boundary         uint
	proto            uint32
	tstampType       uint32
	reserved         [56]byte
}

// fullStart is the kernel's own defaults with one change: the stream starts once the ring is full,
// not on the first frame. proto is left at zero so the kernel ignores tstampType.
func fullStart(cfg Config) swParams {
	ring := uint(cfg.PeriodSize * cfg.Periods)
	return swParams{
		periodStep:     1,
		availMin:       uint(cfg.PeriodSize),
		startThreshold: ring,
		stopThreshold:  ring,
	}
}

// Playback is an open PCM playback stream.
type Playback struct {
	f          *os.File
	cfg        Config
	frameBytes int
	started    bool
	// full is whether the kernel took StartFull: the stream starts once the ring is written.
	full   bool
	wakeFD int // nonblocking control/event wake; -1 without hardware pacing
}

// OpenPlayback configures a playback stream. The hardware starts itself on the first write, or once
// the ring is full with cfg.StartFull. A kernel that refuses StartFull gets a stream that starts on
// the first write, as before, rather than no stream: see StartsFull.
//
// The open is non-blocking: mediaserver holds this device, and a blocking open waits forever.
// A held device returns ErrBusy. Writes and Drain remain nonblocking. The
// caller serializes all operations and retries EAGAIN with cancellation.
func OpenPlayback(card, device int, cfg Config) (*Playback, error) {
	ring := cfg.PeriodSize * cfg.Periods
	if cfg.PlaybackWakeLead < 0 || (cfg.PlaybackWakeLead != 0 && (cfg.StartFull || cfg.PlaybackWakeLead >= ring)) {
		return nil, errors.New("alsa: invalid paced playback watermark")
	}
	path := fmt.Sprintf("/dev/snd/pcmC%dD%dp", card, device)
	f, err := os.OpenFile(path, os.O_WRONLY|syscall.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, syscall.EBUSY) || errors.Is(err, syscall.EAGAIN) {
			return nil, fmt.Errorf("%w: %s", ErrBusy, path)
		}
		return nil, err
	}

	var p hwParams
	p.init()
	p.setMask(paramAccess, accessRWInterleaved)
	p.setMask(paramFormat, cfg.Format)
	p.setMask(paramSubformat, subformatStd)
	if cfg.Format != FormatS24_LE { // its sample width (24) is not its container's (Bits)
		p.setInterval(paramSampleBits, uint32(cfg.Bits))
	}
	p.setInterval(paramFrameBits, uint32(cfg.Bits*cfg.Channels))
	p.setInterval(paramChannels, uint32(cfg.Channels))
	p.setInterval(paramRate, uint32(cfg.Rate))
	p.setInterval(paramPeriodSize, uint32(cfg.PeriodSize))
	p.setInterval(paramPeriods, uint32(cfg.Periods))

	if err := ioctl(f.Fd(), ioctlHwParams, unsafe.Pointer(&p)); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("hw_params: %w", err)
	}
	if err := granted(&p, cfg); err != nil {
		_ = f.Close()
		return nil, err
	}
	full := false
	if cfg.StartFull {
		sw := fullStart(cfg)
		full = ioctl(f.Fd(), ioctlSwParams, unsafe.Pointer(&sw)) == nil
	}
	if cfg.PlaybackWakeLead != 0 {
		// ALSA readiness is measured in free frames, not playback lead.
		sw := fullStart(cfg)
		sw.startThreshold, sw.availMin = 1, uint(ring-cfg.PlaybackWakeLead)
		if err := ioctl(f.Fd(), ioctlSwParams, unsafe.Pointer(&sw)); err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("alsa: paced playback sw_params: %w", err)
		}
	}
	if err := ioctlArgless(f.Fd(), ioctlPrepare); err != nil {
		_ = f.Close()
		return nil, fmt.Errorf("prepare: %w", err)
	}
	wakeFD := -1
	if cfg.PlaybackWakeLead != 0 {
		wakeFD, err = unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
		if err != nil {
			_ = f.Close()
			return nil, fmt.Errorf("alsa: playback eventfd: %w", err)
		}
	}

	return &Playback{
		f:          f,
		cfg:        cfg,
		frameBytes: cfg.Channels * cfg.Bits / 8,
		full:       full,
		wakeFD:     wakeFD,
	}, nil
}

func (p *Playback) PlaybackPaced() bool { return p.cfg.PlaybackWakeLead != 0 }

// WakePlayback coalesces work without waiting for the PCM owner. The caller
// serializes wakes with shutdown; only the owner closes either descriptor.
func (p *Playback) WakePlayback() {
	var b [8]byte
	binary.NativeEndian.PutUint64(b[:], 1)
	for {
		if _, err := unix.Write(p.wakeFD, b[:]); err != unix.EINTR {
			return // EAGAIN means a wake is already pending.
		}
	}
}

// WaitPlayback blocks on ALSA or a software wake, not a timer. ALSA POLLERR
// also wakes the owner; DELAY determines whether recovery is needed.
func (p *Playback) WaitPlayback() (bool, error) {
	fds := [2]unix.PollFd{{Fd: int32(p.wakeFD), Events: unix.POLLIN}, {Fd: int32(p.f.Fd()), Events: unix.POLLOUT}}
	for {
		_, err := unix.Poll(fds[:], -1)
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return false, err
		}
		if fds[0].Revents&unix.POLLIN != 0 {
			var b [8]byte
			for {
				if _, err := unix.Read(p.wakeFD, b[:]); err != unix.EINTR {
					return false, err
				}
			}
		}
		if fds[0].Revents != 0 || fds[1].Revents&(unix.POLLNVAL|unix.POLLHUP) != 0 {
			return false, io.ErrClosedPipe
		}
		return true, nil
	}
}

// StartsFull is whether the stream waits for a full ring before it starts, which is what
// Config.StartFull asked for unless the kernel refused it.
func (p *Playback) StartsFull() bool { return p.full }

// granted refuses a stream the hardware would run differently from how it was asked to.
//
// Failing here is the point: a caller writes in whole periods of the size it configured, so a period or
// rate the driver quietly changed means every write is misaligned with what the hardware consumes, and
// the result is audible as noise and wrong-sounding audio rather than as an error.
func granted(p *hwParams, cfg Config) error {
	for _, got := range []struct {
		what  string
		param int
		want  uint32
	}{
		{"period size", paramPeriodSize, uint32(cfg.PeriodSize)},
		{"periods", paramPeriods, uint32(cfg.Periods)},
		{"rate", paramRate, uint32(cfg.Rate)},
		{"channels", paramChannels, uint32(cfg.Channels)},
	} {
		if have := p.interval(got.param); have != got.want {
			return fmt.Errorf("alsa: the driver gives %s %d, not %d", got.what, have, got.want)
		}
	}
	return nil
}

// FrameBytes is the size of one interleaved frame across all channels.
func (p *Playback) FrameBytes() int { return p.frameBytes }

// Write plays whole frames, returning EAGAIN if the hardware has no room. On underrun the stream is
// re-prepared unless the paced owner handles recovery; the call reports the gap either way. With
// StartFull the kernel starts the stream itself when the write that fills the ring lands.
func (p *Playback) Write(buf []byte) (int, error) {
	frames := len(buf) / p.frameBytes
	if frames == 0 {
		return 0, fmt.Errorf("buffer smaller than one frame (%d bytes)", p.frameBytes)
	}
	x := xferi{
		buf:    unsafe.Pointer(&buf[0]),
		frames: uintptr(frames),
	}
	if err := ioctl(p.f.Fd(), ioctlWritei, unsafe.Pointer(&x)); err != nil {
		if err == syscall.EPIPE {
			if p.PlaybackPaced() {
				return 0, ErrUnderrun // the owner fences the stream before preparing it
			}
			_ = ioctlArgless(p.f.Fd(), ioctlPrepare)
			p.started = false
			return 0, ErrUnderrun
		}
		return 0, err
	}
	if x.result < 0 {
		return 0, syscall.Errno(-x.result)
	}

	if !p.started && !p.full {
		p.started = true
		// EBADFD if the stream is already running, which is not an error here.
		if err := ioctlArgless(p.f.Fd(), ioctlStart); err != nil && err != syscall.Errno(0x4d) {
			return int(x.result) * p.frameBytes, fmt.Errorf("start: %w", err)
		}
	}
	return int(x.result) * p.frameBytes, nil
}

// Delay is how many frames the card has yet to play: what has been written and not yet reached the
// codec. The kernel syncs the hardware pointer first, so on a driver whose pointer callback reads the
// DMA position (MediaTek's AFE does) it is good to a few frames, not just to a period. It is what a
// frame written now waits before it is heard, less whatever the codec and amplifier add after the
// ring.
func (p *Playback) Delay() (int, error) {
	var d int // snd_pcm_sframes_t is the kernel's long, which is Go's int on both ABIs (see xferi)
	if err := ioctl(p.f.Fd(), ioctlDelay, unsafe.Pointer(&d)); err != nil {
		return 0, err
	}
	return d, nil
}

// Drain initiates hardware drain. EAGAIN means physical playback is not done.
// Success leaves the stream in SETUP; prepare it for the next turn.
func (p *Playback) Drain() error {
	if err := ioctlArgless(p.f.Fd(), ioctlDrain); err != nil {
		if err != syscall.EAGAIN {
			return err
		}
		// Nonblocking DRAIN can return EAGAIN even after it reached SETUP.
		// Check the kernel state rather than waiting for an ioctl success
		// that such kernels never return, or mistaking zero delay for done.
		var status [statusSize]byte
		if err := ioctl(p.f.Fd(), ioctlStatus, unsafe.Pointer(&status)); err != nil {
			return err
		}
		if binary.LittleEndian.Uint32(status[:4]) != 1 { // SNDRV_PCM_STATE_SETUP
			return syscall.EAGAIN
		}
	}
	p.started = false
	return ioctlArgless(p.f.Fd(), ioctlPrepare)
}

// Drop promptly discards the kernel ring and prepares an empty stream.
func (p *Playback) Drop() error {
	if err := ioctlArgless(p.f.Fd(), ioctlDrop); err != nil {
		return err
	}
	p.started = false
	return ioctlArgless(p.f.Fd(), ioctlPrepare)
}

func (p *Playback) Close() error {
	if p.PlaybackPaced() {
		_ = unix.Close(p.wakeFD)
	}
	_ = ioctlArgless(p.f.Fd(), ioctlDrop)
	return p.f.Close()
}

// ErrUnderrun means the hardware ran out of samples; playback gapped.
var ErrUnderrun = fmt.Errorf("alsa: playback underrun")

// ErrBusy means another process holds the device.
var ErrBusy = errors.New("alsa: device busy")
