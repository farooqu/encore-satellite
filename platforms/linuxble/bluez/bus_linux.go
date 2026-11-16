//go:build linux

package bluez

import (
	"context"
	"os"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
)

const (
	service    = "org.bluez"
	adapterIfc = "org.bluez.Adapter1"
	deviceIfc  = "org.bluez.Device1"
	propsIfc   = "org.freedesktop.DBus.Properties"
	objMgrIfc  = "org.freedesktop.DBus.ObjectManager"
	agentIfc   = "org.bluez.Agent1"
	agentPath  = dbus.ObjectPath("/org/encore/agent")
)

func SystemBus() (*dbus.Conn, error) {
	if addr := os.Getenv("DBUS_SYSTEM_BUS_ADDRESS"); addr != "" {
		return dbus.Connect(addr)
	}
	return dbus.ConnectSystemBus()
}

// object applies a hard upper bound to the imported BlueZ code's synchronous
// D-Bus calls, including property access. Explicit operation contexts can end
// them sooner. A missing bluetoothd never stalls a worker indefinitely.
type boundedObject struct{ dbus.BusObject }

func object(c *dbus.Conn, p dbus.ObjectPath) boundedObject {
	return boundedObject{c.Object(service, p)}
}
func (o boundedObject) Call(method string, flags dbus.Flags, args ...any) *dbus.Call {
	return o.CallWithContext(context.Background(), method, flags, args...)
}
func (o boundedObject) CallWithContext(ctx context.Context, method string, flags dbus.Flags, args ...any) *dbus.Call {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return o.BusObject.CallWithContext(ctx, method, flags, args...)
}
func (o boundedObject) GetProperty(name string) (dbus.Variant, error) {
	var v dbus.Variant
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			err := o.Call(propsIfc+".Get", 0, name[:i], name[i+1:]).Store(&v)
			return v, err
		}
	}
	return v, dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", nil)
}
func (o boundedObject) SetProperty(name string, value any) error {
	for i := len(name) - 1; i >= 0; i-- {
		if name[i] == '.' {
			return o.Call(propsIfc+".Set", 0, name[:i], name[i+1:], value).Err
		}
	}
	return dbus.NewError("org.freedesktop.DBus.Error.InvalidArgs", nil)
}

var pairing = struct {
	sync.Mutex
	paths map[dbus.ObjectPath]int
}{paths: map[dbus.ObjectPath]int{}}

func proxyPairingSet(p dbus.ObjectPath, on bool) {
	pairing.Lock()
	defer pairing.Unlock()
	if on {
		pairing.paths[p]++
	} else {
		pairing.paths[p]--
		if pairing.paths[p] <= 0 {
			delete(pairing.paths, p)
		}
	}
}

type agent struct{}

func (agent) Release() *dbus.Error { return nil }
func (agent) Cancel() *dbus.Error  { return nil }
func (agent) RequestAuthorization(p dbus.ObjectPath) *dbus.Error {
	pairing.Lock()
	defer pairing.Unlock()
	if pairing.paths[p] > 0 {
		return nil
	}
	return dbus.NewError("org.bluez.Error.Rejected", nil)
}
func (agent) RequestConfirmation(dbus.ObjectPath, uint32) *dbus.Error {
	return dbus.NewError("org.bluez.Error.Rejected", nil)
}
func (agent) RequestPinCode(dbus.ObjectPath) (string, *dbus.Error) {
	return "", dbus.NewError("org.bluez.Error.Rejected", nil)
}
func (agent) RequestPasskey(dbus.ObjectPath) (uint32, *dbus.Error) {
	return 0, dbus.NewError("org.bluez.Error.Rejected", nil)
}
func (agent) DisplayPinCode(dbus.ObjectPath, string) *dbus.Error {
	return dbus.NewError("org.bluez.Error.Rejected", nil)
}
func (agent) DisplayPasskey(dbus.ObjectPath, uint32, uint16) *dbus.Error {
	return dbus.NewError("org.bluez.Error.Rejected", nil)
}
func (a agent) AuthorizeService(p dbus.ObjectPath, _ string) *dbus.Error {
	return a.RequestAuthorization(p)
}
func registerAgent(c *dbus.Conn) error {
	if err := c.Export(agent{}, agentPath, agentIfc); err != nil {
		return err
	}
	// Agent belongs only to this connection's Pair calls, never a default
	// agent opening global pairing/discoverability to nearby strangers.
	return object(c, "/org/bluez").Call("org.bluez.AgentManager1.RegisterAgent", 0, agentPath, "NoInputNoOutput").Err
}
