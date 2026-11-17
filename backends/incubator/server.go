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
	"github.com/farooqu/encore-satellite/core/session"
	"github.com/farooqu/encore-satellite/core/wake"
)

type Server struct {
	Engine             *session.Engine
	Name, MAC, Version string
	// EnableModels adds the protocol-2 mixednet model executor. Capture must
	// then be supplied through Server.Capture, not directly to the engine.
	EnableModels bool
	modelMu      sync.Mutex
	models       *modelSet
}

type link struct{ *outbox.Queue }

func (*link) Backend() session.Backend { return session.Incubator }

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
	models := &modelSet{}
	if s.EnableModels {
		s.modelMu.Lock()
		s.models = models
		s.modelMu.Unlock()
		defer func() {
			s.modelMu.Lock()
			s.models = nil
			s.modelMu.Unlock()
		}()
	}
	stop := context.AfterFunc(ctx, func() { _ = l.Close() })
	defer stop()
	var writeMu sync.Mutex
	send := func(kind byte, data any) error {
		writeMu.Lock()
		defer writeMu.Unlock()
		b, err := json.Marshal(data)
		if err != nil {
			return err
		}
		_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
		return writeFrame(conn, kind, b)
	}
	hello := map[string]any{
		"protocol": 2, "name": s.Name, "mac": s.MAC, "model": "encore-core", "firmware": s.Version,
		"sample_rate": 16000, "channels": 1, "bits_per_sample": 16,
		"preroll_ms": audio.PreRollSamples * 1000 / audio.SampleRate,
		"listen":     true, "aec": false, "monitor": false, "intercom": false,
	}
	if s.EnableModels {
		hello["wake_format"], hello["model_slots"], hello["wake_models"] = "mixednet", modelSlots, 0
		hello["feature_size"], hello["max_pool"], hello["stop_word"] = 40, true, true
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
				writeMu.Lock()
				defer writeMu.Unlock()
				_ = conn.SetWriteDeadline(time.Now().Add(2 * time.Second))
				return writeFrame(conn, 0x03, b)
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
		case 0x20, 0x21, 0x22, 0x23:
			if s.EnableModels {
				if kind == 0x23 {
					_ = s.Engine.Stop()
				}
				s.modelMu.Lock()
				result := models.transfer(kind, data)
				s.modelMu.Unlock()
				if result != nil {
					err = send(0x07, result)
				}
			}
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
					Text   string `json:"text"`
					Active bool   `json:"active"`
				} `json:"data"`
			}
			if err = json.Unmarshal(data, &event); err != nil {
				return fmt.Errorf("incubator: invalid event: %w", err)
			}
			err = s.Engine.Handle(generation, session.Event{Kind: event.Type, Text: event.Data.Text, Active: event.Data.Active})
		case 0x13:
			var event struct {
				Reason string `json:"reason"`
				Listen bool   `json:"listen"`
			}
			if err = json.Unmarshal(data, &event); err != nil {
				return err
			}
			err = s.Engine.Handle(generation, session.Event{Kind: "hangup", Reason: event.Reason, Listen: event.Listen})
			// Unknown optional types are ignored; monitoring/intercom stay unadvertised.
		}
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
}

// Capture shares one privacy-gated PCM stream with pre-roll, voice and local
// model execution. Models are connection-scoped, never resurrected on reconnect.
func (s *Server) Capture(frame audio.Frame) error {
	if err := s.Engine.Capture(frame); err != nil {
		return err
	}
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	state := s.Engine.Snapshot()
	if s.models == nil || state.Muted || !state.Connected {
		return nil
	}
	for i := range s.models.models {
		m := &s.models.models[i]
		stop := m.header.ID == "stop" || m.header.Word == "stop"
		armed := state.Alert || state.Phase == "speaking"
		if stop && armed != m.armed {
			m.detector.Reset()
			m.armed = armed
		}
		if stop && !armed {
			continue
		}
		if !stop && state.Phase != "idle" {
			continue
		}
		p, peak, hit := m.detector.Feed(frame.Samples)
		if hit {
			if stop {
				return s.Engine.Stop()
			}
			return s.Engine.Wake(wake.Detection{Word: m.header.Word, Probability: p, MaxProbability: peak, Cutoff: uint8(float32(m.header.Cutoff)*255 + 0.5)})
		}
	}
	return nil
}

func (s *Server) ModelCount() int {
	s.modelMu.Lock()
	defer s.modelMu.Unlock()
	if s.models == nil {
		return 0
	}
	return len(s.models.models)
}
