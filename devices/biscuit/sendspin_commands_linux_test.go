package biscuit

import (
	"testing"

	"github.com/farooqu/encore-satellite/core/music"
	"github.com/farooqu/encore-satellite/platforms/linuxsendspin"
)

func TestSendspinDispatchKeepsLocalStopDistinct(t *testing.T) {
	player, sample := buttonMusic(t)
	m := newMusic(Config{Sendspin: true}, nil, player)
	m.running = true

	expect := func(want linuxsendspin.Command) {
		t.Helper()
		select {
		case got := <-m.commands:
			if got != want {
				t.Fatalf("queued command = %v, want %v", got, want)
			}
		default:
			t.Fatalf("command %v not queued", want)
		}
	}
	m.command(music.Play)
	// Pause is not implemented and must not turn into Stop or replace Play.
	m.command(music.Pause)
	expect(linuxsendspin.Play)
	if sample() != 6000 {
		t.Fatal("unsupported command silenced music")
	}

	m.command(music.Stop)
	expect(linuxsendspin.Stop)
	buttonWait(t, "explicit Stop did not silence local music", func() bool { return sample() == 0 })

	m.revision++
	m.command(music.Play)
	select {
	case <-m.commands:
		t.Fatal("queued Play for a stale connection")
	default:
	}
	if sample() != 0 {
		t.Fatal("stale Play released local Stop")
	}

	m.runningRevision = m.revision
	m.command(music.Play)
	expect(linuxsendspin.Play)
	buttonWait(t, "explicit Play did not release local Stop", func() bool { return sample() == 6000 })
}
