package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/farooqu/encore-satellite/core/session"
)

func TestFailedMuteSaveSurvivesOtherSettingsAndRuntimeRestart(t *testing.T) {
	for _, change := range []string{"mode", "bluetooth"} {
		t.Run(change, func(t *testing.T) {
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
				if change == "mode" {
					c.VoiceMode = session.Incubator
				} else {
					c.BluetoothProxy = true
				}
			}); err != nil {
				t.Fatal(err)
			}
			reloaded, err := loadSettings(path)
			if err != nil || !s.snapshot().Muted || !reloaded.snapshot().Muted {
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
		})
	}
}
