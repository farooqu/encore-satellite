package mobile_test

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/farooqu/encore-satellite/devices/android-generic/mobile"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"
)

type output struct{}

func (output) WriteVoice([]byte, int64) bool { return true }
func (output) DrainVoice(int64) bool         { return true }
func (output) FlushVoice(int64)              {}

func TestLocalIdentityAndPrivacySurviveReopen(t *testing.T) {
	directory := t.TempDir()
	r, err := mobile.Open(directory, output{})
	if err != nil {
		t.Fatal(err)
	}
	name, key := r.Name(), r.NoiseKey()
	if name == "" || key == "" {
		t.Fatal("missing stable pairing identity")
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
		Dashboard string `json:"dashboard"`
		Voice     struct {
			Muted bool `json:"muted"`
		} `json:"voice"`
	}
	if err := json.Unmarshal([]byte(next.Snapshot()), &snapshot); err != nil {
		t.Fatal(err)
	}
	if snapshot.Dashboard != "" || !snapshot.Voice.Muted {
		t.Fatalf("lost local setup/privacy: %+v", snapshot)
	}
}

func TestFailedMuteSurvivesUnrelatedSaveAndReopen(t *testing.T) {
	for _, setting := range []string{"channel", "dashboard"} {
		t.Run(setting, func(t *testing.T) {
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
				if err := json.Unmarshal([]byte(r.Snapshot()), &snapshot); err != nil {
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
			var command proto.Message
			if setting == "channel" {
				command = &api.SelectCommandRequest{Key: 0x454e4304, State: "stable"}
			} else {
				command = &api.TextCommandRequest{Key: 0x454e4301, State: "https://dashboard.example.test"}
			}
			if err := r.Handle(context.Background(), nil, command); err != nil {
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
		})
	}
}
