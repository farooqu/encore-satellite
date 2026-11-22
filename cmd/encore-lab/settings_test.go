package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFailedMuteSaveSurvivesBluetoothSaveAndReload(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	s, err := loadSettings(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.setMuted(true); err == nil || !s.snapshot().Muted {
		t.Fatal("failed save cleared effective mute", err, s.snapshot())
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := s.update(func(c *configuration) {
		c.BluetoothProxy = true
	}); err != nil {
		t.Fatal(err)
	}
	reloaded, err := loadSettings(path)
	if err != nil || !s.snapshot().Muted || reloaded.snapshot() != (configuration{Muted: true, BluetoothProxy: true}) {
		t.Fatal("unrelated setting lost effective/persisted mute", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	if err := s.setMuted(false); err == nil || !s.snapshot().Muted {
		t.Fatal("unmuted despite failed save", err)
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := s.setMuted(false); err != nil || s.snapshot().Muted {
		t.Fatal("explicit persisted unmute failed", err)
	}
}

func TestLegacyVoiceSelectionDoesNotLoseHostSettings(t *testing.T) {
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"voice_mode":"assist","muted":true,"bluetooth_proxy":true}`), 0600); err != nil {
		t.Fatal(err)
	}
	s, err := loadSettings(path)
	if err != nil || s.snapshot() != (configuration{Muted: true, BluetoothProxy: true}) {
		t.Fatal("old settings did not preserve privacy and BLE", err)
	}
}
