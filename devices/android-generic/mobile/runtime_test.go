package mobile_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/farooqu/encore-satellite/devices/android-generic/mobile"
	"github.com/ygelfand/go-esphome-device/api"
)

type output struct{}

func (output) WriteVoice([]byte, int64) bool { return true }
func (output) DrainVoice(int64) bool         { return true }
func (output) FlushVoice(int64)              {}

func TestUnpairedIdentityAndPrivacySurviveReopen(t *testing.T) {
	directory := t.TempDir()
	r, err := mobile.Open(directory, output{})
	if err != nil {
		t.Fatal(err)
	}
	name, key := r.Name(), r.NoiseKey()
	if name == "" || key != "" {
		t.Fatal("fresh runtime must have a stable identity but no key")
	}
	if err := r.SetMuted(true); err != nil {
		t.Fatal(err)
	}
	r.Close()
	next, err := mobile.Open(directory, output{})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if next.Name() != name || next.NoiseKey() != key {
		t.Fatal("pairing identity changed after restart")
	}
	var snapshot struct {
		Voice struct {
			Muted bool `json:"muted"`
		} `json:"voice"`
	}
	if err := json.Unmarshal([]byte(next.Snapshot(false)), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.Voice.Muted {
		t.Fatalf("lost local setup/privacy: %+v", snapshot)
	}
}

func TestFailedMuteSurvivesUnrelatedSaveAndReopen(t *testing.T) {
	directory := t.TempDir()
	r, err := mobile.Open(directory, output{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	assertMuted := func(want bool) {
		t.Helper()
		var snapshot struct {
			Voice struct {
				Muted bool `json:"muted"`
			} `json:"voice"`
		}
		if err := json.Unmarshal([]byte(r.Snapshot(false)), &snapshot); err != nil {
			t.Fatal(err)
		}
		if snapshot.Voice.Muted != want {
			t.Fatalf("muted = %v, want %v", snapshot.Voice.Muted, want)
		}
	}
	assertMuted(false)
	tmp := filepath.Join(directory, "encore.json.tmp")
	if err := os.Mkdir(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	if err := r.SetMuted(true); err == nil {
		t.Fatal("mute unexpectedly saved through obstructed temporary file")
	}
	assertMuted(true)
	if err := os.Remove(tmp); err != nil {
		t.Fatal(err)
	}
	if err := r.Handle(context.Background(), nil, &api.SelectCommandRequest{Key: 0x454e4304, State: "stable"}); err != nil {
		t.Fatal(err)
	}
	r.Close()
	r, err = mobile.Open(directory, output{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	assertMuted(true)

	// A failed explicit unmute must retain the latch as well.
	if err := os.Mkdir(tmp, 0700); err != nil {
		t.Fatal(err)
	}
	if err := r.SetMuted(false); err == nil {
		t.Fatal("unmute unexpectedly saved through obstructed temporary file")
	}
	assertMuted(true)
	if err := os.Remove(tmp); err != nil {
		t.Fatal(err)
	}
	if err := r.SetMuted(false); err != nil {
		t.Fatal(err)
	}
	assertMuted(false)
	r.Close()
	r, err = mobile.Open(directory, output{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	assertMuted(false)

}

func TestDashboardReloadWithoutURL(t *testing.T) {
	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, "encore.json"), []byte(`{"mac":"02:11:22:ab:cd:ef","dashboard":"https://old.example/dashboard"}`), 0600); err != nil {
		t.Fatal(err)
	}
	r, err := mobile.Open(directory, output{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	if err := r.ConfigureDashboard(); err != nil {
		t.Fatal(err)
	}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	for _, command := range []struct {
		key  uint32
		want int
	}{{0x454e4301, 0}, {0x454e4302, 1}} {
		if command.want == 0 {
			err = r.Handle(t.Context(), nil, &api.TextCommandRequest{Key: command.key, State: "https://ignored.example/dashboard"})
		} else {
			err = r.Handle(t.Context(), nil, &api.ButtonCommandRequest{Key: command.key})
		}
		if err != nil {
			t.Fatal(err)
		}
		var snapshot struct {
			Dashboard *string `json:"dashboard"`
			Reload    int     `json:"reload"`
		}
		if err := json.Unmarshal([]byte(r.Snapshot(false)), &snapshot); err != nil || snapshot.Dashboard != nil || snapshot.Reload != command.want {
			t.Fatalf("unexpected dashboard snapshot for key %x: %+v (%v)", command.key, snapshot, err)
		}
	}
}
