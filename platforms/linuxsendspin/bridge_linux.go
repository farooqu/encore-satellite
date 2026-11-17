//go:build linux

// Package linuxsendspin bridges the official Sendspin SDK companion to a host's
// music-only output. It never owns the voice output or retries a failed child.
package linuxsendspin

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"os/exec"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sys/unix"
)

// Output accepts only interleaved signed little-endian PCM16/48000/stereo.
// Start, End and Volume must be bounded, nonblocking operations. Start and End
// fence queued writes and feedback from older epochs. Write must honor ctx and
// return nil only after bounded queue acceptance (not playback). played reports
// actual consumed music frames, with the completion timestamp in microseconds
// from CLOCK_MONOTONIC_RAW, including hardware latency; never Unix time.
// played may run on an audio thread and must not be called for discarded frames.
// failed reports a lost output clock; it must be nonblocking. Run tears down
// that stream so a subsequent connection negotiates a fresh synchronized epoch.
type Output interface {
	MusicStart(epoch uint64, played func(frames uint32, timestampUS int64), failed ...func())
	MusicWrite(ctx context.Context, epoch uint64, pcm []byte) error
	MusicEnd(epoch uint64)
	MusicVolume(volume uint8, muted bool)
}

type Command uint8

const (
	Play Command = iota + 1
	Stop
)

type Options struct {
	Name, URL, StateDir, Executable string
	Output                          Output
	Commands                        <-chan Command
}

// Epochs passed to Output never repeat when the host retries Run.
var outputEpoch atomic.Uint64

const (
	headerSize  = 32
	maxPCM      = 16384
	msgStart    = 1
	msgEnd      = 2
	msgVolume   = 3
	msgPCM      = 4
	msgAccepted = 5
	msgPlayed   = 6
	msgCommand  = 7
)

type packet struct {
	kind      uint16
	epoch, id uint64
	data      []byte
	value     uint32
}

func encode(p packet) []byte {
	b := make([]byte, headerSize+len(p.data))
	copy(b, "ESPN")
	binary.LittleEndian.PutUint16(b[4:], 1)
	binary.LittleEndian.PutUint16(b[6:], p.kind)
	binary.LittleEndian.PutUint64(b[8:], p.epoch)
	binary.LittleEndian.PutUint64(b[16:], p.id)
	binary.LittleEndian.PutUint32(b[24:], uint32(len(p.data)))
	binary.LittleEndian.PutUint32(b[28:], p.value)
	copy(b[32:], p.data)
	return b
}

func decode(b []byte) (packet, error) {
	if len(b) < headerSize || string(b[:4]) != "ESPN" || binary.LittleEndian.Uint16(b[4:]) != 1 || int(binary.LittleEndian.Uint32(b[24:])) != len(b)-headerSize {
		return packet{}, errors.New("sendspin: malformed IPC frame")
	}
	return packet{binary.LittleEndian.Uint16(b[6:]), binary.LittleEndian.Uint64(b[8:]), binary.LittleEndian.Uint64(b[16:]), b[32:], binary.LittleEndian.Uint32(b[28:])}, nil
}

// ValidateURL disallows credentials, tokens, query parameters and alternate
// transports. Authentication is the SDK's mandatory Noise pairing, not a URL PSK.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "ws" || u.Hostname() == "" || u.Port() == "" || u.User != nil || u.Path != "/sendspin" || u.RawPath != "" || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" {
		return errors.New("sendspin: URL must be ws://HOST:PORT/sendspin without credentials or parameters")
	}
	n, err := strconv.Atoi(u.Port())
	if err != nil || n < 1 || n > 65535 {
		return errors.New("sendspin: invalid port")
	}
	return nil
}

