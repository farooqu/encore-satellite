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

type stateSubscription struct {
	wake  chan struct{}
	reply chan error // At most one command in flight per connection; protected by mu.
}

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

// ListControls composes the Bluetooth switch without terminating discovery.
func (b *Proxy) ListControls(c *esphome.Conn) error {
	if b.save == nil {
		return nil
	}
	return c.Send(&api.ListEntitiesSwitchResponse{ObjectId: "bluetooth_proxy", Key: b.switchKey, Name: "Bluetooth proxy", Icon: "mdi:bluetooth", EntityCategory: esphome.CategoryConfig})
}

func (b *Proxy) control(c *esphome.Conn, msg proto.Message) (bool, error) {
	switch m := msg.(type) {
	case *api.ListEntitiesRequest:
		if err := b.ListControls(c); err != nil {
			return true, err
		}
		return true, c.Send(&api.ListEntitiesDoneResponse{})
	case *api.SubscribeStatesRequest:
		if b.save == nil {
			return true, nil
		}
		b.mu.Lock()
		if b.states[c] == nil {
			b.states[c] = &stateSubscription{wake: make(chan struct{}, 1)}
			go b.publishStates(c, b.states[c])
		}
		b.queueState(c)
		b.mu.Unlock()
		return true, nil
	case *api.SwitchCommandRequest:
		if b.save == nil || m.Key != b.switchKey {
			return false, nil
		}
		return true, b.setEnabled(m.State, c)
	}
	return false, nil
}

// SetEnabled applies the same persisted setting from a local native control.
func (b *Proxy) SetEnabled(enabled bool) error { return b.setEnabled(enabled, nil) }

// queueState is called with mu held. Each subscriber has one writer and at most
// one pending update; observers may coalesce, but commands still get a reply.
func (b *Proxy) queueState(c *esphome.Conn) {
	select {
	case b.states[c].wake <- struct{}{}:
	default:
	}
}

func (b *Proxy) publishStates(c *esphome.Conn, subscription *stateSubscription) {
	for range subscription.wake {
		b.mu.Lock()
		enabled := b.enabled
		reply := subscription.reply
		subscription.reply = nil
		b.mu.Unlock()
		err := c.Send(&api.SwitchStateResponse{Key: b.switchKey, State: enabled})
		if reply != nil {
			reply <- err
		}
		if err != nil {
			b.closeStates(c)
			_ = c.Close()
			return
		}
	}
}

func (b *Proxy) closeStates(c *esphome.Conn) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if subscription := b.states[c]; subscription != nil {
		delete(b.states, c)
		close(subscription.wake)
		if subscription.reply != nil {
			subscription.reply <- errors.New("bluetooth: subscriber closed")
			subscription.reply = nil
		}
	}
}

func (b *Proxy) setEnabled(enabled bool, command *esphome.Conn) error {
	b.controlMu.Lock()
	// A save failure leaves both the live value and peer state unchanged.
	// Persistence is host I/O, not a radio operation or a voice lock.
	if b.save == nil {
		b.controlMu.Unlock()
		return errors.New("bluetooth: persistence callback required")
	}
	if err := b.save(enabled); err != nil {
		b.controlMu.Unlock()
		return err
	}
	b.mu.Lock()
	if b.enabled != enabled {
		b.enabled = enabled
		b.changed()
	}
	var reply chan error
	if subscription := b.states[command]; subscription != nil {
		reply = make(chan error, 1)
		subscription.reply = reply
	}
	for peer := range b.states {
		b.queueState(peer)
	}
	b.mu.Unlock()
	// A control-only client must not claim the radio lease or replace its
	// slot subscriber. Publish to state observers independently of GATT.
	if b.central != nil {
		if !enabled {
			b.links.stop()
		}
		b.links.refresh()
	}
	b.controlMu.Unlock()
	if reply != nil {
		// The connection handler waits, not native controls or lifecycle pause.
		// Sharing the writer prevents an older observer update overtaking this.
		return <-reply
	}
	if command != nil {
		// A one-shot command reply must not hold the persistence lock either.
		return command.Send(&api.SwitchStateResponse{Key: b.switchKey, State: enabled})
	}
	return nil
}
