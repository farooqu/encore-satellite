package biscuit

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/backends/esphome/sensors"
	"golang.org/x/sys/unix"
)

func TestBiscuitLightRequiresProbedDriverAndPreservesLuxUnits(t *testing.T) {
	dir := t.TempDir()
	write := func(name, value string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); err != nil {
			t.Fatal(err)
		}
	}
	assertAbsent := func() {
		t.Helper()
		m, err := discoverLight(dir)
		if err != nil || m.Supported() || (sensors.Sources{Illuminance: m}).IlluminanceState() != nil {
			t.Fatalf("advertised absent hardware: %v", err)
		}
	}
	assertAbsent()
	write("name", "tsl2540\n")
	write("als_lux", "244\n")
	assertAbsent() // DT-instantiated name plus a generic attribute is not enough.
	if err := os.Symlink("../../drivers/tsl258x", filepath.Join(dir, "driver")); err != nil {
		t.Fatal(err)
	}
	assertAbsent()
	if err := os.Remove(filepath.Join(dir, "driver")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../../drivers/tsl2540", filepath.Join(dir, "driver")); err != nil {
		t.Fatal(err)
	}
	m, err := discoverLight(dir)
	if err != nil || !m.Supported() {
		t.Fatalf("probed hardware: %v", err)
	}
	if state := (sensors.Sources{Illuminance: m}).IlluminanceState(); state == nil || !state.MissingState {
		t.Fatal("missing initial sample was not unknown")
	}
	source := biscuitLight{dir}
	write("als_power_state", "1\n")
	write("als_cpl", "100000\n")
	read := func() (float64, error) {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		r, err := source.Read(ctx)
		return r.Lux, err
	}
	if lux, err := read(); err != nil || lux != 244 {
		t.Fatalf("scaled driver lux again: %v %v", lux, err)
	}
	for _, bad := range []string{"-1", "65536", "nan", "raw 244", ""} {
		write("als_lux", bad)
		if _, err := read(); err == nil {
			t.Fatalf("accepted %q", bad)
		}
	}
	write("als_lux", "0\n")
	if lux, err := read(); err != nil || lux != 0 {
		t.Fatalf("darkness rejected: %v %v", lux, err)
	}
	for _, attr := range []string{"als_power_state", "als_cpl"} {
		write(attr, "0\n")
		if _, err := read(); err == nil {
			t.Fatalf("read uninitialized driver (%s)", attr)
		}
		write(attr, "1\n")
	}
	if err := os.Remove(filepath.Join(dir, "als_lux")); err != nil {
		t.Fatal(err)
	}
	if _, err := read(); err == nil {
		t.Fatal("missing attribute returned a value")
	}
	if !m.Supported() {
		t.Fatal("read error removed hardware capability")
	}
	// A blocked attribute must not trap voice/music behind the sensor worker.
	if err := unix.Mkfifo(filepath.Join(dir, "als_lux"), 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := source.Read(ctx); err == nil || time.Since(started) > time.Second {
		t.Fatalf("blocked sensor did not honor deadline: %v", err)
	}
}
