// Package outbox bounds capture independently of a potentially stalled peer.
package outbox

import (
	"context"
	"errors"
	"slices"
	"sync"

	"github.com/farooqu/encore-satellite/core/session"
)

type Queue struct {
	mu       sync.Mutex
	commands chan session.Command
	ready    chan struct{}
	ctx      context.Context
	cancel   context.CancelFunc
	close    func() error
	closed   bool
}

func New(close func() error) *Queue {
	ctx, cancel := context.WithCancel(context.Background())
	return &Queue{commands: make(chan session.Command, 64), ready: make(chan struct{}, 1), ctx: ctx, cancel: cancel, close: close}
}

func (q *Queue) Send(c session.Command) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return errors.New("backend: connection closed")
	}
	if c.Kind == "end" {
		// Stop must not wait behind unsent microphone data.
	clearQueued:
		for {
			select {
			case <-q.commands:
			default:
				break clearQueued
			}
		}
	}
	if c.Kind == "cancel" || c.Kind == "discard_audio" || c.Kind == "monitor_off" {
		// Drop unsent microphone data, but preserve a queued start: otherwise
		// the engine would wait for cancellation of a pipeline never started.
		pending := make([]session.Command, 0, len(q.commands))
		drop := "audio"
		if c.Kind == "monitor_off" {
			drop = "monitor_audio"
		}
	dropAudio:
		for {
			select {
			case queued := <-q.commands:
				if queued.Kind != drop {
					pending = append(pending, queued)
				} else {
					clear(queued.Samples)
				}
			default:
				break dropAudio
			}
		}
		for _, queued := range pending {
			q.commands <- queued
		}
		if c.Kind != "cancel" {
			return nil // Local capture invalidation, not a wire-level abort.
		}
	}
	c.Samples = slices.Clone(c.Samples)
	select {
	case q.commands <- c:
		select {
		case q.ready <- struct{}{}:
		default:
		}
		return nil
	default:
		return errors.New("backend: bounded capture queue full")
	}
}

func (q *Queue) Close() error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if q.closed {
		return nil
	}
	q.closed = true
	q.cancel()
	for {
		select {
		case queued := <-q.commands:
			clear(queued.Samples)
		default:
			return q.close()
		}
	}
}

func (q *Queue) Run(send func(session.Command) error) error {
	for {
		// Receive under the same lock as cancellation's filtering, so the
		// writer cannot overtake a temporarily removed start/control command.
		q.mu.Lock()
		select {
		case c := <-q.commands:
			q.mu.Unlock()
			if err := q.ctx.Err(); err != nil {
				clear(c.Samples)
				return err
			}
			if err := send(c); err != nil {
				return err
			}
		default:
			q.mu.Unlock()
			select {
			case <-q.ctx.Done():
				return q.ctx.Err()
			case <-q.ready:
			}
		}
	}
}
