package session_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/core/session"
)

func TestLocalAnnouncementOwnsOutputUntilPhysicalDrain(t *testing.T) {
	for _, connected := range []bool{false, true} {
		p := &player{entered: make(chan struct{}), release: make(chan struct{})}
		e := session.New(p)
		l := &link{backend: session.Incubator}
		var generation uint64
		if connected {
			var err error
			generation, err = e.Attach(l)
			if err != nil {
				t.Fatal(err)
			}
		}
		var active atomic.Bool
		e.SetActivityHandler(active.Store)
		done := make(chan session.AnnouncementResult, 1)
		go func() {
			result, err := e.Announce(t.Context(), func(ctx context.Context, write func([]int16) error) error {
				return write([]int16{37, -901, 173})
			})
			if err != nil {
				t.Error(err)
			}
			done <- result
		}()
		<-p.entered
		if s := e.Snapshot(); s.Output.Owner != "announcement" || s.Output.State != "draining" || !s.Active || !active.Load() {
			t.Fatal("announcement failed to retain physical output ownership", s)
		}
		if connected {
			if len(l.commands) != 1 || l.commands[0].Kind != "announcing" {
				t.Fatal("start was missing or completion preceded drain", l.commands)
			}
			_ = e.Handle(generation, session.Event{Kind: "output_start"})
			_ = e.Output(generation, []int16{17, 29, -41, 53, 67})
			_ = e.EndOutput(generation) // Wire audio cannot steal/end HTTP output.
		}
		close(p.release)
		if got := <-done; got != (session.AnnouncementResult{AcceptedBlocks: 1, AcceptedSamples: 3, Outcome: "completed", Drain: "ok"}) {
			t.Fatal("wrong completion receipt", got)
		}
		if p.writes != 3 || active.Load() || e.Snapshot().Output.State != "" {
			t.Fatal("unexpected PCM or retained ownership", p.writes, e.Snapshot())
		}
		if connected && (len(l.commands) != 2 || l.commands[1].Kind != "announced") {
			t.Fatal("missing physical completion notification", l.commands)
		}
		if connected {
			if err := e.StartManual(); err != nil {
				t.Fatal(err)
			}
			if err := e.Handle(generation, session.Event{Kind: "listening"}); err != nil {
				t.Fatal(err)
			}
			if err := e.StopAnnouncement(nil); err != nil || !e.Snapshot().Active {
				t.Fatal("late announcement Stop cancelled a newer voice turn", err)
			}
			if err := e.Output(generation, []int16{71, -38}); err != nil || p.writes != 5 {
				t.Fatal("late announcement Stop fenced newer voice audio", err)
			}
		}
	}
}

func TestLocalAnnouncementCancellationFencesBlockedFetch(t *testing.T) {
	for _, tc := range []struct{ action, outcome string }{
		{"stop", "session_stop"}, {"announcement stop", "session_stop"}, {"privacy", "mute_fence"},
		{"privacy then request stop", "mute_fence"}, {"request stop then privacy", "parent_canceled"},
		{"parent", "parent_canceled"}, {"disconnect", "session_disconnected"}, {"hangup", "native_hangup"},
	} {
		t.Run(tc.action, func(t *testing.T) {
			e, _, p, generation := fixture(t)
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			done := make(chan session.AnnouncementResult, 1)
			go func() {
				result, err := e.Announce(parent, func(ctx context.Context, write func([]int16) error) error {
					close(entered)
					<-ctx.Done()
					<-release // Late HTTP cleanup cannot clear a newer output.
					return write([]int16{991})
				})
				if !errors.Is(err, context.Canceled) {
					t.Error(err)
				}
				done <- result
			}()
			<-entered
			switch tc.action {
			case "stop":
				_ = e.Stop()
			case "announcement stop":
				_ = e.StopAnnouncement(nil)
			case "privacy then request stop", "request stop then privacy":
				firstMute := tc.action == "privacy then request stop"
				if firstMute {
					e.SetMuted(true)
				}
				cancel()
				_ = e.StopAnnouncement(errors.New("request stop"))
				if !firstMute {
					e.SetMuted(true)
				}
				e.SetMuted(false)
			case "privacy":
				e.SetMuted(true)
				if result, err := e.Announce(parent, func(context.Context, func([]int16) error) error {
					t.Fatal("muted announcement fetched audio")
					return nil
				}); !errors.Is(err, session.ErrUnavailable) || result.Outcome != "unavailable" {
					t.Fatal(result, err)
				}
				e.SetMuted(false)
			case "parent":
				cancel()
			case "disconnect":
				e.Disconnect(generation, "test")
			case "hangup":
				_ = e.Handle(generation, session.Event{Kind: "hangup", Reason: "answered_elsewhere"})
			}
			close(release)
			select {
			case got := <-done:
				if got != (session.AnnouncementResult{Outcome: tc.outcome, Drain: "not_attempted"}) {
					t.Fatal("wrong cancellation receipt", got)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation waited on network cleanup")
			}
			if p.writes != 0 {
				t.Fatal("cancelled fetch played late audio")
			}
			if _, err := e.Announce(t.Context(), func(ctx context.Context, write func([]int16) error) error {
				return write([]int16{-47, 301})
			}); err != nil || p.writes != 2 {
				t.Fatal("cancelled standalone announcement poisoned later output", err)
			}
		})
	}
}
