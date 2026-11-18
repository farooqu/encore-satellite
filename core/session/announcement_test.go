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
		done := make(chan error, 1)
		go func() {
			done <- e.Announce(t.Context(), func(ctx context.Context, write func([]int16) error) error {
				return write([]int16{37, -901, 173})
			})
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
		if err := <-done; err != nil {
			t.Fatal(err)
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
			if err := e.StopAnnouncement(); err != nil || !e.Snapshot().Active {
				t.Fatal("late announcement Stop cancelled a newer voice turn", err)
			}
			if err := e.Output(generation, []int16{71, -38}); err != nil || p.writes != 5 {
				t.Fatal("late announcement Stop fenced newer voice audio", err)
			}
		}
	}
}

func TestLocalAnnouncementCancellationFencesBlockedFetch(t *testing.T) {
	for _, action := range []string{"stop", "announcement stop", "privacy", "parent", "disconnect"} {
		t.Run(action, func(t *testing.T) {
			e, _, p, generation := fixture(t)
			parent, cancel := context.WithCancel(t.Context())
			defer cancel()
			entered, release := make(chan struct{}), make(chan struct{})
			done := make(chan error, 1)
			go func() {
				done <- e.Announce(parent, func(ctx context.Context, write func([]int16) error) error {
					close(entered)
					<-ctx.Done()
					<-release // Late HTTP cleanup cannot clear a newer output.
					return write([]int16{991})
				})
			}()
			<-entered
			switch action {
			case "stop":
				_ = e.Stop()
			case "announcement stop":
				_ = e.StopAnnouncement()
			case "privacy":
				e.SetMuted(true)
				if err := e.Announce(parent, func(context.Context, func([]int16) error) error {
					t.Fatal("muted announcement fetched audio")
					return nil
				}); !errors.Is(err, session.ErrUnavailable) {
					t.Fatal(err)
				}
				e.SetMuted(false)
			case "parent":
				cancel()
			case "disconnect":
				e.Disconnect(generation, "test")
			}
			close(release)
			select {
			case err := <-done:
				if !errors.Is(err, context.Canceled) {
					t.Fatal(err)
				}
			case <-time.After(time.Second):
				t.Fatal("cancellation waited on network cleanup")
			}
			if p.writes != 0 {
				t.Fatal("cancelled fetch played late audio")
			}
			if err := e.Announce(t.Context(), func(ctx context.Context, write func([]int16) error) error {
				return write([]int16{-47, 301})
			}); err != nil || p.writes != 2 {
				t.Fatal("cancelled standalone announcement poisoned later output", err)
			}
		})
	}
}
