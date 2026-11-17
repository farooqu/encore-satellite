// Package echo provides Linux host primitives, not model wiring or voice policy.
package echo

import (
	"context"
	"errors"
	"io"
	"sync"
	"syscall"
	"time"
)

// PCM must be nonblocking. Drain returns EAGAIN until the hardware ring has
// finished, not merely until a userspace queue is empty. All calls are serialized.
type PCM interface {
	Write([]byte) (int, error)
	Drain() error
	Drop() error
	Close() error
}

// Player has no userspace audio queue. Bounded writes backpressure directly on
// ALSA. Flush invalidates even callers waiting to write, then drops the DMA ring.
type Player struct {
	mu         sync.Mutex
	pcm        PCM
	serial     chan struct{}
	generation uint64
	err        error
	encode     func([]int16) []byte
	frameBytes int
}

// encode supplies model-local sample-rate/channel conversion. frameBytes is
// the physical PCM frame width, used to reject malformed short kernel writes.
func NewPlayer(pcm PCM, encode func([]int16) []byte, frameBytes int) *Player {
	return &Player{pcm: pcm, serial: make(chan struct{}, 1), encode: encode, frameBytes: frameBytes}
}

func (p *Player) Write(ctx context.Context, samples []int16) error {
	if len(samples) == 0 || len(samples) > 2048 {
		return errors.New("echo: invalid playback block")
	}
	var b []byte
	return p.run(ctx, func() (bool, error) {
		if b == nil {
			b = p.encode(samples)
		}
		n, err := p.pcm.Write(b)
		if n < 0 || n > len(b) || n%p.frameBytes != 0 {
			return false, io.ErrShortWrite
		}
		b = b[n:]
		return len(b) == 0, err
	})
}

func (p *Player) Drain(ctx context.Context) error {
	return p.run(ctx, func() (bool, error) { err := p.pcm.Drain(); return err == nil, err })
}

func (p *Player) run(ctx context.Context, step func() (bool, error)) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	generation := p.generation
	p.mu.Unlock()
	select {
	case p.serial <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-p.serial }()
	for {
		p.mu.Lock()
		if generation != p.generation {
			p.mu.Unlock()
			return context.Canceled
		}
		if p.err != nil {
			err := p.err
			p.mu.Unlock()
			return err
		}
		if err := ctx.Err(); err != nil {
			p.drop()
			p.mu.Unlock()
			return err
		}
		done, err := step()
		if cancelled := ctx.Err(); cancelled != nil {
			p.drop()
			p.mu.Unlock()
			return cancelled
		}
		if err != nil && !errors.Is(err, syscall.EAGAIN) && !errors.Is(err, syscall.EINTR) {
			p.drop()
			p.mu.Unlock()
			return err
		}
		p.mu.Unlock()
		if done && err == nil {
			return nil
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Millisecond):
		}
	}
}

// drop is called under mu. Failed DROP closes the descriptor, never reuses a
// potentially audible ring and reports the failure to subsequent operations.
func (p *Player) drop() {
	p.generation++
	if p.err == nil {
		if err := p.pcm.Drop(); err != nil {
			p.err = err
			_ = p.pcm.Close()
		}
	}
}

func (p *Player) Flush() { p.mu.Lock(); defer p.mu.Unlock(); p.drop() }
func (p *Player) Close() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.generation++
	if p.err != nil {
		return p.err
	}
	p.err = io.ErrClosedPipe
	return p.pcm.Close()
}
