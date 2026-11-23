//go:build linux

package bluez

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/core/ble"
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
	"golang.org/x/sys/unix"
)

type testDiscoveryAdapter struct{ filterError *dbus.Error }

func (a testDiscoveryAdapter) SetDiscoveryFilter(map[string]dbus.Variant) *dbus.Error {
	return a.filterError
}
func (testDiscoveryAdapter) StartDiscovery() *dbus.Error { return nil }
func (testDiscoveryAdapter) StopDiscovery() *dbus.Error  { return nil }

func TestScannerRequiresRawCaptureWithLegacyDiscovery(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  *dbus.Error
	}{
		{"filtered", nil},
		{"legacy", dbus.NewError("org.bluez.Error.NotSupported", []any{"Operation is not supported"})},
		{"denied", dbus.NewError("org.bluez.Error.NotAuthorized", []any{"Access denied"})},
		{"unavailable", dbus.NewError("org.freedesktop.DBus.Error.NoReply", []any{"No reply"})},
	} {
		t.Run(tc.name, func(t *testing.T) {
			addr := privateBus(t)
			t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", addr)
			server := connect(t, addr)
			if _, err := server.RequestName(service, dbus.NameFlagDoNotQueue); err != nil {
				t.Fatal(err)
			}
			// No real controller is used: raw capture cannot bind this index.
			adapter := dbus.ObjectPath("/org/bluez/hci65534")
			objects := testObjects{map[dbus.ObjectPath]map[string]map[string]dbus.Variant{adapter: {adapterIfc: {}}}}
			if err := server.Export(objects, "/", objMgrIfc); err != nil {
				t.Fatal(err)
			}
			if err := server.Export(testDiscoveryAdapter{tc.err}, adapter, adapterIfc); err != nil {
				t.Fatal(err)
			}
			if _, err := prop.Export(server, adapter, prop.Map{adapterIfc: {"Powered": {Value: true, Writable: true}}}); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			scanner, err := OpenScanner(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer scanner.Close()
			if scanner.SupportsScanModeSwitch() {
				t.Fatal("adapter without a monitor manager advertised mode switching")
			}
			err = scanner.Scan(ctx, true, func() error {
				t.Error("scanner declared readiness without raw capture")
				return errors.New("unexpected readiness")
			}, func(ble.Advertisement) {})
			if tc.err != nil && tc.err.Name != "org.bluez.Error.NotSupported" {
				var de dbus.Error
				if !errors.As(err, &de) || de.Name != tc.err.Name {
					t.Fatalf("filter error lost: %v", err)
				}
			} else {
				var errno unix.Errno
				if !errors.As(err, &errno) || errno == 0 {
					t.Fatalf("expected raw socket/bind failure, got %v", err)
				}
			}
		})
	}
}

type testMonitorAdapter struct {
	conn  *dbus.Conn
	props *prop.Properties
	calls chan string
}

func (*testMonitorAdapter) SetDiscoveryFilter(map[string]dbus.Variant) *dbus.Error { return nil }
func (a *testMonitorAdapter) StartDiscovery() *dbus.Error {
	a.props.SetMust(adapterIfc, "Discovering", true)
	a.calls <- "start"
	return nil
}
func (a *testMonitorAdapter) StopDiscovery() *dbus.Error {
	a.props.SetMust(adapterIfc, "Discovering", false)
	a.calls <- "stop"
	return nil
}
func (a *testMonitorAdapter) RegisterMonitor(root dbus.ObjectPath, sender dbus.Sender) *dbus.Error {
	call := a.conn.Object(string(sender), root).Call(objMgrIfc+".GetManagedObjects", 0)
	if call.Err != nil {
		return dbus.MakeFailedError(call.Err)
	}
	// Store reconstructs nested variants and can lose their struct signature.
	objects, _ := call.Body[0].(map[dbus.ObjectPath]map[string]map[string]dbus.Variant)
	for path, interfaces := range objects {
		if props := interfaces[monitorIfc]; props["Type"].Value() == "or_patterns" && props["Patterns"].Signature().String() == "a(yyay)" {
			if err := a.conn.Object(string(sender), path).Call(monitorIfc+".Activate", 0).Err; err != nil {
				return dbus.MakeFailedError(err)
			}
			a.calls <- "register"
			return nil
		}
	}
	return dbus.MakeFailedError(fmt.Errorf("bad monitor objects: %v", objects))
}
func (a *testMonitorAdapter) UnregisterMonitor(dbus.ObjectPath) *dbus.Error {
	a.calls <- "unregister"
	return nil
}

