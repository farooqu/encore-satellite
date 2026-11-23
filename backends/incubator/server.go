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
	"math"
	"net"
	"strconv"
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
	// CallAEC requires the host's Capture stream to come from its AEC path.
	// Both call routes use those same samples.
	CallAEC bool
	// Monitor enables primary Live Monitor. Hosts must confirm the local
	// recording indicator via Engine.ConfirmMonitor before any capture is sent.
	Monitor bool
	// MonitorAEC adds the AEC monitor route and intercom and requires CallAEC.
	// Like Monitor, it requires a host that confirms the recording indicator.
	MonitorAEC bool
	// Display capabilities are opt-in by the host that renders the snapshots.
	EarlyTranscript, ReplyChunks, TimerNames bool
	captureMu                                sync.RWMutex
	models                                   *modelSession
}

type link struct {
	*outbox.Queue
	monitorMu sync.Mutex
	revoked   uint64
	writing   bool
}

func (l *link) Send(c session.Command) error {
	if c.Kind != "monitor_off" {
		return l.Queue.Send(c)
	}
	l.monitorMu.Lock()
	l.revoked = c.MonitorID
	writing := l.writing
	l.monitorMu.Unlock()
	if writing {
		// Do not wait behind a stalled peer or leave an in-flight monitor
		// frame alive after revocation. A fresh connection must request again.
		return l.Close()
	}
	return l.Queue.Send(c)
}

