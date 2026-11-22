package mobile_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/farooqu/encore-satellite/devices/android-generic/mobile"
)

func TestBluetoothDefaultOnlyOnFreshSupportedInstall(t *testing.T) {
	for _, sample := range []struct {
		name, existing              string
		supported, desired, pending bool
	}{
		{"fresh BLE", "", true, true, true},
		{"no BLE", "", false, false, false},
		{"upgrade on", `,"bluetooth_proxy":true`, true, true, false},
		{"upgrade off", `,"bluetooth_proxy":false`, true, false, false},
		{"legacy absent", " ", true, false, false},
	} {
		t.Run(sample.name, func(t *testing.T) {
			dir := t.TempDir()
			if sample.existing != "" {
				if err := os.WriteFile(filepath.Join(dir, "encore.json"), []byte(`{"mac":"02:13:24:35:0a:b2"`+sample.existing+`}`), 0600); err != nil {
					t.Fatal(err)
				}
			}
			r, err := mobile.OpenWithBluetoothDefault(dir, output{}, sample.supported)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if r.BluetoothEnabled() != sample.desired || r.BluetoothSetupPending() != sample.pending {
				t.Fatalf("desired/pending = %v/%v, want %v/%v", r.BluetoothEnabled(), r.BluetoothSetupPending(), sample.desired, sample.pending)
			}
			if !sample.pending {
				return
			}
			// Explicit Off is distinct from leaving setup pending, and neither creates approval.
			if err := r.SetBluetoothEnabled(false); err != nil {
				t.Fatal(err)
			}
			r.Close()
			r, err = mobile.OpenWithBluetoothDefault(dir, output{}, true)
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if r.BluetoothEnabled() || !r.BluetoothSetupPending() {
				t.Fatal("reopen overwrote explicit Off or treated it as scan approval")
			}
			if err := r.ApproveBluetoothSetup(); err != nil || r.BluetoothEnabled() || r.BluetoothSetupPending() {
				t.Fatal("approval changed intent or did not clear the pending gate", err)
			}
		})
	}
}

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
	if err := json.Unmarshal([]byte(r.Snapshot(false)), &snapshot); err != nil {
		t.Fatal(err)
	}
	if !snapshot.Voice.Muted {
		t.Fatal("BLE changed microphone mute")
	}
}
