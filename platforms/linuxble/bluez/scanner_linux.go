//go:build linux

package bluez

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/godbus/dbus/v5"

	"github.com/farooqu/encore-satellite/core/ble"
	"github.com/farooqu/encore-satellite/platforms/linuxble"
)

// Scanner uses BlueZ to own discovery and a receive-only HCI socket to retain
// exact AD bytes (not a lossy reconstruction of Device1's cached properties).
// BlueZ discovery is active scanning; do NOT advertise StateAndMode for this
// scanner. It never writes raw scan commands behind bluetoothd's back.
type Scanner struct {
	mu      sync.Mutex
	conn    *dbus.Conn
	adapter dbus.ObjectPath
	index   uint16
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
	return &Scanner{conn: c, adapter: adapter, index: uint16(i)}, nil
}

func (s *Scanner) Scan(ctx context.Context, active bool, ready func() error, found func(ble.Advertisement)) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !active {
		return errors.New("bluez: discovery supports active scanning only")
	}
	o := object(s.conn, s.adapter)
	filter := map[string]dbus.Variant{"Transport": dbus.MakeVariant("le"), "DuplicateData": dbus.MakeVariant(true)}
	if err := o.CallWithContext(ctx, adapterIfc+".SetDiscoveryFilter", 0, filter).Err; err != nil {
		return err
	}
	started := false
	defer func() {
		if started {
			if stopErr := o.Call(adapterIfc+".StopDiscovery", 0).Err; stopErr != nil {
				err = fmt.Errorf("bluez: stop discovery: %w", stopErr)
			}
		}
	}()
	return linuxble.Monitor(ctx, s.index, func() error {
		if err := o.CallWithContext(ctx, adapterIfc+".StartDiscovery", 0).Err; err != nil {
			// An interrupted call may have reached bluetoothd. Closing this
			// private bus connection releases its discovery session too.
			_ = o.Call(adapterIfc+".StopDiscovery", 0).Err
			return err
		}
		started = true
		return ready()
	}, found)
}

func (s *Scanner) Close() error { return s.conn.Close() }