// Run starts one child with one inherited SOCK_SEQPACKET endpoint (fd 3).
// Cancellation closes IPC, fences music, then kills and reaps the child; voice
// remains the host's responsibility. No retries or network listeners are added.
func Run(ctx context.Context, o Options) error {
	if err := ValidateURL(o.URL); err != nil {
		return err
	}
	if o.Output == nil || o.StateDir == "" || o.Executable == "" || o.Name == "" {
		return errors.New("sendspin: incomplete options")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_SEQPACKET|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return err
	}
	parent := os.NewFile(uintptr(fds[0]), "sendspin-parent")
	child := os.NewFile(uintptr(fds[1]), "sendspin-child")
	nc, err := net.FileConn(parent)
	parent.Close()
	if err != nil {
		child.Close()
		return err
	}
	conn := nc.(*net.UnixConn)
	cmd := exec.Command(o.Executable, "run", "--state", o.StateDir, "--name", o.Name, "--url", o.URL)
	cmd.ExtraFiles = []*os.File{child}
	cmd.Env = []string{"LANG=C", "LC_ALL=C"}
	// Do not forward the SDK's diagnostics: future upstream logs may contain
	// pairing material. Errors below deliberately omit configuration and secrets.
	if err = cmd.Start(); err != nil {
		conn.Close()
		child.Close()
		return fmt.Errorf("sendspin: start companion: %w", err)
	}
	child.Close()
	parentCtx := ctx
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	fail := make(chan error, 1)
	die := func(err error) {
		select {
		case fail <- err:
		default:
		}
		cancel()
	}
	out := make(chan packet, 32)
	queue := func(p packet) {
		select {
		case out <- p:
		case <-ctx.Done():
		default:
			die(errors.New("sendspin: IPC send queue saturated"))
		}
	}
	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case p := <-out:
				conn.SetWriteDeadline(time.Now().Add(250 * time.Millisecond))
				if _, e := conn.Write(encode(p)); e != nil {
					die(errors.New("sendspin: IPC write failed"))
					return
				}
			}
		}
	}()
	// Feedback is cumulative per epoch, coalesced without ever touching a socket
	// on the output's callback thread. Old closures cannot update a new epoch.
	var mu sync.Mutex
	var epoch, played uint64
	var stamp int64
	wake := make(chan struct{}, 1)
	feedback := func(e uint64) func(uint32, int64) {
		return func(n uint32, t int64) {
			mu.Lock()
			if epoch != e || epoch == 0 {
				mu.Unlock()
				return
			}
			if t <= 0 || n == 0 || t < stamp || played+uint64(n) > uint64(^uint32(0)) {
				mu.Unlock()
				die(errors.New("sendspin: invalid consumption feedback"))
				return
			}
			played += uint64(n)
			stamp = t
			mu.Unlock()
			select {
			case wake <- struct{}{}:
			default:
			}
		}
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case <-wake:
				mu.Lock()
				p := packet{kind: msgPlayed, epoch: epoch, id: uint64(stamp), value: uint32(played)}
				played = 0
				mu.Unlock()
				if p.epoch != 0 && p.value != 0 {
					queue(p)
				}
			}
		}
	}()
	type write struct {
		p      packet
		epoch  uint64
		ctx    context.Context
		cancel context.CancelFunc
	}
	writes := make(chan write, 1)
	wg.Add(1)
	go func() {
		defer wg.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case w := <-writes:
				if w.ctx.Err() != nil {
					queue(packet{kind: msgAccepted, epoch: w.p.epoch, id: w.p.id})
					w.cancel()
					continue
				}
				err := o.Output.MusicWrite(w.ctx, w.epoch, w.p.data)
				if err != nil {
					if w.ctx.Err() == nil {
						die(errors.New("sendspin: output refused PCM"))
					} else {
						// Explicit zero acceptance lets the SDK retry ordinary
						// bounded output backpressure without losing PCM.
						queue(packet{kind: msgAccepted, epoch: w.p.epoch, id: w.p.id})
					}
					w.cancel()
					continue
				}
				queue(packet{kind: msgAccepted, epoch: w.p.epoch, id: w.p.id, value: uint32(len(w.p.data))})
				w.cancel()
			}
		}
	}()
	var active, localEpoch uint64
	var streamCtx context.Context
	var streamCancel context.CancelFunc
	ended := make(chan struct{})
	go func() {
		defer close(ended)
		defer func() {
			if streamCancel != nil {
				streamCancel()
			}
			mu.Lock()
			epoch = 0
			mu.Unlock()
			if active != 0 {
				o.Output.MusicEnd(localEpoch)
			}
		}()
		var last, lastRequest uint64
		for {
			b := make([]byte, headerSize+maxPCM)
			n, _, flags, _, e := conn.ReadMsgUnix(b, nil)
			if e != nil {
				die(errors.New("sendspin: IPC closed"))
				return
			}
			if flags&unix.MSG_TRUNC != 0 {
				die(errors.New("sendspin: oversized IPC frame"))
				return
			}
			p, e := decode(b[:n])
			if e != nil {
				die(e)
				return
			}
			valid := false
			switch p.kind {
			case msgStart:
				valid = len(p.data) == 0 && p.epoch > last && p.id == 0 && p.value == 0
				if valid {
					if streamCancel != nil {
						streamCancel()
					}
					if active != 0 {
						o.Output.MusicEnd(localEpoch)
					}
					active = p.epoch
					localEpoch = outputEpoch.Add(1)
					streamCtx, streamCancel = context.WithCancel(ctx)
					last = active
					mu.Lock()
					epoch = active
					played = 0
					stamp = 0
					mu.Unlock()
					startedEpoch := active
					o.Output.MusicStart(localEpoch, feedback(active), func() {
						mu.Lock()
						current := epoch == startedEpoch
						mu.Unlock()
						if current {
							die(errors.New("sendspin: output clock discontinuity"))
						}
					})
				}
			case msgEnd:
				valid = len(p.data) == 0 && p.epoch == active && active != 0 && p.id == 0 && p.value == 0
				if valid {
					if streamCancel != nil {
						streamCancel()
					}
					mu.Lock()
					epoch = 0
					played = 0
					mu.Unlock()
					o.Output.MusicEnd(localEpoch)
					active = 0
				}
			case msgVolume:
				valid = len(p.data) == 0 && p.epoch == 0 && p.id <= 1 && p.value <= 100
				if valid {
					o.Output.MusicVolume(uint8(p.value), p.id == 1)
				}
			case msgPCM:
				valid = active != 0 && p.epoch == active && p.id > lastRequest && len(p.data) > 0 && len(p.data)%4 == 0 && p.value > 0 && p.value <= 10000
				if valid {
					lastRequest = p.id
					wc, writeCancel := context.WithTimeout(streamCtx, time.Duration(p.value)*time.Millisecond)
					select {
					case writes <- write{p, localEpoch, wc, writeCancel}:
					default:
						writeCancel()
						valid = false
					}
				}
			}
			if !valid {
				die(errors.New("sendspin: invalid IPC message/state"))
				return
			}
		}
	}()
	wg.Add(1)
	go func() {
		defer wg.Done()
		commands := o.Commands
		for {
			select {
			case <-ctx.Done():
				return
			case c, ok := <-commands:
				if !ok {
					commands = nil
					continue
				}
				if c != Play && c != Stop {
					die(errors.New("sendspin: invalid command"))
					return
				}
				queue(packet{kind: msgCommand, value: uint32(c)})
			}
		}
	}()
	childDone := make(chan error, 1)
	go func() { childDone <- cmd.Wait() }()
	var result error
	var childErr error
	exited := false
	select {
	case <-ctx.Done():
		result = ctx.Err()
	case childErr = <-childDone:
		result = errors.New("sendspin: companion exited")
		exited = true
	case result = <-fail:
	}
	cancel()
	conn.Close()
	if !exited && parentCtx.Err() == nil {
		// IPC EOF often arrives just before wait reports a deliberate native
		// fail-closed exit. Preserve its fixed diagnostic code, not SDK logs.
		select {
		case childErr = <-childDone:
			exited = true
		case <-time.After(50 * time.Millisecond):
		}
	}
	if !exited {
		_ = cmd.Process.Kill()
		select {
		case childErr = <-childDone:
		case <-time.After(2 * time.Second):
			result = errors.New("sendspin: child reap timed out")
		}
	}
	<-ended
	wg.Wait()
	select {
	case e := <-fail:
		if parentCtx.Err() == nil {
			result = e
		}
	default:
	}
	if parentCtx.Err() != nil {
		return parentCtx.Err()
	}
	if childErr != nil {
		return fmt.Errorf("%w (companion: %v)", result, childErr)
	}
	return result
}
