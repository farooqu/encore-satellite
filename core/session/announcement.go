package session

import (
	"context"
	"errors"

	"github.com/farooqu/encore-satellite/core/audio"
)

type outputFence string

func (f outputFence) Error() string { return string(f) }

type requestCanceled struct{ error }

// AnnouncementResult is safe to log. Acceptance counts whole blocks whose
// Write succeeded, not samples heard or partially accepted by a failed Write.
type AnnouncementResult struct {
	AcceptedBlocks, AcceptedSamples int
	Outcome, Drain                  string
}

// StopAnnouncement cannot cancel an unrelated voice turn that won ownership
// after an HTTP announcement finished but before its worker finished cleanup.
// Pass the request's cancellation cause when stopping on behalf of its owner.
func (e *Engine) StopAnnouncement(cause error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.localOutput != nil {
		if cause != nil {
			e.cancelOutput(requestCanceled{cause})
		}
		return e.stop()
	}
	return nil
}

// Announce owns the shared voice lane while stream fetches/decodes a local media
// URL. The stream must honor ctx and call write serially with bounded PCM16/16k
// mono blocks. Stop, privacy, disconnect and parent cancellation fence it just
// like wire audio. No backend is required for standalone HA announcements.
func (e *Engine) Announce(parent context.Context, stream func(context.Context, func([]int16) error) error) (AnnouncementResult, error) {
	result := AnnouncementResult{Drain: "not_attempted"}
	e.mu.Lock()
	if err := parent.Err(); err != nil {
		e.mu.Unlock()
		result.Outcome = "parent_canceled"
		return result, err
	}
	if e.state.Muted || e.cancelling || e.discard || e.outputOpen || e.state.Alert {
		e.mu.Unlock()
		result.Outcome = "unavailable"
		return result, ErrUnavailable
	}
	e.resetOutput()
	ctx, cancel := e.output, e.cancelOutput
	output := ctx
	if player, ok := e.player.(audio.PlaybackStream); ok {
		output = player.StreamContext(ctx)
	}
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
			cancel(requestCanceled{context.Cause(parent)})
			e.player.Flush()
		}
	})
	defer stop()

	result.Outcome = "stream_failed"
	err := stream(output, func(samples []int16) error {
		if len(samples) == 0 || len(samples) > 2048 {
			return errors.New("session: invalid announcement block")
		}
		if err := output.Err(); err != nil {
			return err
		}
		if err := e.player.Write(output, samples); err != nil {
			result.Outcome = "write_failed"
			return err
		}
		result.AcceptedBlocks++
		result.AcceptedSamples += len(samples)
		e.mu.Lock()
		defer e.mu.Unlock()
		if e.localOutput != ctx || output.Err() != nil {
			return context.Canceled
		}
		e.outputReceived, e.outputAccepted = true, true
		if result.AcceptedBlocks == 1 {
			if e.link != nil && e.generation == generation && e.state.Backend == string(Incubator) {
				return e.send(Command{Kind: "announcing"})
			}
		}
		return nil
	})
	if err == nil && result.AcceptedBlocks == 0 {
		err = errors.New("session: announcement contained no PCM")
	}
	if err == nil {
		e.mu.Lock()
		if e.localOutput == ctx && output.Err() == nil {
			e.outputDraining = true
		} else {
			err = context.Canceled
		}
		e.mu.Unlock()
		if err == nil {
			err = e.player.Drain(output)
			switch {
			case err == nil:
				result.Drain, result.Outcome = "ok", "completed"
			case errors.Is(err, context.Canceled), errors.Is(err, context.DeadlineExceeded):
				result.Drain, result.Outcome = "canceled", "drain_failed"
			default:
				result.Drain, result.Outcome = "failed", "drain_failed"
			}
		}
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	cause := context.Cause(output)
	if cause == audio.ErrPlaybackUnderrun {
		result.Outcome, err = "playback_underrun", cause
	} else if cause == audio.ErrPlaybackInterrupted {
		result.Outcome, err = "playback_interrupted", cause
	} else if fence, ok := cause.(outputFence); ok {
		result.Outcome, err = string(fence), context.Canceled
	} else if _, ok := cause.(requestCanceled); ok || (cause == nil && parent.Err() != nil) {
		result.Outcome, err = "parent_canceled", context.Canceled
	} else if cause != nil || e.localOutput != ctx || errors.Is(err, context.Canceled) {
		result.Outcome, err = "canceled", context.Canceled
	}
	if e.localOutput != ctx {
		return result, err // A newer owner already replaced/flushed this output.
	}
	if err != nil {
		cancel(nil)
		e.player.Flush()
	}
	e.outputOpen, e.outputTimer = false, false
	e.resetOutput()
	e.publishActivity()
	if err == nil && e.link != nil && e.generation == generation && e.state.Backend == string(Incubator) {
		if err = e.send(Command{Kind: "announced"}); err != nil {
			result.Outcome = "completion_notification_failed"
		}
	}
	return result, err
}
