// Package incubator implements the minimal native Voice Incubator protocol 2.
// The protocol is unencrypted and unauthenticated: bind only to a trusted network.
package incubator

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"sync"
	"time"

	"github.com/farooqu/encore-satellite/backends/internal/outbox"
	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/core/presentation"
	"github.com/farooqu/encore-satellite/core/session"
)

type Server struct {
	Engine             *session.Engine
	Name, MAC, Version string
	// EnableModels requires the host to deliver privacy-gated audio via Capture.
	// Without it HELLO makes no model-execution claim (e.g. manual-only labs).
	EnableModels bool
	captureMu    sync.RWMutex
	models       *modelSession
}

type link struct{ *outbox.Queue }

func (*link) Backend() session.Backend { return session.Incubator }

// Capture supplies both session pre-roll/live audio and local model inference.
// The host must call it only inside its OS-level privacy boundary. DSP is queued
// without blocking capture; models never retain the caller's sample buffer.
func (s *Server) Capture(frame audio.Frame) error {
	s.captureMu.RLock()
	defer s.captureMu.RUnlock()
	if err := s.Engine.Capture(frame); err != nil {
		return err
	}
	if s.models != nil {
		s.models.feed(frame)
	}
	return nil
}

func (s *Server) ModelCount() int {
	s.captureMu.RLock()
	defer s.captureMu.RUnlock()
	if s.models == nil {
		return 0
	}
	return s.models.count()
}

// Snapshot adds Incubator readiness without taking the inference lock. The host
// must supply current permission/capture health, not merely a connected socket
// or a previously delivered frame; unknown health must be passed as false.
// Existing engine-only consumers remain unchanged until their host is wired.
func (s *Server) Snapshot(captureReady bool) presentation.State {
	s.captureMu.RLock()
	defer s.captureMu.RUnlock()
	state := s.Engine.Snapshot()
	state.CaptureReady = captureReady && state.Connected && !state.Muted
	state.TalkReady = state.TalkAllowed && state.CaptureReady
	if m := s.models; m != nil && m.generation == state.Generation {
		state.WakeModels = int(m.ordinary.Load())
		state.WakeArmed = state.WakeAllowed && m.armed.Load()
	}
	state.WakeReady = state.CaptureReady && state.WakeModels > 0 && state.WakeArmed
	return state
}

// Serve refuses competing clients. The caller owns mDNS advertisement and the
// listener address; this adapter never opens a microphone or a LAN control API.
func (s *Server) Serve(ctx context.Context, listener net.Listener) error {
	if s.Name == "" || s.MAC == "" {
		return errors.New("incubator: name and stable MAC required")
	}
	stop := context.AfterFunc(ctx, func() { _ = listener.Close() })
	defer stop()
	var mu sync.Mutex
	var current net.Conn
	var workers sync.WaitGroup
	defer workers.Wait()
	defer func() {
		mu.Lock()
		if current != nil {
			_ = current.Close()
		}
		mu.Unlock()
	}()
	for {
		conn, err := listener.Accept()
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		mu.Lock()
		if current != nil {
			mu.Unlock()
			_ = conn.Close()
			continue
		}
		current = conn
		mu.Unlock()
		workers.Go(func() {
			defer func() { _ = conn.Close(); mu.Lock(); current = nil; mu.Unlock() }()
			_ = s.serveConn(ctx, conn)
		})
	}
}

