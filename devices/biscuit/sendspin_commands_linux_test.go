package biscuit

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/core/music"
	"github.com/farooqu/encore-satellite/platforms/linuxsendspin"
)

func TestSendspinActionDoesNotTreatUnrelatedStopAsPause(t *testing.T) {
	playing := linuxsendspin.Readiness{Generation: 1, Group: 1, Ready: true, Playing: true}
	stopped := linuxsendspin.Readiness{Generation: 2, Group: 1, Ready: true, Stopped: true}
	for _, tc := range []struct {
		name    string
		pending time.Time
		next    []linuxsendspin.Readiness
	}{
		{"external stop", time.Time{}, []linuxsendspin.Readiness{stopped}},
		{"late stop", time.Now().Add(-time.Second), []linuxsendspin.Readiness{stopped}},
		{"authorization lost", time.Now().Add(time.Minute), []linuxsendspin.Readiness{{}, stopped}},
		{"regroup during pause", time.Now().Add(time.Minute), []linuxsendspin.Readiness{{Generation: 2, Group: 2, Ready: true, Stopped: true}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := musicWorker{ready: playing, pausePending: tc.pending}
			for _, snapshot := range tc.next {
				w.updateReadiness(snapshot)
			}
			if w.resumePaused {
				t.Fatal("unrelated stop armed Action resume")
			}
		})
	}
}

func TestSendspinDispatchKeepsLocalStopDistinct(t *testing.T) {
	player, sample := buttonMusic(t)
	m := newMusic(Config{Sendspin: true}, nil, player)
	w := &musicWorker{commands: make(chan linuxsendspin.CommandRequest, 1)}
	m.worker = w

	m.command(music.Stop) // must silence even before a connection is ready
	buttonWait(t, "offline Stop did not silence local music", func() bool { return sample() == 0 })
	m.command(music.Play)
	if len(w.commands) != 0 || sample() != 0 {
		t.Fatal("Play escaped the readiness gate")
	}
	w.ready = linuxsendspin.Readiness{Generation: 1, Ready: true, Commands: 0b01111}

	expect := func(want linuxsendspin.Command) {
		t.Helper()
		select {
		case got := <-w.commands:
			if got.Command != want || got.Generation != w.ready.Generation {
				t.Fatalf("queued command = %v, want %v", got, want)
			}
		default:
			t.Fatalf("command %v not queued", want)
		}
	}
	m.command(music.Play)
	// A command the server does not advertise must not replace a valid one.
	m.command(music.Previous)
	expect(linuxsendspin.Play)
	buttonWait(t, "explicit Play did not release local Stop", func() bool { return sample() == 6000 })
	m.command(music.Pause)
	expect(linuxsendspin.Pause)
	m.command(music.Next)
	expect(linuxsendspin.Next)

	m.command(music.Stop)
	expect(linuxsendspin.Stop)
	buttonWait(t, "explicit Stop did not silence local music", func() bool { return sample() == 0 })

	m.revision++
	m.command(music.Play)
	if len(w.commands) != 0 {
		t.Fatal("queued Play for a stale connection")
	}
	if sample() != 0 {
		t.Fatal("stale Play released local Stop")
	}

	w.revision = m.revision
	w.ready = linuxsendspin.Readiness{Generation: 2, Ready: true}
	m.command(music.Play)
	if len(w.commands) != 0 {
		t.Fatal("forwarded Play without a server capability")
	}
	buttonWait(t, "local Resume requires server Play capability", func() bool { return sample() == 6000 })
	player.SetMusicPrivacy(true)
	m.command(music.Play)
	buttonWait(t, "Resume released active privacy", func() bool { return sample() == 0 })
	player.SetMusicPrivacy(false)
	if sample() != 0 {
		t.Fatal("privacy release undid the local latch")
	}
}

func TestSendspinWorkerReplacementCannotInheritReadinessOrCommands(t *testing.T) {
	player, sample := buttonMusic(t)
	c := Config{Schema: 1, Name: "test-dot", Sendspin: true}
	m := newMusic(c, &settings{path: filepath.Join(t.TempDir(), "config.json"), config: c}, player)
	endpoints := make(chan string, 2)
	endpoints <- "ws://192.0.2.4:8931/sendspin"
	url := "ws://192.0.2.5:8932/sendspin"
	endpoints <- url
	m.discover = func(ctx context.Context) (string, error) {
		select {
		case endpoint := <-endpoints:
			return endpoint, nil
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	started := make(chan linuxsendspin.Options)
	exit := make(chan struct{})
	m.run = func(ctx context.Context, o linuxsendspin.Options) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case started <- o:
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-exit:
			return errors.New("test transport lost")
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	defer func() { cancel(); <-done }()
	start := func() linuxsendspin.Options {
		t.Helper()
		select {
		case o := <-started:
			return o
		case <-time.After(time.Second):
			t.Fatal("companion did not start")
			return linuxsendspin.Options{}
		}
	}
	first := start()
	ready := linuxsendspin.Readiness{Generation: 1, Ready: true, Commands: 1}
	first.OnReadiness(ready)
	m.command(music.Play)
	buttonWait(t, "old worker did not queue Play", func() bool { return len(first.Commands) == 1 })
	exit <- struct{}{}
	buttonWait(t, "exit did not silence the old stream", func() bool { return sample() == 0 })
	if err := m.configure(false); err != nil {
		t.Fatal(err)
	}
	if err := m.configure(true); err != nil {
		t.Fatal(err)
	}
	second := start() // change bypasses the old target's retry delay
	if second.URL != url || second.Commands == first.Commands || len(second.Commands) != 0 {
		t.Fatal("replacement inherited the old worker target or queue")
	}
	first.OnReadiness(ready)
	m.command(music.Play)
	if len(second.Commands) != 0 || sample() != 0 {
		t.Fatal("stale readiness enabled a replacement")
	}
	second.OnReadiness(ready)
	if sample() != 0 {
		t.Fatal("connection alone released local Stop")
	}
	m.command(music.Play)
	buttonWait(t, "new authenticated worker did not resume", func() bool { return sample() == 6000 })
	second.OnReadiness(linuxsendspin.Readiness{Generation: 2})
	buttonWait(t, "lost readiness did not silence output", func() bool { return sample() == 0 })
}
