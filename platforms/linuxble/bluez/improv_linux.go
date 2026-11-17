//go:build linux

package bluez

import (
	"context"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/farooqu/encore-satellite/core/improv"
	"github.com/farooqu/encore-satellite/platforms/linuxble"
)

// Provision offers Improv while the proxy is stopped, including with its saved
// setting OFF. The host calls this only for explicit first-boot setup or an
// authorized local recovery action, never just because an online device loses
// Wi-Fi. It must stop/wait for proxy workers before entry and may resume voice
// afterwards. A successful registration is not a hardware/provisioning proof.
func Provision(ctx context.Context, options improv.Options) (err error) {
	if options.Join == nil || options.Authorize == nil {
		return errors.New("improv: Join and physical authorization are required")
	}
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	writes := make(chan []byte, 8)
	chars := []CharSpec{
		{UUID: improv.StateUUID, Flags: []string{"read", "notify"}, Value: []byte{byte(improv.AuthorizationRequired)}},
		{UUID: improv.ErrorUUID, Flags: []string{"read", "notify"}, Value: []byte{0}},
		{UUID: improv.RPCCommandUUID, Flags: []string{"write"}, Write: func(b []byte) {
			if len(b) > 258 {
				cancel()
				return
			}
			select {
			case writes <- b:
			default:
				cancel()
			}
		}},
		{UUID: improv.RPCResultUUID, Flags: []string{"read", "notify"}},
		{UUID: improv.CapabilitiesUUID, Flags: []string{"read"}, Value: []byte{byte(improv.CanDeviceInfo)}},
	}
	p, err := OpenPeripheral("/org/encore/improv", improv.ServiceUUID, chars, AdvertSpec{
		ServiceUUIDs: []string{improv.ServiceUUID}, LocalName: options.Name,
		ServiceData: map[string][]byte{improv.ServiceDataUUID: improv.ServiceData(improv.AuthorizationRequired, improv.CanDeviceInfo)},
	})
	if err != nil {
		return err
	}
	defer func() { err = errors.Join(err, p.Close()) }()
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
			updateErr = p.SetServiceData(map[string][]byte{improv.ServiceDataUUID: improv.ServiceData(state, improv.CanDeviceInfo)})
			if updateErr != nil {
				cancel()
			}
		} else {
			select {
			case updates <- improv.Advertisement(state, improv.CanDeviceInfo):
			case <-ctx.Done():
			}
		}
	}
	if p.Advertising() {
		return errors.Join(improv.Run(ctx, options, writes, update), updateErr)
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
		advertDone <- linuxble.OfferKernel(ctx, uint16(i), improv.Advertisement(improv.AuthorizationRequired, improv.CanDeviceInfo), improv.ScanResponse(options.Name), updates, func() error { close(started); return nil })
	}()
	select {
	case err := <-advertDone:
		return err
	case <-ctx.Done():
		<-advertDone
		return ctx.Err()
	case <-started:
	}
	sessionDone := make(chan error, 1)
	go func() { sessionDone <- improv.Run(ctx, options, writes, update) }()
	select {
	case err := <-sessionDone:
		cancel()
		advertErr := <-advertDone
		if errors.Is(advertErr, context.Canceled) {
			advertErr = nil // Expected shutdown after a successful session.
		}
		return errors.Join(err, advertErr)
	case err := <-advertDone:
		cancel()
		return errors.Join(err, <-sessionDone)
	}
}
