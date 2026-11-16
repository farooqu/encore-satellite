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
	ctx      context.Context
	cancel   context.CancelFunc
	close    func() error
	closed   bool
}

func New(close func() error) *Queue {
	ctx, cancel := context.WithCancel(context.Background())
	return &Queue{commands: make(chan session.Command, 64), ctx: ctx, cancel: cancel, close: close}
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
	c.Samples = slices.Clone(c.Samples)
	select {
	case q.commands <- c:
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
	return q.close()
}

func (q *Queue) Run(send func(session.Command) error) error {
	for {
		select {
		case <-q.ctx.Done():
			return q.ctx.Err()
		case c := <-q.commands:
			if err := q.ctx.Err(); err != nil {
				return err
			}
			if err := send(c); err != nil {
				return err
			}
		}
	}
}
