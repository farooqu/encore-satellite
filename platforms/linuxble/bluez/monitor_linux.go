//go:build linux

package bluez

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"

	"github.com/farooqu/encore-satellite/core/ble"
)

const (
	monitorMgrIfc = "org.bluez.AdvertisementMonitorManager1"
	monitorIfc    = "org.bluez.AdvertisementMonitor1"
	monitorRoot   = dbus.ObjectPath("/org/encore/scan")
	monitorPath   = monitorRoot + "/monitor"
)

type advertisementMonitor struct {
	owner     dbus.Sender
	activated chan struct{}
	cancel    context.CancelCauseFunc
	props     *prop.Properties
}

func (m *advertisementMonitor) GetManagedObjects() (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, *dbus.Error) {
	props, err := m.props.GetAll(monitorIfc)
	if err != nil {
		return nil, err
	}
	return map[dbus.ObjectPath]map[string]map[string]dbus.Variant{monitorPath: {monitorIfc: props}}, nil
}

func (m *advertisementMonitor) Activate(sender dbus.Sender) *dbus.Error {
	if sender == m.owner {
		select {
		case m.activated <- struct{}{}:
		default:
		}
	}
	return nil
}

func (m *advertisementMonitor) Release(sender dbus.Sender) *dbus.Error {
	if sender == m.owner {
		m.cancel(errors.New("bluez: passive advertisement monitor released"))
	}
	return nil
}

func (*advertisementMonitor) DeviceFound(dbus.ObjectPath) *dbus.Error { return nil }
func (*advertisementMonitor) DeviceLost(dbus.ObjectPath) *dbus.Error  { return nil }

func (s *Scanner) scanPassive(parent context.Context, capture captureAdvertisements, ready func() error, found func(ble.Advertisement)) error {
	// Scope registration to one private bus owner. Even a failed or timed-out
	// unregister cannot retain the monitor beyond this connection's lifetime.
	conn, err := SystemBus()
	if err != nil {
		return err
	}
	defer conn.Close()
	ctx, cancel := context.WithCancelCause(parent)
	defer cancel(nil)
	stop := context.AfterFunc(conn.Context(), func() { cancel(errors.New("bluez: passive monitor bus closed")) })
	defer stop()
	var owner string
	if err := conn.BusObject().CallWithContext(ctx, "org.freedesktop.DBus.GetNameOwner", 0, service).Store(&owner); err != nil {
		return err
	}
	monitor := &advertisementMonitor{owner: dbus.Sender(owner), activated: make(chan struct{}, 1), cancel: cancel}
	// This software monitor requests passive scanning. The UUID pattern only
	// limits BlueZ's DeviceFound bookkeeping; reports come from raw HCI and
	// are not filtered to the pattern. Use the same UUID as the on-device trial.
	monitor.props, err = prop.Export(conn, monitorPath, prop.Map{monitorIfc: {
		"Type": ro("or_patterns"),
		"Patterns": ro([]struct {
			Offset, Type byte
			Value        []byte
		}{{0, 0x07, []byte{0xed, 0xc1, 0x6c, 0x74, 0xf8, 0x23, 0x51, 0x9a, 0x9e, 0x4b, 0x2d, 0xb7, 0xf6, 0x63, 0xe0, 0x8d}}}),
	}})
	if err != nil {
		return err
	}
	if err := conn.Export(monitor, monitorRoot, objMgrIfc); err != nil {
		return err
	}
	if err := conn.Export(monitor, monitorPath, monitorIfc); err != nil {
		return err
	}

	// Active discovery takes precedence over background scanning. Never call
	// StopDiscovery on another client or report that its active scan is ours.
	signals := make(chan *dbus.Signal, 8)
	conn.Signal(signals)
	if err := conn.AddMatchSignalContext(ctx, dbus.WithMatchSender(owner), dbus.WithMatchObjectPath(s.adapter),
		dbus.WithMatchInterface(propsIfc), dbus.WithMatchMember("PropertiesChanged"), dbus.WithMatchArg(0, adapterIfc)); err != nil {
		return err
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case signal, ok := <-signals:
				if !ok {
					return
				}
				if signal.Sender != owner || signal.Path != s.adapter || signal.Name != propsIfc+".PropertiesChanged" {
					continue
				}
				var ifc string
				var changed map[string]dbus.Variant
				var invalidated []string
				if dbus.Store(signal.Body, &ifc, &changed, &invalidated) != nil || ifc != adapterIfc {
					continue
				}
				if changed["Discovering"].Value() == true || changed["Powered"].Value() == false ||
					slices.Contains(invalidated, "Discovering") || slices.Contains(invalidated, "Powered") {
					cancel(errors.New("bluez: adapter no longer available for passive scanning"))
					return
				}
			}
		}
	}()
	adapter := object(conn, s.adapter)
	registered := false
	err = capture(ctx, s.index, func() error {
		var state map[string]dbus.Variant
		if err := adapter.CallWithContext(ctx, propsIfc+".GetAll", 0, adapterIfc).Store(&state); err != nil {
			return err
		}
		if state["Powered"].Value() != true {
			return errors.New("bluez: adapter is not powered for passive scanning")
		}
		if state["Discovering"].Value() != false {
			return errors.New("bluez: active discovery prevents passive scanning")
		}
		if err := adapter.CallWithContext(ctx, monitorMgrIfc+".RegisterMonitor", 0, monitorRoot).Err; err != nil {
			return fmt.Errorf("bluez: register passive monitor: %w", err)
		}
		registered = true
		activation, done := context.WithTimeout(ctx, 5*time.Second)
		defer done()
		select {
		case <-activation.Done():
			return fmt.Errorf("bluez: activate passive monitor: %w", context.Cause(activation))
		case <-monitor.activated:
			if err := context.Cause(ctx); err != nil {
				return err
			}
			return ready()
		}
	}, found)
	if cause := context.Cause(ctx); cause != nil {
		err = cause
	}
	if registered {
		if stopErr := adapter.Call(monitorMgrIfc+".UnregisterMonitor", 0, monitorRoot).Err; stopErr != nil {
			err = errors.Join(err, fmt.Errorf("bluez: unregister passive monitor: %w", stopErr))
		}
	}
	return err
}