// Capture supplies both session pre-roll/live audio and local model inference.
// The host must call it only inside its OS-level privacy boundary. DSP is queued
// without blocking capture; models never retain the caller's sample buffer.
func (s *Server) Capture(frame audio.Frame) error {
	s.captureMu.RLock()
	defer s.captureMu.RUnlock()
	modelInput, monitorID, err := s.Engine.CaptureForModels(frame)
	if err != nil {
		return err
	}
	if modelInput && s.models != nil {
		s.models.feed(frame, monitorID)
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

// SetWakeWordEnabled fences queued model input as well as the engine's pre-roll.
func (s *Server) SetWakeWordEnabled(enabled bool) {
	s.captureMu.Lock()
	defer s.captureMu.Unlock()
	m := s.models
	if m != nil {
		m.mu.Lock()
		defer m.mu.Unlock()
	}
	if s.Engine.SetWakeWordEnabled(enabled) && m != nil {
		m.epoch.Add(1)
		m.reset()
	}
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
	l := &link{Queue: outbox.New(conn.Close)}
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
	monitorAEC := s.CallAEC && s.MonitorAEC
	monitor := s.Monitor || monitorAEC
	hello := map[string]any{
		"protocol": 2, "name": s.Name, "mac": s.MAC, "model": "encore-core", "firmware": s.Version,
		"sample_rate": 16000, "channels": 1, "bits_per_sample": 16,
		"preroll_ms": audio.PreRollSamples * 1000 / audio.SampleRate,
		"listen":     true, "aec": s.CallAEC, "monitor": monitor, "monitor_aec": monitorAEC, "intercom": monitorAEC,
	}
	if s.EarlyTranscript {
		hello["early_transcript"] = true
	}
	if s.ReplyChunks {
		hello["reply_chunks"] = true
	}
	if s.TimerNames {
		hello["timer_names"] = true
	}
	if monitor {
		hello["monitor_result"] = true
	}
	if s.CallAEC {
		hello["aec_lead_samples"] = 0 // one capture path, no relative pipeline delay
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
			if c.Kind == "monitor_audio" || c.Kind == "wake_report" {
				defer clear(c.Samples)
				l.monitorMu.Lock()
				if c.MonitorID <= l.revoked {
					l.monitorMu.Unlock()
					return nil
				}
				l.writing = true
				l.monitorMu.Unlock()
				defer func() {
					l.monitorMu.Lock()
					l.writing = false
					l.monitorMu.Unlock()
				}()
			}
			switch c.Kind {
			case "wake_report":
				return send(0x0B, c.WakeReport)
			case "start":
				aecPreRoll := 0
				if s.CallAEC {
					aecPreRoll = c.PreRollBytes
				}
				return send(0x02, map[string]any{"wake_word": c.Detection.Word, "preroll_bytes": c.PreRollBytes, "aec_preroll_bytes": aecPreRoll, "probability": c.Detection.Probability, "max_probability": c.Detection.MaxProbability, "cutoff": c.Detection.Cutoff})
			case "audio", "monitor_audio":
				b := make([]byte, len(c.Samples)*2)
				for i, v := range c.Samples {
					binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
				}
				if c.Kind == "monitor_audio" {
					defer clear(b)
					if monitorAEC && !c.Intercom {
						if err := write(0x0C, b); err != nil {
							return err
						}
					}
					return write(0x0A, b)
				}
				if s.CallAEC {
					// Supply the aligned AEC tap before the primary frame can
					// advance the peer's wake verifier or endpointer.
					if err := write(0x08, b); err != nil {
						return err
					}
				}
				return write(0x03, b)
			case "cancel":
				// Flush cancellation before retiring this voice generation.
				// AUDIO_OUT_END cannot distinguish the ACK from an old stream.
				if err := send(0x04, map[string]string{"reason": c.Reason}); err != nil {
					return err
				}
				return io.EOF
			case "drained":
				return send(0x06, map[string]any{})
			case "announcing", "announced", "intercom_end":
				return send(0x09, map[string]string{"type": c.Kind})
			case "monitor_result":
				return send(0x09, map[string]any{"type": c.Kind, "request_id": c.RequestID, "accepted": c.MonitorID != 0})
			case "stop_alert":
				return send(0x09, map[string]string{"type": "stop_timer"})
			}
			return nil
		})
		_ = l.Close()
	}()
	defer func() { _ = l.Close(); <-done }()
	type relayBlock struct {
		monitorID uint64
		pcm       []int16
	}
	var relayID uint64
	var relay chan relayBlock
	if monitor {
		relay = make(chan relayBlock, 4)
		relayDone := make(chan struct{})
		go func() {
			defer close(relayDone)
			for block := range relay {
				if err := s.Engine.OutputIntercom(generation, block.monitorID, block.pcm); err != nil && !errors.Is(err, context.Canceled) {
					_ = l.Close()
					return
				}
			}
		}()
		defer func() {
			// EOF must cancel a blocked relay write before waiting for it.
			s.Engine.Disconnect(generation, "connection_closed")
			close(relay)
			<-relayDone
		}()
	}
	for {
		kind, data, err := readFrame(conn)
		if err != nil {
			return err
		}
		switch kind {
		case 0x10:
			if len(data) == 0 || len(data)%2 != 0 || len(data) > 4096 {
				return errors.New("incubator: malformed PCM16")
			}
			pcm := make([]int16, len(data)/2)
			for i := range pcm {
				pcm[i] = int16(binary.LittleEndian.Uint16(data[i*2:]))
			}
			if relayID != 0 {
				// Live relay drops excess PCM rather than blocking peer off/EOF.
				select {
				case relay <- relayBlock{relayID, pcm}:
				default:
				}
			} else {
				err = s.Engine.Output(generation, pcm)
			}
		case 0x11:
			if relayID == 0 { // Relay terminator follows monitor-off, never drains the relay.
				err = s.Engine.EndOutput(generation)
			}
		case 0x12:
			var event struct {
				Type string `json:"type"`
				Data struct {
					Text    json.RawMessage `json:"text"`
					Heard   string          `json:"heard"`
					Active  bool            `json:"active"`
					Speaker json.RawMessage `json:"speaker"`
					Score   json.RawMessage `json:"score"`
					R       json.RawMessage `json:"r"`
					G       json.RawMessage `json:"g"`
					B       json.RawMessage `json:"b"`
					On      json.RawMessage `json:"on"`
					Name    json.RawMessage `json:"name"`
					Names   json.RawMessage `json:"names"`
					Timers  json.RawMessage `json:"timers"`
				} `json:"data"`
			}
			if err = json.Unmarshal(data, &event); err != nil {
				return fmt.Errorf("incubator: invalid event: %w", err)
			}
			incoming := session.Event{Kind: event.Type, Active: event.Data.Active}
			var text *string
			if raw := event.Data.Text; raw != nil {
				if err := json.Unmarshal(raw, &text); err != nil {
					if event.Type == "transcript" || event.Type == "reply_chunk" {
						continue // Bad optional captions must not interrupt playback.
					}
					return fmt.Errorf("incubator: invalid event text: %w", err)
				}
			}
			if text != nil {
				incoming.Text = *text
			} else if event.Type == "transcript" || event.Type == "reply_chunk" {
				continue
			}
			if event.Type == "not_recognized" {
				// Incubator sends the final transcript under a different key on this path.
				incoming.Text = event.Data.Heard
			} else if event.Type == "processing" {
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
			} else if event.Type == "timer_progress" {
				incoming.Timers = timerProgress(data)
			} else if event.Type == "timer_ringing" {
				_ = json.Unmarshal(event.Data.Name, &incoming.Ring.Name)
				_ = json.Unmarshal(event.Data.Timers, &incoming.Ring.Count)
				if json.Unmarshal(event.Data.Names, &incoming.Ring.Names) != nil {
					incoming.Ring.Names = nil
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
		case 0x14:
			if monitor {
				var request struct {
					Active, Intercom bool
					RequestID        uint64 `json:"request_id"`
				}
				if err = json.Unmarshal(data, &request); err != nil {
					return err
				}
				if request.Active && request.Intercom && !monitorAEC {
					if request.RequestID != 0 {
						err = l.Send(session.Command{Kind: "monitor_result", RequestID: request.RequestID})
					}
					break
				}
				var id uint64
				id, err = s.Engine.SetMonitor(generation, request.Active, request.Intercom, request.RequestID)
				relayID = 0
				if request.Intercom {
					relayID = id
				}
				if errors.Is(err, session.ErrUnavailable) && (request.RequestID != 0 || !(request.Active && request.Intercom)) {
					err = nil // Report negotiated denial without interrupting a turn.
				}
			}
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

func timerProgress(data []byte) []session.TimerProgress {
	var event struct {
		Data map[string]json.RawMessage `json:"data"`
	}
	if json.Unmarshal(data, &event) != nil {
		return nil
	}
	var shown *int
	if json.Unmarshal(event.Data["shown"], &shown) != nil || shown == nil || *shown < 0 {
		return nil
	}
	if *shown == 0 {
		return []session.TimerProgress{}
	}
	count := 1 // Older servers only send the soonest timer.
	if raw := event.Data["count"]; raw != nil {
		if json.Unmarshal(raw, &count) != nil || count < 1 {
			return nil
		}
	}
	timers := make([]session.TimerProgress, min(count, presentation.TimerSlots))
	for i := range timers {
		slot := strconv.Itoa(i)
		raw := event.Data["left"+slot]
		if raw == nil && i == 0 {
			raw = event.Data["remaining"]
		}
		var seconds *float64
		if json.Unmarshal(raw, &seconds) != nil || seconds == nil || *seconds < 0 || *seconds >= float64(math.MaxInt64)/float64(time.Second) {
			return nil // Optional visual metadata must not break a voice session.
		}
		timers[i].Left = time.Duration(*seconds * float64(time.Second))
		_ = json.Unmarshal(event.Data["name"+slot], &timers[i].Name)
	}
	return timers
}
