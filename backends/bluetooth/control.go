package bluetooth

import (
	"context"
	"errors"
	"net"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"
)

// Serve is the voice-independent encrypted control endpoint, for example when
// Incubator owns voice. Assist can compose the same Handler/Run/Features API.
// Never expose this handler through a plaintext or unprovisioned server.
func (b *Proxy) Serve(ctx context.Context, listener net.Listener, info esphome.Info, key esphome.PSK) error {
	if key.IsZero() {
		return errors.New("bluetooth: a nonzero Noise key is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); b.Run(ctx) }()
	defer func() { cancel(); <-done }()
	info.BluetoothFeatures = b.Features()
	return (&esphome.Server{Info: info, PSK: &key, Handler: b, WriteTimeout: 2 * time.Second}).Serve(ctx, listener)
}

func (b *Proxy) enabledNow() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.enabled && !b.paused
}

func (b *Proxy) control(c *esphome.Conn, msg proto.Message) (bool, error) {
	switch m := msg.(type) {
	case *api.ListEntitiesRequest:
		if err := c.Send(&api.ListEntitiesSwitchResponse{ObjectId: "bluetooth_proxy", Key: b.switchKey, Name: "Bluetooth proxy", Icon: "mdi:bluetooth", EntityCategory: esphome.CategoryConfig}); err != nil {
			return true, err
		}
		return true, c.Send(&api.ListEntitiesDoneResponse{})
	case *api.SubscribeStatesRequest:
		b.controlMu.Lock()
		defer b.controlMu.Unlock()
		b.mu.Lock()
		b.states[c] = struct{}{}
		enabled := b.enabled
		b.mu.Unlock()
		return true, c.Send(&api.SwitchStateResponse{Key: b.switchKey, State: enabled})
	case *api.SwitchCommandRequest:
		if m.Key != b.switchKey {
			return false, nil
		}
		b.controlMu.Lock()
		defer b.controlMu.Unlock()
		// A save failure leaves both the live value and peer state unchanged.
		// Persistence is host I/O, not a radio operation or a voice lock.
		if b.save == nil {
			return true, errors.New("bluetooth: persistence callback required")
		}
		if err := b.save(m.State); err != nil {
			return true, err
		}
		b.mu.Lock()
		if b.enabled != m.State {
			b.enabled = m.State
			b.changed()
		}
		clients := make([]*esphome.Conn, 0, len(b.states)+1)
		for peer := range b.states {
			if peer != c {
				clients = append(clients, peer)
			}
		}
		b.mu.Unlock()
		if !m.State {
			b.links.stop()
		}
		// A control-only client must not claim the radio lease or replace its
		// slot subscriber. Publish to state observers independently of GATT.
		b.links.refresh()
		for _, peer := range clients {
			if err := peer.Send(&api.SwitchStateResponse{Key: b.switchKey, State: m.State}); err != nil {
				_ = peer.Close()
			}
		}
		return true, c.Send(&api.SwitchStateResponse{Key: b.switchKey, State: m.State})
	}
	return false, nil
}