func (s *Server) serveConn(ctx context.Context, conn net.Conn) error {
	l := &link{outbox.New(conn.Close)}
	generation, err := s.Engine.Attach(l)
	if err != nil {
		return err
	}
	defer s.Engine.Disconnect(generation, "connection_closed")
	stop := context.AfterFunc(ctx, func() { _ = l.Close() })
	defer stop()
	var models *modelSession
	if s.EnableModels {
		models = newModelSession(ctx, s.Engine, generation)
		s.captureMu.Lock()
		s.models = models
		s.captureMu.Unlock()
		defer func() {
			s.captureMu.Lock()
			s.models = nil
			s.captureMu.Unlock()
			models.close()
		}()
	}
	var writeMu sync.Mutex
	write := func(kind byte, data []byte) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		return writeFrame(conn, kind, data)
	}
	send := func(kind byte, data any) error {
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		return write(kind, b)
	}
	hello := map[string]any{
		"protocol": 2, "name": s.Name, "mac": s.MAC, "model": "encore-core", "firmware": s.Version,
		"sample_rate": 16000, "channels": 1, "bits_per_sample": 16,
		"preroll_ms": audio.PreRollSamples * 1000 / audio.SampleRate,
		"listen":     true, "aec": false, "monitor": false, "intercom": false,
	}
	if models != nil {
		hello["wake_format"], hello["model_slots"] = "mixednet", modelSlots
		hello["wake_models"], hello["feature_size"] = 0, 40
		hello["max_pool"], hello["stop_word"] = true, true
	}
	if err := send(0x01, hello); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		defer close(done)
		_ = l.Run(func(c session.Command) error {
			switch c.Kind {
			case "start":
				return send(0x02, map[string]any{"wake_word": c.Detection.Word, "preroll_bytes": c.PreRollBytes, "aec_preroll_bytes": 0, "probability": c.Detection.Probability, "max_probability": c.Detection.MaxProbability, "cutoff": c.Detection.Cutoff})
			case "audio":
				b := make([]byte, len(c.Samples)*2)
				for i, v := range c.Samples {
					binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
				}
				return write(0x03, b)
			case "cancel":
				// Flush cancellation before retiring this voice generation.
				// AUDIO_OUT_END cannot distinguish the ACK from an old stream.
				if err := send(0x04, map[string]string{"reason": c.Reason}); err != nil {
					return err
				}
				return io.EOF
			case "end":
				return send(0x04, map[string]string{"reason": c.Reason})
			case "drained":
				return send(0x06, map[string]any{})
			case "announcing", "announced":
				return send(0x09, map[string]string{"type": c.Kind})
			case "stop_alert":
				return send(0x09, map[string]string{"type": "stop_timer"})
			}
			return nil
		})
		_ = l.Close()
	}()
	defer func() { _ = l.Close(); <-done }()
	for {
		kind, data, err := readFrame(conn)
		if err != nil {
			return err
		}
		switch kind {
		case 0x10:
			if len(data) == 0 || len(data)%2 != 0 {
				return errors.New("incubator: malformed PCM16")
			}
			pcm := make([]int16, len(data)/2)
			for i := range pcm {
				pcm[i] = int16(binary.LittleEndian.Uint16(data[i*2:]))
			}
			err = s.Engine.Output(generation, pcm)
		case 0x11:
			err = s.Engine.EndOutput(generation)
		case 0x12:
			var event struct {
				Type string `json:"type"`
				Data struct {
					Text    string          `json:"text"`
					Active  bool            `json:"active"`
					Speaker json.RawMessage `json:"speaker"`
					Score   json.RawMessage `json:"score"`
					R       json.RawMessage `json:"r"`
					G       json.RawMessage `json:"g"`
					B       json.RawMessage `json:"b"`
					On      json.RawMessage `json:"on"`
				} `json:"data"`
			}
			if err = json.Unmarshal(data, &event); err != nil {
				return fmt.Errorf("incubator: invalid event: %w", err)
			}
			incoming := session.Event{Kind: event.Type, Text: event.Data.Text, Active: event.Data.Active}
			if event.Type == "processing" {
				var name string
				if json.Unmarshal(event.Data.Speaker, &name) == nil && name != "" && name != "unknown" {
					incoming.Speaker.Name = name
					var score *float64
					if json.Unmarshal(event.Data.Score, &score) == nil && score != nil {
						incoming.Speaker.Score, incoming.Speaker.ScoreKnown = *score, true
					}
					incoming.Speaker.ColorKnown = true
					for i, raw := range []json.RawMessage{event.Data.R, event.Data.G, event.Data.B} {
						var channel *int
						if json.Unmarshal(raw, &channel) != nil || channel == nil || *channel < 0 || *channel > 255 {
							incoming.Speaker.ColorKnown = false
							break
						}
						incoming.Speaker.RGB[i] = byte(*channel)
					}
					if !incoming.Speaker.ColorKnown {
						incoming.Speaker.RGB = [3]byte{}
					}
				}
			} else if event.Type == "night" {
				// Optional visual metadata must not tear down a healthy call.
				// Null/absent/invalid values leave the last valid policy intact.
				if json.Unmarshal(event.Data.On, &incoming.Night) != nil {
					incoming.Night = nil
				}
			}
			err = s.Engine.Handle(generation, incoming)
		case 0x13:
			var event struct {
				Reason string `json:"reason"`
				Listen bool   `json:"listen"`
			}
			if err = json.Unmarshal(data, &event); err != nil {
				return err
			}
			err = s.Engine.Handle(generation, session.Event{Kind: "hangup", Reason: event.Reason, Listen: event.Listen})
		case 0x20, 0x21, 0x22, 0x23:
			if models != nil {
				err = models.handle(kind, data, send)
			}
			// Unknown optional types are ignored as protocol 2 requires.
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
}
