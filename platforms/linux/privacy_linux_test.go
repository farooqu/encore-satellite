package linux_test

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/platforms/linux"
)

func TestPublicPrivacyFailsClosed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "privacy_state")
	for _, value := range []string{"0\n", "1\n", "", "-1", "unknown", "00"} {
		if err := os.WriteFile(path, []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
		gate := linux.NewPrivacy(func() (bool, error) { return linux.ReadPrivacy(path) })
		called := false
		muted, err := gate.Check(func() { called = true })
		if value == "0\n" {
			if muted || !called || err != nil {
				t.Fatal("unmuted capture denied")
			}
		} else if !muted || called {
			t.Fatalf("privacy %q leaked", value)
		}
	}
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	muted, err := linux.ReadPrivacy(path)
	if !muted || err == nil {
		t.Fatal("missing driver was not fail-closed")
	}
	gate := linux.NewPrivacy(func() (bool, error) { return false, errors.New("driver failed") })
	muted, err = gate.Check(func() { t.Fatal("delivery after failed read") })
	if !muted || err == nil {
		t.Fatal("ignored state error")
	}
}

func TestPublicPrivacyHardwareEventInhibitsWithoutToggling(t *testing.T) {
	reads := 0
	gate := linux.NewPrivacy(func() (bool, error) { reads++; return false, nil })
	gate.Inhibit(time.Hour)
	muted, err := gate.Check(func() { t.Fatal("captured during driver's delayed transition") })
	if err != nil || !muted || reads != 1 {
		t.Fatal("did not observe authoritative driver")
	}
}
