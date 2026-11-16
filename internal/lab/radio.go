package lab

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/farooqu/encore-satellite/core/ble"
)

// VirtualRadio emits changing manufacturer data so HA receipt can be checked
// without a physical adapter. It does not implement GATT or claim RF coverage.
type VirtualRadio struct {
	Starts  atomic.Int64
	Stops   atomic.Int64
	reports atomic.Uint64
}

func (r *VirtualRadio) Scan(ctx context.Context, _ bool, ready func() error, found func(ble.Advertisement)) error {
	r.Starts.Add(1)
	defer r.Stops.Add(1)
	if err := ready(); err != nil {
		return err
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			n := r.reports.Add(1)
			found(ble.Advertisement{Address: 0xc0ffee123456, AddressType: 1, RSSI: -73, Data: []byte{2, 1, 6, 5, 0xff, 0x34, 0x12, byte(n), byte(n >> 8)}})
		}
	}
}