func TestScannerSwitchesDiscoveryAndPassiveMonitorWithGATT(t *testing.T) {
	addr := privateBus(t)
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", addr)
	server := connect(t, addr)
	if _, err := server.RequestName(service, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	adapter := dbus.ObjectPath("/org/bluez/hci0")
	const address = "C0:FF:EE:12:34:56"
	device := adapter + "/dev_C0_FF_EE_12_34_56"
	gattService := device + "/service0010"
	characteristic := gattService + "/char0013" // Value handle is 0x14.
	objects := testObjects{map[dbus.ObjectPath]map[string]map[string]dbus.Variant{adapter: {
		adapterIfc: {}, monitorMgrIfc: {
			"SupportedMonitorTypes": dbus.MakeVariant([]string{"or_patterns"}),
			"SupportedFeatures":     dbus.MakeVariant([]string{}),
		},
	}}}
	objects.objects[gattService] = map[string]map[string]dbus.Variant{gattServiceIfc: {"UUID": dbus.MakeVariant("180f")}}
	objects.objects[characteristic] = map[string]map[string]dbus.Variant{gattCharIfc: {"UUID": dbus.MakeVariant("2a19")}}
	if err := server.Export(objects, "/", objMgrIfc); err != nil {
		t.Fatal(err)
	}
	props, err := prop.Export(server, adapter, prop.Map{adapterIfc: {
		"Powered": {Value: true, Writable: true}, "Discovering": {Value: false, Emit: prop.EmitTrue},
	}})
	if err != nil {
		t.Fatal(err)
	}
	fixture := &testMonitorAdapter{conn: server, props: props, calls: make(chan string, 10)}
	for _, ifc := range []string{adapterIfc, monitorMgrIfc} {
		if err := server.Export(fixture, adapter, ifc); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	scanner, err := OpenScanner(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer scanner.Close()
	if !scanner.SupportsScanModeSwitch() {
		t.Fatal("expected software monitor support")
	}
	peer, err := prop.Export(server, device, prop.Map{deviceIfc: {
		"Address": ro(address), "ServicesResolved": ro(true), "Connected": {Value: false, Emit: prop.EmitTrue},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Export(&testDevice{props: peer}, device, deviceIfc); err != nil {
		t.Fatal(err)
	}
	if err := server.Export(&testCharacteristic{}, characteristic, gattCharIfc); err != nil {
		t.Fatal(err)
	}
	if err := server.Export(&testAgentManager{conn: server}, "/org/bluez", "org.bluez.AgentManager1"); err != nil {
		t.Fatal(err)
	}
	central, err := OpenCentral(nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer central.Close()
	if _, err := central.Connect(ctx, address, true); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []struct {
		active bool
		start  string
		stop   string
	}{
		{true, "start", "stop"}, {false, "register", "unregister"}, {true, "start", "stop"},
	} {
		scanCtx, stop := context.WithCancel(ctx)
		capture := func(ctx context.Context, _ uint16, start func() error, found func(ble.Advertisement)) error {
			if err := start(); err != nil {
				return err
			}
			found(ble.Advertisement{Address: 0xc0ffee123456, RSSI: -73})
			<-ctx.Done()
			return ctx.Err()
		}
		var got ble.Advertisement
		err = scanner.scan(scanCtx, phase.active, capture, func() error {
			select {
			case call := <-fixture.calls:
				if call != phase.start {
					t.Errorf("ready after %s, want %s", call, phase.start)
				}
			case <-ctx.Done():
				return ctx.Err()
			}
			value, err := central.Read(ctx, address, 0x14)
			if err != nil || !bytes.Equal(value, []byte{0x27, 0xe1, 0x82}) {
				t.Errorf("GATT read while active=%t: got %x, err=%v", phase.active, value, err)
			}
			return nil
		}, func(ad ble.Advertisement) {
			got = ad
			stop()
		})
		stop()
		if !errors.Is(err, context.Canceled) || got.Address != 0xc0ffee123456 || got.RSSI != -73 {
			t.Fatalf("active=%t: report=%+v, scan error=%v", phase.active, got, err)
		}
		select {
		case call := <-fixture.calls:
			if call != phase.stop {
				t.Fatalf("cleanup called %s, want %s", call, phase.stop)
			}
		default:
			t.Fatalf("missing cleanup %s", phase.stop)
		}
	}
	err = scanner.scan(ctx, false, func(ctx context.Context, _ uint16, start func() error, _ func(ble.Advertisement)) error {
		if err := start(); err != nil {
			return err
		}
		props.SetMust(adapterIfc, "Discovering", true)
		<-ctx.Done()
		return ctx.Err()
	}, func() error { return nil }, func(ble.Advertisement) {})
	if ctx.Err() != nil || err == nil || !strings.Contains(err.Error(), "no longer available") || len(fixture.calls) != 2 {
		t.Fatalf("competing discovery should end passive scanning: %v, calls=%d", err, len(fixture.calls))
	}
	if start, stop := <-fixture.calls, <-fixture.calls; start != "register" || stop != "unregister" {
		t.Fatalf("competing discovery caused %s then %s, want register then unregister", start, stop)
	}
	err = scanner.scan(ctx, false, func(_ context.Context, _ uint16, start func() error, _ func(ble.Advertisement)) error {
		return start()
	}, func() error {
		t.Error("reported passive with another active discovery owner")
		return nil
	}, func(ble.Advertisement) {})
	if err == nil || !strings.Contains(err.Error(), "active discovery") || len(fixture.calls) != 0 {
		t.Fatalf("passive scan should fail without touching active discovery: %v, calls=%d", err, len(fixture.calls))
	}
}
