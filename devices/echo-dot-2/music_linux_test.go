package dot

import (
	"context"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/platforms/linuxsendspin"
)

func TestMusicSwitchLifecycleAndIndependentPersistence(t *testing.T) {
	c := Config{Schema: 1, Name: "test-dot"}
	s := &settings{path: filepath.Join(t.TempDir(), "config.json"), config: c}
	if err := SaveConfig(s.path, c); err != nil {
		t.Fatal(err)
	}
	player := newSpeaker(&speakerPCM{})
	defer player.Close()
	m := newMusic(c, s, player)
	started := make(chan string, 8)
	var live, maximum atomic.Int32
	m.run = func(ctx context.Context, o linuxsendspin.Options) error {
		n := live.Add(1)
		if n > maximum.Load() {
			maximum.Store(n)
		}
		defer live.Add(-1)
		started <- o.URL
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-started:
		t.Fatal("default off started music")
	case <-time.After(15 * time.Millisecond):
	}
	yes, no := true, false
	if err := m.configure(&yes, nil); err == nil {
		t.Fatal("enabled music without server configuration")
	}
	url := "ws://ma.local:8927/sendspin"
	if err := m.configure(nil, &url); err != nil {
		t.Fatal(err)
	}
	var writes sync.WaitGroup
	writes.Go(func() {
		if err := s.update(func(c *Config) { c.BluetoothProxy = true }); err != nil {
			t.Error(err)
		}
	})
	writes.Go(func() {
		if err := m.configure(&yes, nil); err != nil {
			t.Error(err)
		}
	})
	writes.Wait()
	select {
	case got := <-started:
		if got != url {
			t.Fatal("wrong endpoint")
		}
	case <-time.After(time.Second):
		t.Fatal("music did not start")
	}
	got, err := LoadConfig(s.path)
	if err != nil || !got.BluetoothProxy || !got.Sendspin || got.SendspinURL != url || got.Name != c.Name {
		t.Fatalf("independent setting lost: %+v %v", got, err)
	}
	if err := m.configure(&no, nil); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Second)
	for live.Load() != 0 {
		if time.Now().After(until) {
			t.Fatal("off did not join child")
		}
		time.Sleep(time.Millisecond)
	}
	// OFF must stay off; commands from that period cannot turn into autoplay.
	m.command(linuxsendspin.Play)
	select {
	case <-m.commands:
		t.Fatal("queued Play while disabled")
	default:
	}
	select {
	case <-started:
		t.Fatal("off restarted receiver")
	case <-time.After(15 * time.Millisecond):
	}
	s.path = filepath.Join(t.TempDir(), "missing", "config.json")
	if err := m.configure(&yes, nil); err == nil {
		t.Fatal("accepted failed persistence")
	}
	if enabled, _, _ := m.snapshot(); enabled {
		t.Fatal("failed save changed live music switch")
	}
	if maximum.Load() != 1 {
		t.Fatalf("overlapping music children: %d", maximum.Load())
	}
}
