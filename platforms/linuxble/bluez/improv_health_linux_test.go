//go:build linux

package bluez

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/core/improv"
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

func TestProvisionHealthFollowsRegistrationAndStopsOnAdapterLoss(t *testing.T) {
	addr := privateBus(t)
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", addr)
	server := connect(t, addr)
	if _, err := server.RequestName(service, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	adapter := dbus.ObjectPath("/org/bluez/hci0")
	if err := server.Export(testObjects{map[dbus.ObjectPath]map[string]map[string]dbus.Variant{adapter: {adapterIfc: {}, gattMgrIfc: {}, advertMgrIfc: {}}}}, "/", objMgrIfc); err != nil {
		t.Fatal(err)
	}
	a := &testPeripheralAdapter{conn: server, advertised: make(chan byte, 8), application: make(chan dbus.Sender, 1)}
	for _, ifc := range []string{gattMgrIfc, advertMgrIfc} {
		if err := server.Export(a, adapter, ifc); err != nil {
			t.Fatal(err)
		}
	}
	properties, err := prop.Export(server, adapter, prop.Map{adapterIfc: {
		"Powered": {Value: false, Writable: true},
		"Alias":   {Value: "BlueZ", Writable: true},
	}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	checks := 0
	err = Provision(ctx, "Setup", improv.Options{
		Authorize: make(chan struct{}),
		Join: func(context.Context, improv.WiFi) (string, error) {
			t.Error("unexpected join")
			return "", nil
		},
		Health: func() error {
			checks++
			select {
			case <-a.application:
			default:
				t.Error("health before GATT registration")
			}
			select {
			case <-a.advertised:
			default:
				t.Error("health before advertising registration")
			}
			properties.SetMust(adapterIfc, "Powered", false)
			return nil
		},
	})
	if err == nil || errors.Is(err, context.DeadlineExceeded) || checks != 1 {
		t.Fatalf("adapter loss: health checks=%d, error=%v", checks, err)
	}
}
