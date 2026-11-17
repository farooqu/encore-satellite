package mobile_test

import (
	"encoding/json"
	"testing"

	"github.com/farooqu/encore-satellite/devices/android-generic/mobile"
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
