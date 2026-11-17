package mobile

import (
	"errors"

	"github.com/farooqu/encore-satellite/backends/bluetooth"
	"github.com/farooqu/encore-satellite/core/ble"
	androidhost "github.com/farooqu/encore-satellite/platforms/android"
	esphome "github.com/ygelfand/go-esphome-device"
)

// Bluetooth is the gomobile-compatible native scanner boundary.
type Bluetooth interface {
	StartScan(generation int64) error
	StopScan(generation int64) error
}

// ConfigureBluetooth runs once, before opening listeners. API/hardware support
// is distinct from temporary grants/radio state, which PauseBluetooth gates.
func (r *Runtime) ConfigureBluetooth(host Bluetooth, sdk int, hasBLE bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil || r.bluetoothConfigured {
		return errors.New("Bluetooth must be configured once before start")
	}
	r.bluetoothConfigured = true
	var scanner ble.Scanner
	if host != nil && sdk >= 26 && hasBLE {
		r.radio = androidhost.NewBluetoothScanner(host)
		scanner = r.radio
	}
	r.proxy = r.newBluetooth(scanner)
	return nil
}

func (r *Runtime) newBluetooth(scanner ble.Scanner) *bluetooth.Proxy {
	b := bluetooth.New(scanner, bluetooth.Options{Enabled: r.config.BluetoothProxy, SaveEnabled: func(enabled bool) error {
		r.mu.Lock()
		defer r.mu.Unlock()
		next := r.config
		next.BluetoothProxy = enabled
		if err := r.save(next); err != nil {
			return err
		}
		r.config = next
		return nil
	}})
	b.SetPaused(true) // No activity/foreground/permission assertion at open.
	return b
}

func (r *Runtime) BluetoothEnabled() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.config.BluetoothProxy
}

func (r *Runtime) SetBluetoothEnabled(enabled bool) error {
	r.mu.Lock()
	proxy := r.proxy
	r.mu.Unlock()
	return proxy.SetEnabled(enabled)
}

func (r *Runtime) PauseBluetooth(paused bool) {
	r.mu.Lock()
	proxy := r.proxy
	r.mu.Unlock()
	proxy.SetPaused(paused)
}

func (r *Runtime) BluetoothAdvertisement(generation int64, address string, addressType, rssi int, data []byte) {
	if r.radio != nil {
		r.radio.Report(generation, address, addressType, rssi, data)
	}
}

func (r *Runtime) BluetoothScanFailed(generation int64) {
	if r.radio != nil {
		r.radio.Failed(generation)
	}
}

func (r *Runtime) ConnClosed(c *esphome.Conn) { r.proxy.ConnClosed(c) }
