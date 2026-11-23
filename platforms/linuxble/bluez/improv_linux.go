//go:build linux

package bluez

import (
	"context"
	"errors"
	"log"
	"strconv"
	"strings"
	"time"

	"github.com/farooqu/encore-satellite/core/improv"
	"github.com/farooqu/encore-satellite/platforms/linuxble"
	"github.com/godbus/dbus/v5"
)

type ProvisionOptions struct {
	improv.Options
	// DisconnectPeer releases the successful Wi-Fi writer after result delivery
	// and teardown, if still connected. Other peers are left alone.
	DisconnectPeer bool
}

// Provision offers Improv while the proxy is stopped, including with its saved
// setting OFF. The host calls this only for explicit first-boot setup or an
// authorized local recovery action, never just because an online device loses
// Wi-Fi. It must stop/wait for proxy workers before entry and may resume voice
// afterwards. A successful registration is not a hardware/provisioning proof.
// localName is the public Bluetooth name, separate from the per-unit name in
// the Improv device-info response.
func Provision(parent context.Context, localName string, setup ProvisionOptions) (err error) {
	options := setup.Options
	if options.Join == nil || options.Authorize == nil {
		return errors.New("improv: Join and physical authorization are required")
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	capabilities := options.Capabilities()
	writes := make(chan improv.Write, 8)
	chars := []CharSpec{
		{UUID: improv.StateUUID, Flags: []string{"read", "notify"}, Value: []byte{byte(improv.AuthorizationRequired)}},
		{UUID: improv.ErrorUUID, Flags: []string{"read", "notify"}, Value: []byte{0}},
		{UUID: improv.RPCCommandUUID, Flags: []string{"write"}, Write: func(b []byte, peer dbus.ObjectPath) {
			if len(b) > 258 {
				cancel()
				return
			}
			select {
			case writes <- improv.Write{Peer: string(peer), Data: b}:
			default:
				cancel()
			}
		}},
		{UUID: improv.RPCResultUUID, Flags: []string{"read", "notify"}},
		{UUID: improv.CapabilitiesUUID, Flags: []string{"read"}, Value: []byte{byte(capabilities)}},
	}
	p, err := OpenPeripheral("/org/encore/improv", improv.ServiceUUID, chars, AdvertSpec{
		ServiceUUIDs: []string{improv.ServiceUUID}, LocalName: localName,
		ServiceData: map[string][]byte{improv.ServiceDataUUID: improv.ServiceData(improv.AuthorizationRequired, capabilities)},
	})
	if err != nil {
		return err
	}
	var peer string
	defer func() {
		// Stop accepting new connections before dropping the enrollment link.
		// Unregistering GATT alone does not terminate the controller's ACL.
		err = errors.Join(err, p.close(func() {
			if setup.DisconnectPeer && err == nil && peer != "" && parent.Err() == nil {
				p.disconnectImprovPeer(parent, dbus.ObjectPath(peer))
			}
		}))
	}()
	health := options.Health
	options.Health = func() error {
		// A live process is not enough: bluetoothd/the adapter may have died
		// while the system bus and this GATT object's connection remain alive.
		probe, done := context.WithTimeout(ctx, 3*time.Second)
		defer done()
		var powered bool
		if err := object(p.conn, p.adapter).CallWithContext(probe, propsIfc+".Get", 0, adapterIfc, "Powered").Store(&powered); err != nil {
			return err
		}
		if !powered {
			return errors.New("bluez: provisioning adapter powered off")
		}
		if health != nil {
			return health()
		}
		return nil
	}
	updates := make(chan []byte, 8)
	state := improv.AuthorizationRequired
	var updateErr error
	update := func(uuid string, value []byte) {
		p.Set(uuid, value)
		if uuid != improv.StateUUID || len(value) != 1 || state == improv.State(value[0]) {
			return
		}
		state = improv.State(value[0])
		if p.Advertising() {
			updateErr = p.SetServiceData(map[string][]byte{improv.ServiceDataUUID: improv.ServiceData(state, capabilities)})
			if updateErr != nil {
				cancel()
			}
		} else {
			select {
			case updates <- improv.Advertisement(state, capabilities):
			case <-ctx.Done():
			}
		}
	}
	if p.Advertising() {
		peer, err = improv.Run(ctx, options, writes, update)
		return errors.Join(err, updateErr)
	}
	// Linux 3.18 lacks Add Advertising, but can still host GATT. Only this
	// narrowly scoped legacy advertising path uses raw HCI beside BlueZ.
	i, err := strconv.ParseUint(strings.TrimPrefix(string(p.adapter), "/org/bluez/hci"), 10, 16)
	if err != nil {
		return err
	}
	started := make(chan struct{})
	advertDone := make(chan error, 1)
	go func() {
		advertDone <- linuxble.OfferKernel(ctx, uint16(i), improv.Advertisement(improv.AuthorizationRequired, capabilities), improv.ScanResponse(localName), updates, func() error { close(started); return nil })
	}()
	select {
	case err := <-advertDone:
		return err
	case <-ctx.Done():
		<-advertDone
		return ctx.Err()
	case <-started:
	}
	type sessionResult struct {
		peer string
		err  error
	}
	sessionDone := make(chan sessionResult, 1)
	go func() {
		peer, err := improv.Run(ctx, options, writes, update)
		sessionDone <- sessionResult{peer, err}
	}()
	select {
	case result := <-sessionDone:
		peer = result.peer
		cancel()
		advertErr := <-advertDone
		if errors.Is(advertErr, context.Canceled) {
			advertErr = nil // Expected shutdown after a successful session.
		}
		return errors.Join(result.err, advertErr)
	case err := <-advertDone:
		cancel()
		return errors.Join(err, (<-sessionDone).err)
	}
}

func (p *Peripheral) disconnectImprovPeer(ctx context.Context, peer dbus.ObjectPath) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	device := boundedObject{p.conn.Object(string(p.owner), peer)}
	var connected bool
	err := device.CallWithContext(ctx, propsIfc+".Get", 0, deviceIfc, "Connected").Store(&connected)
	if err == nil && connected {
		err = device.CallWithContext(ctx, deviceIfc+".Disconnect", 0).Err
	}
	var reply dbus.Error
	if err != nil && !(errors.As(err, &reply) && (reply.Name == "org.bluez.Error.NotConnected" || reply.Name == "org.freedesktop.DBus.Error.UnknownObject")) {
		// Do not log the peer identity or fail a committed Wi-Fi enrollment.
		// The host still checks its controller and independently validates BLE.
		log.Print("bluez: could not disconnect the Improv peer after enrollment")
	}
}
