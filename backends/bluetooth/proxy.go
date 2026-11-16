// Package bluetooth implements an optional encrypted ESPHome Bluetooth proxy,
// independent of the selected voice backend and of first-boot Improv.
package bluetooth

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"

	"github.com/farooqu/encore-satellite/core/ble"
)

// Proxy lends one initialized scanner to one authenticated API client.
// Construct with New; use on only one Server. No audio/session
// locks are held during radio I/O. Control messages coalesce rather than spawn
// workers, and advertisements are expendable when the bounded queue fills.
type Proxy struct {
	scanner    ble.Scanner
	mu         sync.Mutex
	controlMu  sync.Mutex // Serialize persistence and application across clients.
	conn       *esphome.Conn
	active     bool
	paused     bool
	cancel     context.CancelFunc
	wake       chan struct{}
	dropped    atomic.Uint64
	enabled    bool
	save       func(bool) error
	central    func(func(string, uint16, []byte), func(string)) (ble.Central, error)
	pairing    bool
	modeSwitch bool
	links      *connections
	switchKey  uint32
	states     map[*esphome.Conn]struct{}
}

// Options holds host integration, not separate user switches. Enabled is the
// single saved bluetooth_proxy setting (omitted/default false). SaveEnabled
// atomically persists it before any change takes effect. OpenCentral, if set,
// must successfully initialize GATT and its pairing agent before returning.
// Pairing is true only on platforms implementing bonding; ModeSwitch is true
// only when Scan honors both active and passive radio modes.
type Options struct {
	Enabled     bool
	SaveEnabled func(bool) error
	OpenCentral func(func(string, uint16, []byte), func(string)) (ble.Central, error)
	Pairing     bool
	ModeSwitch  bool
}

func New(scanner ble.Scanner, options Options) *Proxy {
	if scanner == nil {
		return nil
	}
	sw := &esphome.Switch{Base: esphome.Base{ObjectID: "bluetooth_proxy"}}
	b := &Proxy{scanner: scanner, wake: make(chan struct{}, 1), enabled: options.Enabled,
		save: options.SaveEnabled, central: options.OpenCentral, pairing: options.Pairing,
		modeSwitch: options.ModeSwitch, switchKey: sw.Key(), states: make(map[*esphome.Conn]struct{})}
	b.links = newConnections(b)
	return b
}

// SetPaused cancels scanning asynchronously (including queued reports). The
// worker waits for controller cleanup before any resume. Hosts can pause for
// voice/RF contention or a privacy policy; this is NOT a microphone mute API.
// Cancel Serve and wait for it before lending the controller to Improv/BlueZ.
func (b *Proxy) SetPaused(paused bool) {
	b.controlMu.Lock()
	defer b.controlMu.Unlock()
	b.mu.Lock()
	if b.paused != paused {
		b.paused = paused
		b.changed()
	}
	b.mu.Unlock()
	if paused {
		b.links.stop()
	}
	b.links.refresh()
}

func (b *Proxy) Dropped() uint64 { return b.dropped.Load() }

// Features describes implemented platform capability, not whether the optional
// proxy is currently enabled. Keeping it stable lets HA discover the one switch
// while OFF, without reconnect races or a second active-connection toggle.
func (b *Proxy) Features() esphome.BluetoothFeature {
	f := esphome.BluetoothPassiveScan | esphome.BluetoothRawAdvertisements
	if b.modeSwitch {
		f |= esphome.BluetoothStateAndMode
	}
	if b.central != nil {
		f |= esphome.BluetoothActiveConnections | esphome.BluetoothRemoteCaching
		if b.pairing {
			f |= esphome.BluetoothPairing
		}
	}
	return f
}

// changed is held with mu. At most one controller worker and one batch writer
// exist, even during floods of mode/subscription requests.
func (b *Proxy) changed() {
	if b.cancel != nil {
		b.cancel()
	}
	select {
	case b.wake <- struct{}{}:
	default:
	}
}

func (b *Proxy) Handle(_ context.Context, c *esphome.Conn, msg proto.Message) error {
	if handled, err := b.control(c, msg); handled {
		return err
	}
	if handles(msg) {
		return b.links.handle(c, msg)
	}
	_, err := b.handle(c, msg)
	return err
}

