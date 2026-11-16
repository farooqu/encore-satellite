package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/farooqu/encore-satellite/core/session"
)

type configuration struct {
	VoiceMode      session.Backend `json:"voice_mode"`
	BluetoothProxy bool            `json:"bluetooth_proxy"`
	Muted          bool            `json:"muted"`
}

// settings belongs to the virtual host, not the session engine or device package
// planning profiles. One lock prevents unrelated settings overwriting each other.
type settings struct {
	mu    sync.Mutex
	path  string
	data  configuration
	muted bool // Effective privacy latch, including a mute that failed to persist.
}

func loadSettings(path string) (*settings, error) {
	s := &settings{path: path, data: configuration{VoiceMode: session.Assist}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return s, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, &s.data); err != nil {
		return nil, err
	}
	if s.data.VoiceMode != session.Assist && s.data.VoiceMode != session.Incubator {
		return nil, fmt.Errorf("invalid saved voice mode %q", s.data.VoiceMode)
	}
	s.muted = s.data.Muted
	return s, nil
}

func (s *settings) snapshot() configuration {
	s.mu.Lock()
	defer s.mu.Unlock()
	current := s.data
	current.Muted = s.muted
	return current
}

func (s *settings) setMuted(muted bool) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if muted {
		s.muted = true // Failed persistence must never undo a privacy action.
	}
	err := s.save(func(c *configuration) { c.Muted = muted })
	if err == nil {
		s.muted = muted
	}
	return err
}

func (s *settings) update(change func(*configuration)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.save(change)
}

func (s *settings) save(change func(*configuration)) error {
	next := s.data
	next.Muted = s.muted
	change(&next)
	if next.VoiceMode != session.Assist && next.VoiceMode != session.Incubator {
		return fmt.Errorf("invalid voice mode %q", next.VoiceMode)
	}
	if next == s.data {
		return nil
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	// Write/rename in the same private directory; failure preserves the prior
	// saved record. The effective privacy latch is retained separately.
	file, err := os.CreateTemp(filepath.Dir(s.path), ".settings-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	defer file.Close()
	if _, err := file.Write(b); err != nil {
		return err
	}
	if err := file.Sync(); err != nil {
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	if err := os.Rename(file.Name(), s.path); err != nil {
		return err
	}
	s.data = next
	return nil
}
