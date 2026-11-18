//go:build linux

package bluez

import (
	"context"
	"errors"
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
