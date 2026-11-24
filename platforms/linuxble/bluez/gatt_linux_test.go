//go:build linux

// Adapted tests from TECHO5 2b091439765d5f881de4b89255b8cf0637d80cba;
// licenses/Techo5-MIT.txt. Private-bus Central lifecycle tests are Encore's.
package bluez

import (
	"bytes"
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

func TestHandleOf(t *testing.T) {
	for path, want := range map[dbus.ObjectPath]uint16{
		"/org/bluez/hci0/dev_C0_FF_EE_12_34_56/service000c":                   0x0c,
		"/org/bluez/hci0/dev_C0_FF_EE_12_34_56/service000c/char000d":          0x0d,
		"/org/bluez/hci0/dev_C0_FF_EE_12_34_56/service000c/char000d/desc00ff": 0xff,
	} {
		if h, ok := handleOf(path); !ok || h != want {
			t.Errorf("handleOf(%s) = %#x %v, want %#x", path, h, ok, want)
		}
	}
	if _, ok := handleOf("/org/bluez/hci0/dev_C0_FF_EE_12_34_56"); ok {
		t.Error("a device path has no handle")
	}
}

func TestTree(t *testing.T) {
	dev := dbus.ObjectPath("/org/bluez/hci0/dev_C0_FF_EE_12_34_56")
	str := func(s string) dbus.Variant { return dbus.MakeVariant(s) }
	objs := map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
		dev:                  {deviceIfc: {"Address": str("C0:FF:EE:12:34:56")}},
		dev + "/service0010": {gattServiceIfc: {"UUID": str("0000180A-0000-1000-8000-00805F9B34FB")}},
		dev + "/service000c": {gattServiceIfc: {"UUID": str("f00dcafe-0000-4000-8000-000000000001")}},
		dev + "/service000c/char000d": {gattCharIfc: {
			"UUID":  str("f00dcafe-0000-4000-8000-000000000002"),
			"Flags": dbus.MakeVariant([]string{"read", "notify"}),
		}},
		dev + "/service000c/char000d/desc000f": {gattDescIfc: {"UUID": str(cccd)}},
		// Another device's attributes stay out.
		"/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF/service0001": {gattServiceIfc: {"UUID": str("1800")}},
	}

	services, attrs := tree(dev, objs)
	if len(services) != 2 || services[0].Handle != 0x0c || services[1].Handle != 0x10 {
		t.Fatalf("services %+v", services)
	}
	if services[1].UUID != "0000180a-0000-1000-8000-00805f9b34fb" {
		t.Errorf("uuid not lower case: %s", services[1].UUID)
	}
	chars := services[0].Characteristics
	if len(chars) != 1 || chars[0].Handle != 0x0e || len(chars[0].Flags) != 2 {
		t.Fatalf("characteristics %+v", chars)
	}
	if len(chars[0].Descriptors) != 1 || chars[0].Descriptors[0].Handle != 0x0f {
		t.Fatalf("descriptors %+v", chars[0].Descriptors)
	}
	if a := attrs[0x0f]; a.ifc != gattDescIfc || a.char != 0x0e || a.uuid != cccd {
		t.Errorf("descriptor attribute %+v", a)
	}
	if len(attrs) != 4 {
		t.Errorf("attributes %d, want 4", len(attrs))
	}
}

func TestLinksGoWithTheirDevice(t *testing.T) {
	for _, s := range []*dbus.Signal{
		{Name: objMgrIfc + ".InterfacesRemoved", Body: []any{dbus.ObjectPath("/org/bluez/hci0/dev_C0_FF_EE_12_34_56"), []string{deviceIfc}}},
		{Name: objMgrIfc + ".InterfacesRemoved", Body: []any{dbus.ObjectPath("/org/bluez/hci0"), []string{adapterIfc}}},
		{Name: "org.freedesktop.DBus.NameOwnerChanged", Body: []any{service, ":1.0", ""}},
		{Name: propsIfc + ".PropertiesChanged", Path: "/org/bluez/hci0/dev_C0_FF_EE_12_34_56",
			Body: []any{deviceIfc, map[string]dbus.Variant{"Connected": dbus.MakeVariant(false)}, []string{}}},
	} {
		var lost []string
		stopped := false
		c := &Central{adapter: "/org/bluez/hci0", lost: func(a string) { lost = append(lost, a) }, links: map[string]*link{
			"C0:FF:EE:12:34:56": {path: "/org/bluez/hci0/dev_C0_FF_EE_12_34_56", notifying: map[uint16]func(){0x0e: func() { stopped = true }}},
			"AA:BB:CC:DD:EE:FF": {path: "/org/bluez/hci0/dev_AA_BB_CC_DD_EE_FF", notifying: map[uint16]func(){}},
		}}
		c.signal(s)
		if len(lost) == 0 || lost[0] != "C0:FF:EE:12:34:56" && lost[len(lost)-1] != "C0:FF:EE:12:34:56" || !stopped {
			t.Errorf("%s: lost %v, notifications stopped %v", s.Name, lost, stopped)
		}
		if _, still := c.links["C0:FF:EE:12:34:56"]; still {
			t.Errorf("%s: link kept", s.Name)
		}
	}
}

