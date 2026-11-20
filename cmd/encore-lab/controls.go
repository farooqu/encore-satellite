// Lab-only selector controls. Production packages link one voice backend and
// must not expose these controls; package main cannot be imported by hosts.
package main

import (
	"context"
	"errors"
	"net"
	"time"

	"github.com/farooqu/encore-satellite/backends/esphome/controls"
	"github.com/farooqu/encore-satellite/backends/esphome/sensors"
	"github.com/farooqu/encore-satellite/core/session"
	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"
)

// voiceControls is composed with Assist or served alone when native Incubator
// owns the lab voice session. SaveMode must persist; Restart must not block.
// Neither callback may switch the live session in response to selection alone.
type voiceControls struct {
	RunningMode session.Backend
	Mode        func() session.Backend
	SaveMode    func(session.Backend) error
	Restart     func()
	Bluetooth   interface {
		esphome.Handler
		esphome.ConnCloser
		Features() esphome.BluetoothFeature
		Run(context.Context)
	}
}

var modeNames = map[session.Backend]string{
	session.Assist: "Home Assistant Assist", session.Incubator: "Voice Incubator",
}

var voiceModeControl = controls.VoiceMode(modeNames[session.Assist], modeNames[session.Incubator])

func (s *voiceControls) Features() esphome.BluetoothFeature {
	if s.Bluetooth != nil {
		return s.Bluetooth.Features()
	}
	return 0
}

func (s *voiceControls) Run(ctx context.Context) {
	if s.Bluetooth != nil {
		s.Bluetooth.Run(ctx)
	}
}

func (s *voiceControls) ConnClosed(c *esphome.Conn) {
	if s.Bluetooth != nil {
		s.Bluetooth.ConnClosed(c)
	}
}

func (s *voiceControls) Serve(ctx context.Context, listener net.Listener, info esphome.Info, key esphome.PSK) error {
	if key.IsZero() {
		return errors.New("controls: a nonzero Noise key is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); s.Run(ctx) }()
	defer func() { cancel(); <-done }()
	info.VoiceFeatures, info.BluetoothFeatures = 0, s.Features()
	return (&esphome.Server{Info: info, PSK: &key, Handler: s, WriteTimeout: 2 * time.Second}).Serve(ctx, listener)
}

func (s *voiceControls) writable() controls.Bindings {
	var bindings controls.Bindings
	if s.Mode != nil && s.SaveMode != nil {
		bindings = append(bindings, voiceModeControl.Bind(func(c *esphome.Conn, m *api.SelectCommandRequest) error {
			for mode, name := range modeNames {
				if name == m.State {
					if err := s.SaveMode(mode); err != nil {
						return err
					}
					return c.Send(&api.SelectStateResponse{Key: voiceModeControl.Key(), State: name})
				}
			}
			return errors.New("controls: unknown voice mode")
		}))
	}
	if s.Mode != nil && s.Restart != nil {
		bindings = append(bindings, controls.ApplyVoiceMode.Bind(func(_ *esphome.Conn, _ *api.ButtonCommandRequest) error {
			if s.Mode() != s.RunningMode {
				s.Restart()
			}
			return nil
		}))
	}
	return bindings
}

func (s *voiceControls) Handle(ctx context.Context, c *esphome.Conn, message proto.Message) error {
	if controls.IsCommand(message) {
		if handled, err := s.writable().Handle(c, message); handled {
			return err
		}
	}
	sources := sensors.Sources{RunningVoiceMode: func() string { return modeNames[s.RunningMode] }}
	switch message.(type) {
	case *api.ListEntitiesRequest:
		if err := s.writable().List(c.Send); err != nil {
			return err
		}
		if err := sources.List(c.Send); err != nil {
			return err
		}
		// Bluetooth terminates the combined entity list, exactly once.
		if s.Bluetooth == nil {
			return c.Send(&api.ListEntitiesDoneResponse{})
		}
	case *api.SubscribeStatesRequest:
		if s.Mode != nil && s.SaveMode != nil {
			if err := c.Send(&api.SelectStateResponse{Key: voiceModeControl.Key(), State: modeNames[s.Mode()]}); err != nil {
				return err
			}
		}
		for _, state := range sources.States() {
			if err := c.Send(state); err != nil {
				return err
			}
		}
	}
	if s.Bluetooth != nil {
		return s.Bluetooth.Handle(ctx, c, message)
	}
	return nil
}
