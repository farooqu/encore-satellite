//go:build linux

package bluez

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/farooqu/encore-satellite/core/ble"
	"github.com/farooqu/encore-satellite/core/improv"
	"github.com/farooqu/encore-satellite/platforms/linuxble"
)

// Validate probes the REQUIRED Bluetooth base before a host marks a slot good,
// even when its optional proxy setting is false. Call before voice/proxy/Improv
// starts: it briefly starts/stops discovery and registers an unauthorized Improv
// service/advertisement. It never accepts credentials, pairs, or changes bonds.
// A success proves local interfaces/permissions, NOT RF, a phone provisioning
// exchange, remote GATT interoperability, supported link count or audio coexistence.
func Validate(ctx context.Context) error {
	s, err := OpenScanner(ctx)
	if err != nil {
		return fmt.Errorf("BLE scanner: %w", err)
	}
	defer s.Close()
	ready := errors.New("BLE discovery ready")
	err = s.Scan(ctx, true, func() error { return ready }, func(ble.Advertisement) {})
	if err != ready {
		return fmt.Errorf("BLE raw capture/discovery: %w", err)
	}
	c, err := OpenCentral(nil, nil)
	if err != nil {
		return fmt.Errorf("BLE GATT client/pairing agent: %w", err)
	}
	if err := c.Close(); err != nil {
		return err
	}
	p, err := OpenPeripheral("/org/encore/bleprobe", improv.ServiceUUID, []CharSpec{
		{UUID: improv.StateUUID, Flags: []string{"read", "notify"}, Value: []byte{byte(improv.AuthorizationRequired)}},
	}, AdvertSpec{ServiceUUIDs: []string{improv.ServiceUUID}, ServiceData: map[string][]byte{improv.ServiceDataUUID: improv.ServiceData(improv.AuthorizationRequired, 0)}})
	if err != nil {
		return fmt.Errorf("BLE GATT server/advertising: %w", err)
	}
	defer p.Close()
	if !p.Advertising() {
		offer, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		err := linuxble.OfferKernel(offer, s.index, improv.Advertisement(improv.AuthorizationRequired, 0), nil, nil, func() error { return ready })
		if err != ready {
			return fmt.Errorf("BLE legacy advertising: %w", err)
		}
	}
	return p.Close()
}