type testObjects struct {
	objects map[dbus.ObjectPath]map[string]map[string]dbus.Variant
}

func (o testObjects) GetManagedObjects() (map[dbus.ObjectPath]map[string]map[string]dbus.Variant, *dbus.Error) {
	return o.objects, nil
}

type testAgentManager struct {
	conn   *dbus.Conn
	sender dbus.Sender
	path   dbus.ObjectPath
}

func (m *testAgentManager) RegisterAgent(path dbus.ObjectPath, capability string, sender dbus.Sender) *dbus.Error {
	if capability != "NoInputNoOutput" {
		return dbus.NewError("org.bluez.Error.Rejected", nil)
	}
	m.path, m.sender = path, sender
	return nil
}

type testDevice struct {
	agent          *testAgentManager
	path           dbus.ObjectPath
	props          *prop.Properties
	slowConnect    atomic.Bool
	failDisconnect atomic.Bool
}

func (d *testDevice) Connect() *dbus.Error {
	d.props.SetMust(deviceIfc, "Connected", true)
	if d.slowConnect.Swap(false) {
		time.Sleep(5250 * time.Millisecond)
	}
	return nil
}
func (d *testDevice) Disconnect() *dbus.Error {
	if d.failDisconnect.Load() {
		return dbus.NewError("org.bluez.Error.Failed", []any{"fixture disconnect failure"})
	}
	d.props.SetMust(deviceIfc, "Connected", false)
	return nil
}
func (d *testDevice) Pair() *dbus.Error {
	if paired, err := d.props.Get(deviceIfc, "Paired"); err == nil && paired.Value() == true {
		return dbus.NewError("org.bluez.Error.AlreadyExists", []any{"Already Exists"})
	}
	err := d.agent.conn.Object(string(d.agent.sender), d.agent.path).Call(agentIfc+".RequestAuthorization", 0, d.path).Err
	if err != nil {
		return dbus.NewError("org.bluez.Error.Rejected", nil)
	}
	return nil
}

type testCharacteristic struct{ props *prop.Properties }

func (*testCharacteristic) ReadValue(map[string]dbus.Variant) ([]byte, *dbus.Error) {
	return []byte{0x27, 0xe1, 0x82}, nil
}
func (c *testCharacteristic) WriteValue(b []byte, _ map[string]dbus.Variant) *dbus.Error {
	c.props.SetMust(gattCharIfc, "Value", b)
	return nil
}
func (*testCharacteristic) StartNotify() *dbus.Error { return nil }
func (*testCharacteristic) StopNotify() *dbus.Error  { return nil }

type testDescriptor struct{ props *prop.Properties }

func (d *testDescriptor) ReadValue(map[string]dbus.Variant) ([]byte, *dbus.Error) {
	v, err := d.props.Get(gattDescIfc, "Value")
	if err != nil {
		return nil, err
	}
	return v.Value().([]byte), nil
}
func (d *testDescriptor) WriteValue(b []byte, _ map[string]dbus.Variant) *dbus.Error {
	d.props.SetMust(gattDescIfc, "Value", b)
	return nil
}

type testCentralAdapter struct{ removed chan dbus.ObjectPath }

func (a *testCentralAdapter) RemoveDevice(path dbus.ObjectPath) *dbus.Error {
	a.removed <- path
	return nil
}

