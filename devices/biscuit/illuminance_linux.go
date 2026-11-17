package biscuit

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/farooqu/encore-satellite/core/illuminance"
)

// Only this exact chip/driver/attribute has been audited. Do not glob IIO or
// alternative I2C addresses: a DT node/name alone does not prove populated hardware.
const biscuitLightDevice = "/sys/bus/i2c/devices/0-0039"

type biscuitLight struct{ directory string }

func discoverLight(directory string) (*illuminance.Monitor, error) {
	name, err := os.ReadFile(filepath.Join(directory, "name"))
	if os.IsNotExist(err) {
		return illuminance.New(nil), nil
	}
	if err != nil {
		return illuminance.New(nil), fmt.Errorf("light discovery: %w", err)
	}
	if strings.TrimSpace(string(name)) != "tsl2540" {
		return illuminance.New(nil), nil
	}
	driver, err := os.Readlink(filepath.Join(directory, "driver"))
	if os.IsNotExist(err) {
		return illuminance.New(nil), nil
	}
	if err != nil {
		return illuminance.New(nil), fmt.Errorf("light binding: %w", err)
	}
	if filepath.Base(driver) != "tsl2540" {
		return illuminance.New(nil), nil
	}
	// This driver's successful binding follows its e4/01/61 hardware-ID check.
	// A failed measurement from here onward is availability, not absent hardware.
	return illuminance.New(biscuitLight{directory}), nil
}

func (s biscuitLight) Read(ctx context.Context) (illuminance.Reading, error) {
	// Isolate sysfs from voice/music workers. No enable/gain/calibration writes.
	// The legacy callback masks internal I2C errors; see ILLUMINANCE.md. This
	// timestamp means a completed sysfs observation, not a chip conversion counter.
	read := func(attribute string) (string, error) {
		cmd := exec.CommandContext(ctx, "/bin/cat", filepath.Join(s.directory, attribute))
		cmd.WaitDelay = 100 * time.Millisecond
		pipe, err := cmd.StdoutPipe()
		if err != nil {
			return "", err
		}
		if err = cmd.Start(); err != nil {
			return "", err
		}
		b, readErr := io.ReadAll(io.LimitReader(pipe, 65))
		err = cmd.Wait()
		if readErr != nil {
			return "", readErr
		}
		if err != nil {
			return "", err
		}
		if len(b) > 64 {
			return "", fmt.Errorf("oversized light reading")
		}
		return strings.TrimSpace(string(b)), nil
	}
	// Both are audited, software-only show callbacks. Guard the legacy lux
	// callback's division by zero before initialization; never enable the device.
	power, err := read("als_power_state")
	if err != nil {
		return illuminance.Reading{}, err
	}
	if power != "1" {
		return illuminance.Reading{}, fmt.Errorf("light driver disabled")
	}
	cpl, err := read("als_cpl")
	if err != nil {
		return illuminance.Reading{}, err
	}
	divisor, err := strconv.ParseUint(cpl, 10, 32)
	if err != nil || divisor == 0 {
		return illuminance.Reading{}, fmt.Errorf("light conversion not initialized")
	}
	value, err := read("als_lux")
	if err != nil {
		return illuminance.Reading{}, err
	}
	lux, err := strconv.ParseUint(value, 10, 16)
	if err != nil {
		return illuminance.Reading{}, err
	}
	return illuminance.Reading{Lux: float64(lux), At: time.Now()}, nil
}
