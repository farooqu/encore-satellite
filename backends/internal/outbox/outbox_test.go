package outbox_test

import (
	"errors"
	"slices"
	"testing"

	"github.com/farooqu/encore-satellite/backends/internal/outbox"
	"github.com/farooqu/encore-satellite/core/session"
)

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

func TestMonitorOffPurgesQueuedMonitorBeforeRearm(t *testing.T) {
	q := outbox.New(func() error { return nil })
	defer q.Close()
	for _, c := range []session.Command{
		{Kind: "announced"},
		{Kind: "monitor_audio", MonitorID: 1, Samples: []int16{100}},
		{Kind: "wake_report", MonitorID: 1},
		{Kind: "monitor_off", MonitorID: 1},
		{Kind: "monitor_audio", MonitorID: 2, Samples: []int16{-200}},
		{Kind: "wake_report", MonitorID: 2},
		{Kind: "drained"},
	} {
		if err := q.Send(c); err != nil {
			t.Fatal(err)
		}
	}
	var kinds []string
	_ = q.Run(func(c session.Command) error {
		kinds = append(kinds, c.Kind)
		if c.Kind == "monitor_audio" && (c.MonitorID != 2 || !slices.Equal(c.Samples, []int16{-200})) {
			t.Fatal("revoked monitor data reached writer", c)
		}
		if c.Kind == "wake_report" && c.MonitorID != 2 {
			t.Fatal("revoked monitor report reached writer", c)
		}
		if c.Kind == "drained" {
			return errors.New("finished")
		}
		return nil
	})
	if !slices.Equal(kinds, []string{"announced", "monitor_audio", "wake_report", "drained"}) {
		t.Fatal("monitor off lost unrelated commands", kinds)
	}
}
