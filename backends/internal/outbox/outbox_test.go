package outbox_test

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/farooqu/encore-satellite/backends/internal/outbox"
	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/core/session"
)

type assistLink struct{ *outbox.Queue }

func (*assistLink) Backend() session.Backend { return session.Assist }

type speaker struct{}

func (*speaker) Write(context.Context, []int16) error { return nil }
func (*speaker) Drain(context.Context) error          { return nil }
func (*speaker) Flush()                               {}

func TestMuteBeforeRunStartDropsQueuedCaptureWithoutAbortingEarly(t *testing.T) {
	l := &assistLink{outbox.New(func() error { return nil })}
	defer l.Close()
	e := session.New(&speaker{})
	_ = e.Select(session.Assist)
	g, _ := e.Attach(l)
	_ = e.StartManual()
	_ = e.Capture(audio.Frame{Samples: []int16{17, -83}})
	e.SetMuted(true)
	_ = l.Send(session.Command{Kind: "drained"}) // End the delayed writer probe.
	finished := errors.New("probe complete")
	var kinds []string
	_ = l.Run(func(c session.Command) error {
		kinds = append(kinds, c.Kind)
		if c.Kind == "drained" {
			return finished
		}
		return nil
	})
	if !slices.Equal(kinds, []string{"start", "drained"}) {
		t.Fatal("sent muted capture or aborted before RUN_START", kinds)
	}
	_ = e.Handle(g, session.Event{Kind: "run_start"})
	_ = l.Run(func(c session.Command) error {
		if c.Kind != "cancel" {
			t.Fatal("missing deferred abort", c.Kind)
		}
		return finished
	})
}

func TestCancelDropsQueuedCaptureButRetainsStartAndControls(t *testing.T) {
	q := outbox.New(func() error { return nil })
	defer q.Close()
	for _, c := range []session.Command{{Kind: "drained"}, {Kind: "start"}} {
		if err := q.Send(c); err != nil {
			t.Fatal(err)
		}
	}
	for range 62 {
		if err := q.Send(session.Command{Kind: "audio", Samples: []int16{17, -89}}); err != nil {
			t.Fatal(err)
		}
	}
	if err := q.Send(session.Command{Kind: "cancel"}); err != nil {
		t.Fatal("cancellation queued behind a full microphone backlog", err)
	}
	var kinds []string
	finished := errors.New("received cancellation")
	err := q.Run(func(c session.Command) error {
		kinds = append(kinds, c.Kind)
		if c.Kind == "cancel" {
			return finished
		}
		return nil
	})
	if !errors.Is(err, finished) || !slices.Equal(kinds, []string{"drained", "start", "cancel"}) {
		t.Fatal("cancel lost pipeline start/control or sent queued capture", kinds, err)
	}
}
