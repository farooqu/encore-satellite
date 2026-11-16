// Package lab is test instrumentation, never a device/platform implementation.
package lab

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"hash"
	"sync"
	"time"
)

// VirtualSpeaker consumes samples on a clock, without opening physical audio.
// Hold freezes consumption, allowing tests to distinguish receipt from drain.
type VirtualSpeaker struct {
	mu                                        sync.Mutex
	hold                                      bool
	queued, received, played, flushed, drains int
	digest                                    hash.Hash
	samples                                   []int16
	music                                     *Music
}

type PlaybackState struct {
	Queued   int    `json:"queued"`
	Received int    `json:"received"`
	Played   int    `json:"played"`
	Flushed  int    `json:"flushed"`
	Drains   int    `json:"drains"`
	SHA256   string `json:"sha256"`
	Hold     bool   `json:"hold"`
}

func NewSpeaker(ctx context.Context) *VirtualSpeaker {
	p := &VirtualSpeaker{digest: sha256.New()}
	go func() {
		ticker := time.NewTicker(10 * time.Millisecond)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				p.mu.Lock()
				var voice []int16
				if !p.hold {
					n := min(p.queued, 160)
					p.queued -= n
					p.played += n
					voice = p.samples[:n]
					p.samples = p.samples[n:]
				}
				if p.music != nil {
					p.music.render(voice)
				}
				p.mu.Unlock()
			}
		}
	}()
	return p
}

func (p *VirtualSpeaker) Write(ctx context.Context, samples []int16) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		p.mu.Lock()
		if err := ctx.Err(); err != nil {
			p.mu.Unlock()
			return err
		}
		if p.queued+len(samples) <= 8192 {
			p.queued += len(samples)
			p.received += len(samples)
			p.samples = append(p.samples, samples...)
			for _, sample := range samples {
				var b [2]byte
				binary.LittleEndian.PutUint16(b[:], uint16(sample))
				_, _ = p.digest.Write(b[:])
			}
			p.mu.Unlock()
			return nil
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (p *VirtualSpeaker) Drain(ctx context.Context) error {
	ticker := time.NewTicker(5 * time.Millisecond)
	defer ticker.Stop()
	for {
		p.mu.Lock()
		if err := ctx.Err(); err != nil {
			p.mu.Unlock()
			return err
		}
		if p.queued == 0 {
			p.drains++
			p.mu.Unlock()
			return nil
		}
		p.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (p *VirtualSpeaker) Flush() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.flushed += p.queued
	p.queued = 0
	p.samples = nil
}

// AttachMusic adds a separate 48 kHz stereo music route to this virtual clock.
// Voice Flush/Drain still affect only the voice queue.
func (p *VirtualSpeaker) AttachMusic(music *Music) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.music = music
}

func (p *VirtualSpeaker) Hold(hold bool) { p.mu.Lock(); defer p.mu.Unlock(); p.hold = hold }
func (p *VirtualSpeaker) Snapshot() PlaybackState {
	p.mu.Lock()
	defer p.mu.Unlock()
	return PlaybackState{p.queued, p.received, p.played, p.flushed, p.drains, hex.EncodeToString(p.digest.Sum(nil)), p.hold}
}