func TestCentralGATTAndScopedPairingOverDBus(t *testing.T) {
	addr := privateBus(t)
	t.Setenv("DBUS_SYSTEM_BUS_ADDRESS", addr)
	server := connect(t, addr)
	if _, err := server.RequestName(service, dbus.NameFlagDoNotQueue); err != nil {
		t.Fatal(err)
	}
	dev := dbus.ObjectPath("/org/bluez/hci0/dev_C0_FF_EE_12_34_56")
	char := dev + "/service0010/char0013"
	desc := char + "/desc0019"
	objects := map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
		"/org/bluez/hci0":    {adapterIfc: {}},
		dev:                  {deviceIfc: {"Address": dbus.MakeVariant("C0:FF:EE:12:34:56"), "Connected": dbus.MakeVariant(true), "ServicesResolved": dbus.MakeVariant(true)}},
		dev + "/service0010": {gattServiceIfc: {"UUID": dbus.MakeVariant("0000180f-0000-1000-8000-00805f9b34fb")}},
		char:                 {gattCharIfc: {"UUID": dbus.MakeVariant("00002a19-0000-1000-8000-00805f9b34fb"), "Flags": dbus.MakeVariant([]string{"read", "write", "notify"})}},
		desc:                 {gattDescIfc: {"UUID": dbus.MakeVariant("00002901-0000-1000-8000-00805f9b34fb")}},
	}
	if err := server.Export(testObjects{objects}, "/", objMgrIfc); err != nil {
		t.Fatal(err)
	}
	m := &testAgentManager{conn: server}
	if err := server.Export(m, "/org/bluez", "org.bluez.AgentManager1"); err != nil {
		t.Fatal(err)
	}
	a := &testCentralAdapter{removed: make(chan dbus.ObjectPath, 1)}
	if err := server.Export(a, "/org/bluez/hci0", adapterIfc); err != nil {
		t.Fatal(err)
	}
	dp, err := prop.Export(server, dev, prop.Map{deviceIfc: {"Address": ro("C0:FF:EE:12:34:56"), "Connected": {Value: true, Emit: prop.EmitTrue}, "ServicesResolved": ro(true), "Trusted": {Value: false, Writable: true}, "Paired": {Value: false}, "Icon": {Value: ""}}})
	if err != nil {
		t.Fatal(err)
	}
	device := &testDevice{agent: m, path: dev, props: dp}
	if err := server.Export(device, dev, deviceIfc); err != nil {
		t.Fatal(err)
	}
	cp, err := prop.Export(server, char, prop.Map{gattCharIfc: {"Value": {Value: []byte{}, Emit: prop.EmitTrue}, "MTU": ro(uint16(61))}})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Export(&testCharacteristic{cp}, char, gattCharIfc); err != nil {
		t.Fatal(err)
	}
	descProps, err := prop.Export(server, desc, prop.Map{gattDescIfc: {"Value": {Value: []byte("description")}}})
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Export(&testDescriptor{descProps}, desc, gattDescIfc); err != nil {
		t.Fatal(err)
	}
	values := make(chan []byte, 2)
	c, err := OpenCentral(func(a string, h uint16, b []byte) {
		if a != "C0:FF:EE:12:34:56" || h != 0x14 {
			t.Error("notification address/ATT handle changed")
		}
		values <- append([]byte(nil), b...)
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	// BlueZ can take more than five seconds to return after the link comes up.
	device.slowConnect.Store(true)
	mtu, err := c.Connect(ctx, "C0:FF:EE:12:34:56", true)
	if err != nil || mtu != 61 {
		t.Fatalf("connect mtu=%d err=%v", mtu, err)
	}
	services, err := c.Services("C0:FF:EE:12:34:56")
	if err != nil || len(services) != 1 || services[0].Characteristics[0].Handle != 0x14 {
		t.Fatalf("services=%+v err=%v", services, err)
	}
	b, err := c.Read(ctx, "C0:FF:EE:12:34:56", 0x14)
	if err != nil || !bytes.Equal(b, []byte{0x27, 0xe1, 0x82}) {
		t.Fatalf("read=%x err=%v", b, err)
	}
	b, err = c.Read(ctx, "C0:FF:EE:12:34:56", 0x19)
	if err != nil || string(b) != "description" {
		t.Fatalf("descriptor read=%q err=%v", b, err)
	}
	if err := c.Write(ctx, "C0:FF:EE:12:34:56", 0x19, []byte("changed description"), true); err != nil {
		t.Fatal(err)
	}
	b, err = c.Read(ctx, "C0:FF:EE:12:34:56", 0x19)
	if err != nil || string(b) != "changed description" {
		t.Fatalf("descriptor after write=%q err=%v", b, err)
	}
	if err := c.Notify(ctx, "C0:FF:EE:12:34:56", 0x14, true); err != nil {
		t.Fatal(err)
	}
	if err := c.Write(ctx, "C0:FF:EE:12:34:56", 0x14, []byte{0x83, 0x15}, true); err != nil {
		t.Fatal(err)
	}
	select {
	case b := <-values:
		if !bytes.Equal(b, []byte{0x83, 0x15}) {
			t.Fatalf("notify=%x", b)
		}
	case <-ctx.Done():
		t.Fatal("notification not forwarded")
	}
	if err := c.Pair(ctx, "C0:FF:EE:12:34:56"); err != nil {
		t.Fatal(err)
	}
	// After the requested bond, the private agent must refuse even this peer,
	// and must never silently confirm numeric-comparison authentication.
	for _, call := range []struct {
		method string
		args   []any
	}{{"RequestAuthorization", []any{dev}}, {"RequestConfirmation", []any{dev, uint32(123456)}}, {"RequestPinCode", []any{dev}}, {"RequestPasskey", []any{dev}}} {
		if err := server.Object(string(m.sender), m.path).Call(agentIfc+"."+call.method, 0, call.args...).Err; err == nil {
			t.Fatalf("unrequested %s accepted", call.method)
		}
	}
	dp.SetMust(deviceIfc, "Paired", true)
	dp.SetMust(deviceIfc, "Trusted", false)
	if err := c.Pair(ctx, "C0:FF:EE:12:34:56"); err != nil {
		t.Fatalf("pair with an existing bond: %v", err)
	}
	if trusted, err := dp.Get(deviceIfc, "Trusted"); err != nil || trusted.Value() != true {
		t.Fatalf("existing bond not trusted: %v, %v", trusted, err)
	}
	dp.SetMust(deviceIfc, "Icon", "phone")
	if err := c.Unpair(ctx, "C0:FF:EE:12:34:56"); err == nil || !c.Connected("C0:FF:EE:12:34:56") {
		t.Fatal("unpair did not protect the device's paired phone")
	}
	select {
	case <-a.removed:
		t.Fatal("protected phone was removed")
	default:
	}
	dp.SetMust(deviceIfc, "Icon", "")
	if err := c.Disconnect(ctx, "C0:FF:EE:12:34:56"); err != nil {
		t.Fatal(err)
	}
	if c.Connected("C0:FF:EE:12:34:56") {
		t.Fatal("disconnected link retained")
	}
	if _, err := c.Connect(ctx, "C0:FF:EE:12:34:56", true); err != nil {
		t.Fatal(err)
	}
	// A remote-cache client does not ask for services on its new connection.
	b, err = c.Read(ctx, "C0:FF:EE:12:34:56", 0x14)
	if err != nil || !bytes.Equal(b, []byte{0x27, 0xe1, 0x82}) {
		t.Fatalf("cached-handle read after reconnect=%x err=%v", b, err)
	}
	if err := c.Unpair(ctx, "C0:FF:EE:12:34:56"); err != nil {
		t.Fatal(err)
	}
	select {
	case path := <-a.removed:
		if path != dev || c.Connected("C0:FF:EE:12:34:56") {
			t.Fatalf("unpaired path=%s connected=%v", path, c.Connected("C0:FF:EE:12:34:56"))
		}
	case <-ctx.Done():
		t.Fatal("unpair did not remove the device")
	}
	if _, err := c.Connect(ctx, "C0:FF:EE:12:34:56", true); err != nil {
		t.Fatal(err)
	}
	device.failDisconnect.Store(true)
	if err := c.Close(); err == nil {
		t.Fatal("Close hid the controller's failed disconnect")
	}
	connected, dbusErr := dp.Get(deviceIfc, "Connected")
	if dbusErr != nil || connected.Value() != true {
		t.Fatalf("failed disconnect changed controller state: %v, %v", connected, dbusErr)
	}
}
