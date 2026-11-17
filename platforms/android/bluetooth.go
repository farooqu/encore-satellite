package android

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"sync"

	"github.com/farooqu/encore-satellite/core/ble"
)

// Bluetooth is an in-process native scanner, not an unrestricted command/JS
// bridge. Generation tokens fence callbacks crossing JNI during cancellation.
type Bluetooth interface {
	StartScan(generation int64) error
	StopScan(generation int64) error
}

// BluetoothScanner preserves Android's raw legacy ScanRecord bytes. It never
// fabricates address types or GATT handles. Older APIs report explicit unknown
// address types and must only be offered to a non-connectable proxy.
type BluetoothScanner struct {
	host       Bluetooth
	mu         sync.Mutex
	generation int64
	found      func(ble.Advertisement)
	failed     chan error
}

func NewBluetoothScanner(host Bluetooth) *BluetoothScanner { return &BluetoothScanner{host: host} }

func (s *BluetoothScanner) Scan(ctx context.Context, _ bool, ready func() error, found func(ble.Advertisement)) error {
	s.mu.Lock()
	if s.failed != nil {
		s.mu.Unlock()
		return errors.New("Android scanner already leased")
	}
	s.generation++
	generation := s.generation
	failed := make(chan error, 1)
	s.failed, s.found = failed, found
	s.mu.Unlock()
	defer func() {
		s.mu.Lock()
		s.failed, s.found = nil, nil
		s.mu.Unlock()
	}()
	defer s.host.StopScan(generation)
	if err := s.host.StartScan(generation); err != nil {
		return err
	}
	if err := ready(); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case err := <-failed:
		return err
	}
}

// Report is bounded/nonblocking at the protocol consumer. Holding mu fences the
// final callback against Scan's return; the caller must not retain native data.
func (s *BluetoothScanner) Report(generation int64, address string, addressType, rssi int, data []byte) {
	if (addressType != int(ble.AddressTypeUnknown) && (addressType < 0 || addressType > 1)) || rssi < -127 || rssi > 20 || len(data) == 0 || len(data) > 62 {
		return
	}
	a, err := strconv.ParseUint(strings.ReplaceAll(address, ":", ""), 16, 48)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil && generation == s.generation {
		s.found(ble.Advertisement{Address: a, AddressType: uint8(addressType), RSSI: int8(rssi), Data: append([]byte(nil), data...)})
	}
}

func (s *BluetoothScanner) Failed(generation int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failed != nil && generation == s.generation {
		select {
		case s.failed <- errors.New("Android BLE scan unavailable"):
		default:
		}
	}
}
