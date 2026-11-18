package session

import (
	"context"
	"errors"
)

// StopAnnouncement cannot cancel an unrelated voice turn that won ownership
// after an HTTP announcement finished but before its worker finished cleanup.
func (e *Engine) StopAnnouncement() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.localOutput != nil {
		return e.stop()
	}
	return nil
}

// Announce owns the shared voice lane while stream fetches/decodes a local media
// URL. The stream must honor ctx and call write serially with bounded PCM16/16k
// mono blocks. Stop, privacy, disconnect and parent cancellation fence it just
// like wire audio. No backend is required for standalone HA announcements.
func (e *Engine) Announce(parent context.Context, stream func(context.Context, func([]int16) error) error) error {
	e.mu.Lock()
	if err := parent.Err(); err != nil {
		e.mu.Unlock()
		return err
	}
	if e.state.Muted || e.cancelling || e.discard || e.outputOpen || e.state.Alert {
		e.mu.Unlock()
		return ErrUnavailable
	}
	e.resetOutput()
	ctx, cancel := e.output, e.cancelOutput
	generation := e.generation
	e.localOutput = ctx
	e.openOutput()
	if !e.active {
		e.outputOwner = "announcement"
	}
	e.publishActivity()
	e.mu.Unlock()
	stop := context.AfterFunc(parent, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.localOutput == ctx {
			cancel()
			e.player.Flush()
		}
	})
	defer stop()

	started := false
	err := stream(ctx, func(samples []int16) error {
		if len(samples) == 0 || len(samples) > 2048 {
			return errors.New("session: invalid announcement block")
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		if err := e.player.Write(ctx, samples); err != nil {
			return err
		}
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.localOutput != ctx || ctx.Err() != nil {
			return context.Canceled
		}
		e.outputReceived, e.outputAccepted = true, true
		if !started {
			started = true
			if e.link != nil && e.generation == generation && e.state.Backend == string(Incubator) {
				return e.send(Command{Kind: "announcing"})
			}
		}
		return nil
	})
	if err == nil && !started {
		err = errors.New("session: announcement contained no PCM")
	}
	if err == nil {
		e.mu.Lock()
		if e.localOutput == ctx && ctx.Err() == nil {
			e.outputDraining = true
		} else {
			err = context.Canceled
		}
		e.mu.Unlock()
		if err == nil {
			err = e.player.Drain(ctx)
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.localOutput != ctx {
		return context.Canceled // A newer owner already replaced/flushed this output.
	}
	if ctx.Err() != nil || parent.Err() != nil {
		err = context.Canceled
	}
	if err != nil {
		cancel()
		e.player.Flush()
	}
	e.outputOpen, e.outputTimer = false, false
	e.resetOutput()
	e.publishActivity()
	if err == nil && e.link != nil && e.generation == generation && e.state.Backend == string(Incubator) {
		return e.send(Command{Kind: "announced"})
	}
	return err
}
