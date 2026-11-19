//go:build linux

// Adapted from TECHO5 2b091439765d5f881de4b89255b8cf0637d80cba;
// licenses/Techo5-MIT.txt. Advertising re-registration coverage is Encore's.
package bluez

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/core/improv"
	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

// privateBus starts a dbus-daemon of the test's own, or skips where there is none.
func privateBus(t *testing.T) string {
	t.Helper()
	path, err := exec.LookPath("dbus-daemon")
	if err != nil {
		t.Skip("no dbus-daemon")
	}
	cmd := exec.Command(path, "--session", "--nofork", "--print-address")
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Skip("dbus-daemon will not start:", err)
	}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	addr, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(addr)
}

func connect(t *testing.T, addr string) *dbus.Conn {
	t.Helper()
	c, err := dbus.Connect(addr)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	return c
}

// What bluetoothd does with a registered application, done by hand: read the objects through the
// ObjectManager, write and read a characteristic, and hear a value change as PropertiesChanged.
func TestPeripheralAsBluetoothdSeesIt(t *testing.T) {
	addr := privateBus(t)
	server := connect(t, addr)
	client := connect(t, addr)

	var written [][]byte
	got := make(chan []byte, 1)
	root := dbus.ObjectPath("/org/techo5/test")
	p := &Peripheral{
		conn:    server,
		root:    root,
		advert:  root + "/advert",
		objects: map[dbus.ObjectPath]map[string]map[string]*prop.Prop{},
		chars:   map[string]*charObj{},
	}
	err := p.export("0000aaaa-0000-1000-8000-00805f9b34fb", []CharSpec{
		{UUID: "0000aaa1-0000-1000-8000-00805f9b34fb", Flags: []string{"read", "notify"}, Value: []byte("hello")},
		{UUID: "0000aaa2-0000-1000-8000-00805f9b34fb", Flags: []string{"write"}, Write: func(b []byte) {
			written = append(written, b)
			got <- b
		}},
	}, AdvertSpec{
		ServiceUUIDs: []string{"0000aaaa-0000-1000-8000-00805f9b34fb"},
		ServiceData:  map[string][]byte{"00004677-0000-1000-8000-00805f9b34fb": {2, 7, 0, 0, 0, 0}},
		LocalName:    "Kitchen",
	})
	if err != nil {
		t.Fatal(err)
	}
	name := server.Names()[0]

	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := client.Object(name, root).Call(objMgrIfc+".GetManagedObjects", 0).Store(&objs); err != nil {
		t.Fatal(err)
	}
	svc := objs[root+"/service0"][gattServiceIfc]
	if svc["UUID"].Value() != "0000aaaa-0000-1000-8000-00805f9b34fb" || svc["Primary"].Value() != true {
		t.Fatalf("service %v", svc)
	}
	c0 := objs[root+"/service0/char0"][gattCharIfc]
	if c0["Service"].Value() != root+"/service0" || !bytes.Equal(c0["Value"].Value().([]byte), []byte("hello")) {
		t.Fatalf("char0 %v", c0)
	}

	// Read, whole and from an offset.
	var b []byte
	if err := client.Object(name, root+"/service0/char0").Call(gattCharIfc+".ReadValue", 0, map[string]dbus.Variant{}).Store(&b); err != nil || string(b) != "hello" {
		t.Fatalf("read %q %v", b, err)
	}
	if err := client.Object(name, root+"/service0/char0").Call(gattCharIfc+".ReadValue", 0,
		map[string]dbus.Variant{"offset": dbus.MakeVariant(uint16(2))}).Store(&b); err != nil || string(b) != "llo" {
		t.Fatalf("read at offset %q %v", b, err)
	}

	// Write reaches the callback; a characteristic without one refuses.
	if err := client.Object(name, root+"/service0/char1").Call(gattCharIfc+".WriteValue", 0, []byte{1, 2, 3}, map[string]dbus.Variant{}).Err; err != nil {
		t.Fatal(err)
	}
	select {
	case w := <-got:
		if !bytes.Equal(w, []byte{1, 2, 3}) {
			t.Fatalf("written % x", w)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("write never arrived")
	}
	if err := client.Object(name, root+"/service0/char0").Call(gattCharIfc+".WriteValue", 0, []byte{9}, map[string]dbus.Variant{}).Err; err == nil {
		t.Fatal("a read-only characteristic took a write")
	}

	// Set is the notification: PropertiesChanged on the characteristic, and the new value read back.
	if err := client.AddMatchSignal(dbus.WithMatchInterface(propsIfc), dbus.WithMatchMember("PropertiesChanged")); err != nil {
		t.Fatal(err)
	}
	sigs := make(chan *dbus.Signal, 4)
	client.Signal(sigs)
	// Hold method results before D-Bus encodes them. A subsequent shorter
	// update must not overwrite storage still owned by these replies.
	snapshot, readErr := p.chars["0000aaa1-0000-1000-8000-00805f9b34fb"].ReadValue(nil)
	if readErr != nil {
		t.Fatal(readErr)
	}
	objectSnapshot, objectErr := (manager{p}).GetManagedObjects()
	if objectErr != nil {
		t.Fatal(objectErr)
	}
	p.Set("0000AAA1-0000-1000-8000-00805f9b34fb", []byte("bye"))
	if string(snapshot) != "hello" || string(objectSnapshot[root+"/service0/char0"][gattCharIfc]["Value"].Value().([]byte)) != "hello" {
		t.Fatalf("a previously returned D-Bus value was overwritten: %q", snapshot)
	}
	select {
	case s := <-sigs:
		changed := s.Body[1].(map[string]dbus.Variant)
		if s.Path != root+"/service0/char0" || string(changed["Value"].Value().([]byte)) != "bye" {
			t.Fatalf("signal %v %v", s.Path, changed)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("no PropertiesChanged")
	}
	if err := client.Object(name, root+"/service0/char0").Call(gattCharIfc+".ReadValue", 0, map[string]dbus.Variant{}).Store(&b); err != nil || string(b) != "bye" {
		t.Fatalf("read after set %q %v", b, err)
	}

	// The advertisement, as LEAdvertisingManager1 reads it.
	var all map[string]dbus.Variant
	if err := client.Object(name, p.advert).Call(propsIfc+".GetAll", 0, advertIfc).Store(&all); err != nil {
		t.Fatal(err)
	}
	if all["Type"].Value() != "peripheral" || all["LocalName"].Value() != "Kitchen" {
		t.Fatalf("advert %v", all)
	}
	sd := all["ServiceData"].Value().(map[string]dbus.Variant)
	if !bytes.Equal(sd["00004677-0000-1000-8000-00805f9b34fb"].Value().([]byte), []byte{2, 7, 0, 0, 0, 0}) {
		t.Fatalf("service data %v", sd)
	}
	if len(written) != 1 {
		t.Fatalf("%d writes", len(written))
	}
}

// An adapter with LEAdvertisingManager1 is taken first; one without, as bluetoothd has it on a 3.18
// kernel, still serves the service and is reported as not advertising.
func TestPickAdapter(t *testing.T) {
	ifcs := func(names ...string) map[string]map[string]dbus.Variant {
		m := map[string]map[string]dbus.Variant{}
		for _, n := range names {
			m[n] = nil
		}
		return m
	}
	objs := map[dbus.ObjectPath]map[string]map[string]dbus.Variant{
		"/org/bluez/hci0": ifcs(adapterIfc, gattMgrIfc),
		"/org/bluez/hci2": ifcs(adapterIfc, gattMgrIfc, advertMgrIfc),
		"/org/bluez/hci1": ifcs(adapterIfc, gattMgrIfc, advertMgrIfc),
	}
	if got, adv, err := pickAdapter(objs); err != nil || got != "/org/bluez/hci1" || !adv {
		t.Fatalf("got %q %v %v, want hci1 advertising", got, adv, err)
	}

	old := map[dbus.ObjectPath]map[string]map[string]dbus.Variant{"/org/bluez/hci0": ifcs(adapterIfc, gattMgrIfc)}
	if got, adv, err := pickAdapter(old); err != nil || got != "/org/bluez/hci0" || adv {
		t.Fatalf("no advertising manager: got %q %v %v", got, adv, err)
	}

	if _, _, err := pickAdapter(map[dbus.ObjectPath]map[string]map[string]dbus.Variant{"/org/bluez/hci0": ifcs(adapterIfc)}); err == nil {
		t.Fatal("an adapter without GATT was taken")
	}
}

type testPeripheralAdapter struct {
	conn                *dbus.Conn
	advertised          chan byte
	application         chan dbus.Sender
	advertUnregistered  chan struct{}
	rejectUpdatedAdvert bool
}

func (a *testPeripheralAdapter) RegisterApplication(_ dbus.ObjectPath, _ map[string]dbus.Variant, sender dbus.Sender) *dbus.Error {
	if a.application != nil {
		a.application <- sender
	}
	return nil
}
func (*testPeripheralAdapter) UnregisterApplication(dbus.ObjectPath) *dbus.Error { return nil }
func (a *testPeripheralAdapter) UnregisterAdvertisement(dbus.ObjectPath) *dbus.Error {
	if a.advertUnregistered != nil {
		a.advertUnregistered <- struct{}{}
	}
	return nil
}
func (a *testPeripheralAdapter) RegisterAdvertisement(path dbus.ObjectPath, _ map[string]dbus.Variant, sender dbus.Sender) *dbus.Error {
	var v dbus.Variant
	if err := a.conn.Object(string(sender), path).Call(propsIfc+".Get", 0, advertIfc, "ServiceData").Store(&v); err != nil {
		return dbus.NewError("org.bluez.Error.Failed", nil)
	}
	data := v.Value().(map[string]dbus.Variant)["00004677-0000-1000-8000-00805f9b34fb"].Value().([]byte)
	a.advertised <- data[0]
	if a.rejectUpdatedAdvert && data[0] != byte(improv.AuthorizationRequired) {
		return dbus.NewError("org.bluez.Error.Failed", []any{"Failed to register advertisement"})
	}
	return nil
}

func TestProvisionWithoutProxyOverDBus(t *testing.T) {
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
	// Reproduce the Dot's failure when bluetoothd is asked to register a new
	// advertisement after Action, while the central is using GATT.
	a := &testPeripheralAdapter{
		conn: server, advertised: make(chan byte, 8), application: make(chan dbus.Sender, 1),
		advertUnregistered: make(chan struct{}, 8), rejectUpdatedAdvert: true,
	}
	for _, ifc := range []string{gattMgrIfc, advertMgrIfc} {
		if err := server.Export(a, adapter, ifc); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := prop.Export(server, adapter, prop.Map{adapterIfc: {
		"Powered": {Value: false, Writable: true},
		"Alias":   {Value: "BlueZ", Writable: true},
	}}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	auth := make(chan struct{}, 1)
	joined := make(chan improv.WiFi, 1)
	done := make(chan error, 1)
	// No proxy, Noise connection, scanner or GATT central is started. First
	// boot's locally authorized peripheral role must remain independent.
	go func() {
		done <- Provision(ctx, "Encore Satellite Echo Dot", improv.Options{Name: "kitchen-dot", Authorize: auth, Join: func(_ context.Context, wifi improv.WiFi) (string, error) {
			joined <- wifi
			return "http://192.0.2.23/", nil
		}})
	}()
	var sender dbus.Sender
	select {
	case sender = <-a.application:
	case <-ctx.Done():
		t.Fatal("Improv application was not registered")
	}
	var objs map[dbus.ObjectPath]map[string]map[string]dbus.Variant
	if err := server.Object(string(sender), "/org/encore/improv").Call(objMgrIfc+".GetManagedObjects", 0).Store(&objs); err != nil {
		t.Fatal(err)
	}
	alias, err := server.Object(service, adapter).GetProperty(adapterIfc + ".Alias")
	if err != nil || alias.Value() != "Encore Satellite Echo Dot" {
		t.Fatalf("adapter alias %v: %v", alias, err)
	}
	advertPath := dbus.ObjectPath("/org/encore/improv/advert")
	name, err := server.Object(string(sender), advertPath).GetProperty(advertIfc + ".LocalName")
	if err != nil || name.Value() != "Encore Satellite Echo Dot" {
		t.Fatalf("advertised name %v: %v", name, err)
	}
	signals := make(chan *dbus.Signal, 8)
	server.Signal(signals)
	defer server.RemoveSignal(signals)
	if err := server.AddMatchSignal(dbus.WithMatchObjectPath(advertPath), dbus.WithMatchInterface(propsIfc), dbus.WithMatchMember("PropertiesChanged")); err != nil {
		t.Fatal(err)
	}
	readAdvertState := func(want byte) {
		t.Helper()
		select {
		case sig := <-signals:
			iface, changed := sig.Body[0].(string), sig.Body[1].(map[string]dbus.Variant)
			v, _ := changed["ServiceData"].Value().(map[string]dbus.Variant)
			data, _ := v[improv.ServiceDataUUID].Value().([]byte)
			if iface != advertIfc || !bytes.Equal(data, []byte{want, 2, 0, 0, 0, 0}) {
				t.Fatalf("advertisement change %v, want Improv state %d", sig.Body, want)
			}
		case err := <-done:
			t.Fatalf("session ended while changing advertisement state to %d: %v", want, err)
		case <-ctx.Done():
			t.Fatalf("no advertisement change to state %d", want)
		}
	}
	chars := map[string]dbus.ObjectPath{}
	for path, ifcs := range objs {
		if props, ok := ifcs[gattCharIfc]; ok {
			chars[props["UUID"].Value().(string)] = path
		}
	}
	readUntil := func(uuid string, want []byte) {
		t.Helper()
		for {
			var b []byte
			err := server.Object(string(sender), chars[uuid]).CallWithContext(ctx, gattCharIfc+".ReadValue", 0, map[string]dbus.Variant{}).Store(&b)
			if err != nil {
				t.Fatal(err)
			}
			if bytes.Equal(b, want) {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatalf("%s: got %x want %x", uuid, b, want)
			case <-time.After(time.Millisecond):
			}
		}
	}
	write := func(b []byte) {
		t.Helper()
		if err := server.Object(string(sender), chars[improv.RPCCommandUUID]).CallWithContext(ctx, gattCharIfc+".WriteValue", 0, b, map[string]dbus.Variant{}).Err; err != nil {
			t.Fatal(err)
		}
	}
	readUntil(improv.CapabilitiesUUID, []byte{2}) // Device info only, not identify/scan.
	readUntil(improv.StateUUID, []byte{1})
	write([]byte{3, 0, 3}) // Device info keeps the per-unit name, not the public BLE label.
	readUntil(improv.RPCResultUUID, improv.Result(improv.CmdDeviceInfo, "Encore Satellite", "", "", "kitchen-dot"))
	// Independently encoded Wi-Fi request: command, payload length, strings, checksum.
	frame := []byte{1, 8, 3, 'l', 'a', 'b', 3, 'p', 's', 'k', 0x8c}
	write(frame)
	readUntil(improv.ErrorUUID, []byte{4})
	select {
	case <-joined:
		t.Fatal("Wi-Fi join ran without physical authorization")
	default:
	}
	auth <- struct{}{}
	readAdvertState(2)
	readUntil(improv.StateUUID, []byte{2})
	write(frame[:4])
	write(frame[4:])
	readAdvertState(3)
	readAdvertState(4)
	readUntil(improv.StateUUID, []byte{4})
	select {
	case wifi := <-joined:
		if wifi.SSID != "lab" || wifi.Passphrase != "psk" {
			t.Fatal("provisioned credentials changed")
		}
	case <-ctx.Done():
		t.Fatal("authorized fragmented request never reached Join")
	}
	cancel() // Do not wait through the ten-second result window in this transport test.
	if err := <-done; !errors.Is(err, context.Canceled) {
		t.Fatalf("provisioning shutdown: %v", err)
	}
	if len(a.advertised) != 1 || <-a.advertised != 1 || len(a.advertUnregistered) != 1 {
		t.Fatal("advertisement must be registered once at startup and removed once at shutdown")
	}
}
