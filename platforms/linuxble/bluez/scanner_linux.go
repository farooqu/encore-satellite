//go:build linux

package bluez

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"

	"github.com/farooqu/encore-satellite/core/ble"
	"github.com/farooqu/encore-satellite/platforms/linuxble"
)

// Scanner uses BlueZ for active discovery and, where supported, passive
// advertisement monitoring. A receive-only HCI socket preserves exact AD
// bytes in either mode without competing with bluetoothd for radio ownership.
type Scanner struct {
	mu         sync.Mutex
	conn       *dbus.Conn
	adapter    dbus.ObjectPath
	index      uint16
	modeSwitch bool
}

func OpenScanner(ctx context.Context) (*Scanner, error) {
	c, err := SystemBus()
	if err != nil {
		return nil, err
	}
	var objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := object(c, "/").CallWithContext(ctx, objMgrIfc+".GetManagedObjects", 0).Store(&objects); err != nil {
		c.Close()
		return nil, err
	}
	var adapter dbus.ObjectPath
	for p, ifcs := range objects {
		if _, ok := ifcs[adapterIfc]; ok && (adapter == "" || p < adapter) {
			adapter = p
		}
	}
	if adapter == "" {
		c.Close()
		return nil, errors.New("bluez: no adapter")
	}
	i, err := strconv.ParseUint(strings.TrimPrefix(string(adapter), "/org/bluez/hci"), 10, 16)
	if err != nil {
		c.Close()
		return nil, fmt.Errorf("bluez: invalid adapter path %s", adapter)
	}
	if err := object(c, adapter).CallWithContext(ctx, propsIfc+".Set", 0, adapterIfc, "Powered", dbus.MakeVariant(true)).Err; err != nil {
		c.Close()
		return nil, err
	}
	monitor := objects[adapter][monitorMgrIfc]
	types, _ := monitor["SupportedMonitorTypes"].Value().([]string)
	features, known := monitor["SupportedFeatures"].Value().([]string)
	// An offloaded monitor may filter the controller's reports to its pattern.
	// Only the software path has been qualified for unfiltered raw reports.
	modeSwitch := slices.Contains(types, "or_patterns") && known && !slices.Contains(features, "controller-patterns")
	return &Scanner{conn: c, adapter: adapter, index: uint16(i), modeSwitch: modeSwitch}, nil
}

func (s *Scanner) SupportsScanModeSwitch() bool { return s.modeSwitch }

func (s *Scanner) Scan(ctx context.Context, active bool, ready func() error, found func(ble.Advertisement)) error {
	return s.scan(ctx, active, linuxble.Monitor, ready, found)
}

type captureAdvertisements func(context.Context, uint16, func() error, func(ble.Advertisement)) error

func (s *Scanner) scan(ctx context.Context, active bool, capture captureAdvertisements, ready func() error, found func(ble.Advertisement)) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !active {
		if !s.modeSwitch {
			return errors.New("bluez: passive scanning is not supported on this adapter")
		}
		return s.scanPassive(ctx, capture, ready, found)
	}
	o := object(s.conn, s.adapter)
	filter := map[string]dbus.Variant{"Transport": dbus.MakeVariant("le"), "DuplicateData": dbus.MakeVariant(true)}
	if err := o.CallWithContext(ctx, adapterIfc+".SetDiscoveryFilter", 0, filter).Err; err != nil {
		// BlueZ rejects all discovery filters with management API < 1.8.
		// Fall back only for that capability error; raw capture and actual
		// discovery must still succeed before reporting readiness.
		var de dbus.Error
		if !errors.As(err, &de) || de.Name != "org.bluez.Error.NotSupported" {
			return fmt.Errorf("bluez: discovery filter: %w", err)
		}
	}
	started := false
	defer func() {
		if started {
			if stopErr := o.Call(adapterIfc+".StopDiscovery", 0).Err; stopErr != nil {
				err = fmt.Errorf("bluez: stop discovery: %w", stopErr)
			}
		}
	}()
	return capture(ctx, s.index, func() error {
		if err := o.CallWithContext(ctx, adapterIfc+".StartDiscovery", 0).Err; err != nil {
			// An interrupted call may have reached bluetoothd. Closing this
			// private bus connection releases its discovery session too.
			_ = o.Call(adapterIfc+".StopDiscovery", 0).Err
			return fmt.Errorf("bluez: start discovery: %w", err)
		}
		started = true
		return ready()
	}, found)
}

func (s *Scanner) Close() error { return s.conn.Close() }