func (b *Proxy) handle(c *esphome.Conn, msg proto.Message) (bool, error) {
	switch m := msg.(type) {
	case *api.SubscribeBluetoothLEAdvertisementsRequest:
		if m.Flags&1 == 0 {
			return true, errors.New("assist: raw BLE advertisements must be negotiated")
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.conn != nil && b.conn != c {
			return true, errors.New("assist: Bluetooth proxy already subscribed")
		}
		if b.conn == nil {
			b.conn = c
			b.changed()
		}
		return true, nil
	case *api.UnsubscribeBluetoothLEAdvertisementsRequest:
		b.closed(c)
		return true, nil
	case *api.BluetoothScannerSetModeRequest:
		if !b.modeSwitch {
			return true, errors.New("bluetooth: platform has fixed active scanning")
		}
		if m.Mode != api.BluetoothScannerMode_BLUETOOTH_SCANNER_MODE_ACTIVE && m.Mode != api.BluetoothScannerMode_BLUETOOTH_SCANNER_MODE_PASSIVE {
			return true, errors.New("assist: invalid Bluetooth scanner mode")
		}
		b.mu.Lock()
		defer b.mu.Unlock()
		if b.conn != c {
			return true, errors.New("assist: Bluetooth mode requires subscription ownership")
		}
		active := m.Mode == api.BluetoothScannerMode_BLUETOOTH_SCANNER_MODE_ACTIVE
		if b.active != active {
			b.active = active
			b.changed()
		}
		return true, nil
	case *api.SubscribeBluetoothConnectionsFreeRequest:
		return true, c.Send(&api.BluetoothConnectionsFreeResponse{}) // Zero slots.
	}
	return false, nil
}

func (b *Proxy) ConnClosed(c *esphome.Conn) {
	b.mu.Lock()
	delete(b.states, c)
	b.mu.Unlock()
	b.closed(c)
	b.links.closed(c)
}

func (b *Proxy) closed(c *esphome.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.conn == c {
		b.conn = nil
		b.changed()
	}
}

func scannerState(state esphome.ScannerState, active bool) *api.BluetoothScannerStateResponse {
	mode := api.BluetoothScannerMode_BLUETOOTH_SCANNER_MODE_PASSIVE
	if active {
		mode = api.BluetoothScannerMode_BLUETOOTH_SCANNER_MODE_ACTIVE
	}
	return &api.BluetoothScannerStateResponse{State: state, Mode: mode, ConfiguredMode: api.BluetoothScannerMode_BLUETOOTH_SCANNER_MODE_PASSIVE}
}

func (b *Proxy) Run(ctx context.Context) {
	linksDone := make(chan struct{})
	go func() { defer close(linksDone); b.links.run(ctx) }()
	defer func() { b.links.stop(); <-linksDone }()
	for {
		select {
		case <-ctx.Done():
			return
		case <-b.wake:
		}
		b.mu.Lock()
		c, active, paused := b.conn, b.active, b.paused || !b.enabled
		if !b.modeSwitch {
			active = true
		}
		scanCtx, cancel := context.WithCancel(ctx)
		b.cancel = cancel
		b.mu.Unlock()
		if c == nil {
			cancel()
			continue
		}
		if paused {
			if err := c.Send(scannerState(esphome.ScannerStopped, active)); err != nil {
				b.closed(c)
			}
			cancel()
			continue
		}
		if err := c.Send(scannerState(esphome.ScannerStarting, active)); err != nil {
			b.closed(c)
			cancel()
			continue
		}
		ads := make(chan ble.Advertisement, 64)
		written := make(chan struct{})
		go func() {
			defer close(written)
			if err := b.deliver(scanCtx, c, ads); err != nil {
				b.closed(c)
				_ = c.Close()
			}
		}()
		_ = b.scanner.Scan(scanCtx, active, func() error {
			return c.Send(scannerState(esphome.ScannerRunning, active))
		}, func(a ble.Advertisement) {
			if scanCtx.Err() != nil || len(a.Data) > 62 || a.Address > 0xffffffffffff || a.AddressType > 1 {
				return
			}
			a.Data = append([]byte(nil), a.Data...)
			select {
			case ads <- a:
			default:
				b.dropped.Add(1)
			}
		})
		failed := scanCtx.Err() == nil
		cancel()
		<-written // Fence the old batch writer before starting a new scan.
		if failed {
			_ = c.Send(scannerState(esphome.ScannerFailed, active))
		}
	}
}

func (b *Proxy) deliver(ctx context.Context, c *esphome.Conn, ads <-chan ble.Advertisement) error {
	tick := time.NewTicker(250 * time.Millisecond)
	defer tick.Stop()
	var batch []*api.BluetoothLERawAdvertisement
	for {
		select {
		case <-ctx.Done():
			return nil
		case a := <-ads:
			batch = append(batch, &api.BluetoothLERawAdvertisement{Address: a.Address, AddressType: uint32(a.AddressType), Rssi: int32(a.RSSI), Data: a.Data})
			if len(batch) < 16 {
				continue
			}
		case <-tick.C:
		}
		if ctx.Err() != nil {
			return nil
		}
		if len(batch) > 0 {
			if err := c.Send(&api.BluetoothLERawAdvertisementsResponse{Advertisements: batch}); err != nil {
				return err
			}
			batch = nil
		}
	}
}
