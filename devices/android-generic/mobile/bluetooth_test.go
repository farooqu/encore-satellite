package mobile_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/farooqu/encore-satellite/devices/android-generic/mobile"
)

func TestBluetoothChoiceIsIndependentPersistentAndSaveFailureAtomic(t *testing.T) {
	directory := t.TempDir()
	r, err := mobile.Open(directory, output{})
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()
	if r.BluetoothEnabled() {
		t.Fatal("proxy defaults on")
	}
	if err := r.SetMuted(true); err != nil {
		t.Fatal(err)
	}
	if err := r.SetBluetoothEnabled(true); err != nil {
		t.Fatal(err)
	}
	r.PauseBluetooth(true)
	next, err := mobile.Open(directory, output{})
	if err != nil {
		t.Fatal(err)
	}
	defer next.Close()
	if !next.BluetoothEnabled() {
		t.Fatal("pause lost persisted intent")
	}
	if err := os.Mkdir(filepath.Join(directory, "encore.json.tmp"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := r.SetBluetoothEnabled(false); err == nil {
		t.Fatal("save failure ignored")
	}
	if !r.BluetoothEnabled() {
		t.Fatal("failed save changed live state")
	}
	if err := os.Remove(filepath.Join(directory, "encore.json.tmp")); err != nil {
		t.Fatal(err)
	}
	if err := r.SetBluetoothEnabled(false); err != nil {
		t.Fatal(err)
	}
	var snapshot struct {
		Voice struct {
			Muted bool `json:"muted"`
		} `json:"voice"`
	}
	if err := json.Unmarshal([]byte(r.Snapshot()), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.Voice.Muted {
		t.Fatal("BLE changed microphone mute")
	}
}
