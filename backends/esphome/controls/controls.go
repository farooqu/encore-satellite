// Package controls binds writable ESPHome entities to device implementations.
package controls

import (
	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"
)

type keyedMessage interface {
	proto.Message
	GetKey() uint32
}

type Entity[C keyedMessage] struct{ discovery keyedMessage }

func (e Entity[C]) Key() uint32 { return e.discovery.GetKey() }

// Bind supplies the implementation for discovery and writes together. A nil
// implementation is equivalent to not adding the entity to the endpoint.
func (e Entity[C]) Bind(apply func(*esphome.Conn, C) error) Binding {
	if apply == nil {
		return Binding{}
	}
	return Binding{e.discovery, func(c *esphome.Conn, msg proto.Message) (bool, error) {
		command, ok := msg.(C)
		if !ok || command.GetKey() != e.Key() {
			return false, nil
		}
		return true, apply(c, command)
	}}
}

type Binding struct {
	discovery keyedMessage
	apply     func(*esphome.Conn, proto.Message) (bool, error)
}

type Bindings []Binding

func (b Bindings) List(send func(proto.Message) error) error {
	for _, entry := range b {
		if entry.discovery != nil {
			if err := send(proto.Clone(entry.discovery)); err != nil {
				return err
			}
		}
	}
	return nil
}

// SendStates publishes only bound controls. Devices supply snapshots so related
// values can be read together without holding device locks across network I/O.
func (b Bindings) SendStates(send func(proto.Message) error, states []proto.Message) error {
	for _, state := range states {
		m, ok := state.(keyedMessage)
		if !ok {
			continue
		}
		for _, entry := range b {
			if entry.discovery != nil && entry.discovery.GetKey() == m.GetKey() {
				if err := send(state); err != nil {
					return err
				}
				break
			}
		}
	}
	return nil
}

func (b Bindings) Handle(c *esphome.Conn, msg proto.Message) (bool, error) {
	for _, entry := range b {
		if entry.apply != nil {
			if handled, err := entry.apply(c, msg); handled {
				return true, err
			}
		}
	}
	return false, nil
}

// IsCommand lets endpoints compose writable controls without rebuilding their
// bindings for unrelated messages (e.g. Bluetooth traffic).
func IsCommand(msg proto.Message) bool {
	switch msg.(type) {
	case *api.SwitchCommandRequest, *api.NumberCommandRequest, *api.SelectCommandRequest,
		*api.TextCommandRequest, *api.ButtonCommandRequest, *api.UpdateCommandRequest, *api.MediaPlayerCommandRequest:
		return true
	}
	return false
}
