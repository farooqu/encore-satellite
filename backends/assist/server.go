// Package assist maps the ESPHome native API to shared session contracts.
// This baseline is API-audio-only: no UDP, media player, pushed models, timers,
// multi-channel claims are advertised. Wake audio is an explicit dev opt-in.
package assist

import (
	"context"
	"encoding/binary"
	"errors"
	"net"
	"strconv"
	"sync"
	"sync/atomic"
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
	// Controls shares this encrypted endpoint with voice-independent controls.
	// Nil preserves the voice-only entity set. Keep this endpoint running when
	// Incubator is selected; the session engine still rejects Assist voice.
	Controls interface {
		esphome.Handler
		esphome.ConnCloser
		Features() esphome.BluetoothFeature
		Run(context.Context)
	}
	// ExperimentalWakeWords maps actual host model IDs to their spoken phrases.
	// Nil keeps feature bits exactly 7. Configure only hosts that supply real
	// contiguous wake pre-roll; compatibility is pinned in wake_audio.go.
	ExperimentalWakeWords map[string]string
	// Trace is optional diagnostic output; it must not block the protocol reader.
	Trace           func(string, map[string]string)
	mu              sync.Mutex
	connections     map[*esphome.Conn]*connection
	activeWakeWords []string
}

type connection struct {
	*outbox.Queue
	generation     uint64
	wakeAudio      bool
	closeTransport atomic.Bool
	done           chan struct{}
}

func (*connection) Backend() session.Backend { return session.Assist }

func (s *Server) Serve(ctx context.Context, listener net.Listener, info esphome.Info, key esphome.PSK) error {
	if key.IsZero() {
		return errors.New("assist: a unique nonzero Noise PSK is required")
	}
	info.VoiceFeatures = Features
	if len(s.ExperimentalWakeWords) > 0 {
		for id, phrase := range s.ExperimentalWakeWords {
			if id == "" || phrase == "" || len(id) > 128 || len(phrase) > 128 {
				return errors.New("assist: invalid experimental wake model")
			}
		}
		info.VoiceFeatures |= featureWakeAudio
	}
	for id := range s.ExperimentalWakeWords {
		s.activeWakeWords = append(s.activeWakeWords, id)
	}
	info.ESPHomeVersion = "2026.10.1" // Pinned generated schema, not a firmware identity.
	info.BluetoothFeatures = 0        // Never inherit unsupported caller claims.
	if s.Controls != nil {
		ctxWithControls, cancel := context.WithCancel(ctx)
		ctx = ctxWithControls
		done := make(chan struct{})
		go func() { defer close(done); s.Controls.Run(ctx) }()
		defer func() { cancel(); <-done }()
		info.BluetoothFeatures = s.Controls.Features()
	}
	server := esphome.Server{Info: info, PSK: &key, Handler: s, WriteTimeout: 2 * time.Second}
	return server.Serve(ctx, listener)
}

func (s *Server) trace(kind string, data map[string]string) {
	if s.Trace != nil {
		s.Trace(kind, data)
	}
}

func (s *Server) Handle(ctx context.Context, c *esphome.Conn, message proto.Message) error {
	if s.Controls != nil {
		if err := s.Controls.Handle(ctx, c, message); err != nil {
			return err
		}
	}
	if _, ok := message.(*api.ListEntitiesRequest); ok {
		if s.Controls != nil {
			return nil
		}
		// The satellite is negotiated through DeviceInfo, not a fake entity.
		return c.Send(&api.ListEntitiesDoneResponse{})
	}
	if m, ok := message.(*api.VoiceAssistantConfigurationRequest); ok {
		s.trace("configuration", nil)
		_ = m // No downloaded/executable models are accepted.
		return c.Send(s.wakeConfiguration())
	}
	if m, ok := message.(*api.VoiceAssistantSetConfiguration); ok {
		return s.setWakeWords(m.ActiveWakeWords)
	}
	if m, ok := message.(*api.SubscribeVoiceAssistantRequest); ok {
		if !m.Subscribe {
			s.mu.Lock()
			link := s.connections[c]
			if link != nil && len(s.ExperimentalWakeWords) > 0 {
				// HA toggles verification by unsubscribing and subscribing on
				// this connection. Stop/fence the voice writer before rebinding;
				// closing TCP here loses HA's freshly loaded wake configuration.
				link.closeTransport.Store(false)
			}
			s.mu.Unlock()
			s.voiceClosed(c)
			if link != nil {
				<-link.done
				if !link.closeTransport.Load() {
					// HA re-subscribes callbacks without aborting its old
					// pipeline. End it before the next voice generation.
					return c.Send(&api.VoiceAssistantRequest{Start: false})
				}
			}
			return nil
		}
		if m.Flags&subscribeAPIAudio == 0 {
			return errors.New("assist: peer did not negotiate API audio")
		}
		if m.Flags&subscribeWakeAudio != 0 && len(s.ExperimentalWakeWords) == 0 {
			return errors.New("assist: wake audio requested without configured models")
		}
		s.mu.Lock()
		if s.connections == nil {
			s.connections = make(map[*esphome.Conn]*connection)
		}
		if s.connections[c] != nil {
			s.mu.Unlock()
			return nil
		}
		link := &connection{wakeAudio: m.Flags&subscribeWakeAudio != 0, done: make(chan struct{})}
		link.closeTransport.Store(true)
		link.Queue = outbox.New(func() error {
			if link.closeTransport.Load() {
				return c.Close()
			}
			return nil
		})
		generation, err := s.Engine.Attach(link)
		if err != nil {
			s.mu.Unlock()
			return err
		}
		link.generation = generation
		s.connections[c] = link
		s.mu.Unlock()
		s.trace("subscribed", map[string]string{"api_audio": "true", "wake_audio": strconv.FormatBool(link.wakeAudio)})
		go func() {
			defer close(link.done)
			_ = link.Run(func(command session.Command) error {
				switch command.Kind {
				case "start":
					request, err := s.startRequest(command, link.wakeAudio)
					if err != nil {
						s.trace("invalid_wake_evidence", nil)
						return err
					}
					return c.Send(request)
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
		case eventWakeRejected:
			// This protocol has no turn ID. Fence rejected capture and late
			// events through the same generation boundary as cancellation.
			s.Engine.Disconnect(generation, "wake_rejected")
			return nil
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
	if s.Controls != nil {
		s.Controls.ConnClosed(c)
	}
	s.voiceClosed(c)
}

func (s *Server) voiceClosed(c *esphome.Conn) {
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
