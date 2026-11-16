// Package assist maps the ESPHome native API to shared session contracts.
// This baseline is API-audio-only: no UDP, media player, pushed models, timers,
// wake verification or multi-channel claims are advertised.
package assist

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"sync"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"

	"github.com/farooqu/encore-satellite/backends/internal/outbox"
	"github.com/farooqu/encore-satellite/core/session"
)

const Features = esphome.FeatureVoiceAssistant | esphome.FeatureSpeaker | esphome.FeatureAPIAudio

// aioesphomeapi 46.30.0 uses bit 2, not the value 1 in the generated
// schema's VoiceAssistantSubscribeFlag enum. Verified against pinned HA dev.
const subscribeAPIAudio uint32 = 1 << 2

type Server struct {
	Engine *session.Engine
	// Trace is optional diagnostic output; it must not block the protocol reader.
	Trace       func(string, map[string]string)
	mu          sync.Mutex
	connections map[*esphome.Conn]*connection
}

type connection struct {
	*outbox.Queue
	generation uint64
}

func (*connection) Backend() session.Backend { return session.Assist }

func (s *Server) Serve(ctx context.Context, listener net.Listener, info esphome.Info, key esphome.PSK) error {
	if key.IsZero() {
		return errors.New("assist: a unique nonzero Noise PSK is required")
	}
	info.VoiceFeatures = Features
	info.ESPHomeVersion = "2026.10.1" // Pinned generated schema, not a firmware identity.
	server := esphome.Server{Info: info, PSK: &key, Handler: s, WriteTimeout: 2 * time.Second}
	return server.Serve(ctx, listener)
}

func (s *Server) trace(kind string, data map[string]string) {
	if s.Trace != nil {
		s.Trace(kind, data)
	}
}

func (s *Server) Handle(ctx context.Context, c *esphome.Conn, message proto.Message) error {
	if _, ok := message.(*api.ListEntitiesRequest); ok {
		// The satellite is negotiated through DeviceInfo, not a fake entity.
		return c.Send(&api.ListEntitiesDoneResponse{})
	}
	if m, ok := message.(*api.VoiceAssistantConfigurationRequest); ok {
		s.trace("configuration", nil)
		_ = m // No downloaded/executable models; report an empty selection honestly.
		return c.Send(&api.VoiceAssistantConfigurationResponse{})
	}
	if m, ok := message.(*api.SubscribeVoiceAssistantRequest); ok {
		if !m.Subscribe {
			s.ConnClosed(c)
			return nil
		}
		if m.Flags&subscribeAPIAudio == 0 {
			return errors.New("assist: peer did not negotiate API audio")
		}
		s.mu.Lock()
		if s.connections == nil {
			s.connections = make(map[*esphome.Conn]*connection)
		}
		if s.connections[c] != nil {
			s.mu.Unlock()
			return nil
		}
		link := &connection{Queue: outbox.New(c.Close)}
		generation, err := s.Engine.Attach(link)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		link.generation = generation
		s.connections[c] = link
		s.mu.Unlock()
		s.trace("subscribed", map[string]string{"api_audio": "true"})
		go func() {
			_ = link.Run(func(command session.Command) error {
				switch command.Kind {
				case "start":
					phrase := command.Detection.Word
					if phrase == "manual" {
						phrase = ""
					}
					return c.Send(&api.VoiceAssistantRequest{Start: true, WakeWordPhrase: phrase, AudioSettings: &api.VoiceAssistantAudioSettings{VolumeMultiplier: 1}})
				case "audio":
					b := make([]byte, len(command.Samples)*2)
					for i, sample := range command.Samples {
						binary.LittleEndian.PutUint16(b[i*2:], uint16(sample))
					}
					return c.Send(&api.VoiceAssistantAudio{Data: b})
				case "input_end":
					return c.Send(&api.VoiceAssistantAudio{End: true})
				case "end":
					err := c.Send(&api.VoiceAssistantRequest{Start: false})
					// The baseline protocol has no turn ID. Reconnect after cancel
					// fences late events/audio rather than attributing them to a new turn.
					_ = link.Close()
					return err
				case "drained":
					s.trace("playback_drained", nil)
					return c.Send(&api.VoiceAssistantAnnounceFinished{Success: true})
				}
				return nil
			})
			_ = link.Close()
		}()
		return nil
	}
	s.mu.Lock()
	link := s.connections[c]
	s.mu.Unlock()
	if link == nil {
		return nil
	}
	generation := link.generation
	switch m := message.(type) {
	case *api.VoiceAssistantResponse:
		if m.Error || m.Port != 0 {
			s.Engine.Disconnect(generation, "start_rejected_or_udp")
			return nil
		}
		s.trace("start_accepted", nil)
	case *api.VoiceAssistantAudio:
		if len(m.Data)%2 != 0 || len(m.Data) > 4096 {
			return errors.New("assist: invalid PCM16 output")
		}
		if len(m.Data) > 0 {
			samples := make([]int16, len(m.Data)/2)
			for i := range samples {
				samples[i] = int16(binary.LittleEndian.Uint16(m.Data[i*2:]))
			}
			if err := s.Engine.Output(generation, samples); err != nil && !errors.Is(err, context.Canceled) {
				return err
			}
		}
		if m.End {
			return s.Engine.EndOutput(generation)
		}
	case *api.VoiceAssistantEventResponse:
		data := map[string]string{}
		for _, kv := range m.Data {
			data[kv.Name] = kv.Value
		}
		s.trace(m.EventType.String(), data)
		event := session.Event{}
		switch m.EventType {
		case api.VoiceAssistantEvent_VOICE_ASSISTANT_ERROR:
			event.Kind, event.Reason = "error", data["code"]
		case api.VoiceAssistantEvent_VOICE_ASSISTANT_STT_START:
			event.Kind = "listening"
		case api.VoiceAssistantEvent_VOICE_ASSISTANT_STT_VAD_END:
			event.Kind = "processing"
		case api.VoiceAssistantEvent_VOICE_ASSISTANT_STT_END:
			event.Kind, event.Text = "recognized", data["text"]
		case api.VoiceAssistantEvent_VOICE_ASSISTANT_INTENT_START:
			event.Kind = "processing"
		case api.VoiceAssistantEvent_VOICE_ASSISTANT_TTS_START:
			event.Kind, event.Text = "responding", data["text"]
		case api.VoiceAssistantEvent_VOICE_ASSISTANT_TTS_STREAM_START:
			event.Kind = "output_start"
		case api.VoiceAssistantEvent_VOICE_ASSISTANT_TTS_STREAM_END:
			return s.Engine.EndOutput(generation)
		case api.VoiceAssistantEvent_VOICE_ASSISTANT_RUN_END:
			event.Kind = "run_end"
		}
		return s.Engine.Handle(generation, event)
	}
	return nil
}

func (s *Server) ConnClosed(c *esphome.Conn) {
	s.mu.Lock()
	link := s.connections[c]
	delete(s.connections, c)
	s.mu.Unlock()
	if link != nil {
		_ = link.Close()
		s.Engine.Disconnect(link.generation, "connection_closed")
		s.trace("disconnected", nil)
	}
}
