// Model-transfer validation follows Techo5's session.go at
// 2b091439765d5f881de4b89255b8cf0637d80cba (MIT; licenses/Techo5-MIT.txt).
// Encore adds bounded asynchronous inference and generation-fenced callbacks.
package incubator

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"math"
	"slices"
	"sync"
	"sync/atomic"

	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/core/session"
	"github.com/farooqu/encore-satellite/core/wake"
	"github.com/farooqu/encore-satellite/core/wake/mixednet"
)

const modelSlots = 6

// The protocol's slot is only a hint: the app can leave a hole before stop.
// Capacity and replacement are keyed by ID, as in the upstream firmware.
type modelHeader struct {
	Format   string  `json:"format"`
	ID       string  `json:"id"`
	WakeWord string  `json:"wake_word"`
	Kind     string  `json:"kind"`
	Size     int     `json:"size"`
	SHA256   string  `json:"sha256"`
	Cutoff   float64 `json:"probability_cutoff"`
	Window   int     `json:"sliding_window_size"`
	Step     int     `json:"feature_step_size"`
}

type modelUpload struct {
	header modelHeader
	blob   []byte
}

type wakeModel struct {
	header   modelHeader
	detector *mixednet.Detector
	armed    bool
}

type modelFrame struct {
	audio.Frame
	epoch uint64
}

// modelSession is deliberately connection-local and volatile. Its worker owns
// inference; capture only copies into a four-block queue and never waits for DSP.
// mu serializes inference with installation/clear, never with the privacy gate.
type modelSession struct {
	engine     *session.Engine
	generation uint64
	mu         sync.Mutex
	models     [modelSlots]*wakeModel
	upload     *modelUpload // only the protocol reader accesses staging
	frames     chan modelFrame
	epoch      atomic.Uint64
	cancel     context.CancelFunc
	done       chan struct{}
}

func newModelSession(parent context.Context, engine *session.Engine, generation uint64) *modelSession {
	ctx, cancel := context.WithCancel(parent)
	m := &modelSession{engine: engine, generation: generation, frames: make(chan modelFrame, 4), cancel: cancel, done: make(chan struct{})}
	go func() {
		defer close(m.done)
		defer m.clear()
		var last audio.Frame
		var haveFrame bool
		for {
			select {
			case <-ctx.Done():
				return
			case frame := <-m.frames:
				listen, stop := engine.WakeState(generation)
				m.mu.Lock()
				if ctx.Err() == nil && frame.epoch == m.epoch.Load() {
					if !haveFrame || frame.Discontinuity || frame.Sequence != last.Sequence+1 || frame.CapturedAt <= last.CapturedAt {
						m.reset()
					}
					for _, model := range m.models {
						if model == nil {
							continue
						}
						isStop := model.header.ID == "stop" || model.header.WakeWord == "stop"
						armed := listen
						if isStop {
							armed = stop
						}
						if model.armed != armed {
							model.detector.Reset()
							model.armed = armed
						}
						if !armed {
							continue
						}
						mean, peak, hit := model.detector.Feed(frame.Samples)
						if hit && ctx.Err() == nil {
							word := model.header.WakeWord
							if isStop {
								word = "stop"
							}
							_ = engine.WakeFor(generation, wake.Detection{Word: word, Probability: mean, MaxProbability: peak, Cutoff: uint8(float32(float32(model.header.Cutoff)*255) + .5)})
						}
					}
					last = frame.Frame
					last.Samples = nil
					haveFrame = true
				}
				m.mu.Unlock()
				clear(frame.Samples)
			}
		}
	}()
	return m
}

func (m *modelSession) close() {
	m.cancel()
	<-m.done
	// Server.captureMu has already removed the pointer and joined in-flight
	// feed calls. No producer can retain audio after this drain.
	for {
		select {
		case frame := <-m.frames:
			clear(frame.Samples)
		default:
			return
		}
	}
}

func (m *modelSession) feed(frame audio.Frame) {
	if frame.Validate() != nil || frame.Discontinuity {
		return
	}
	// No registry lock and no inference under the host's privacy gate. Dropped
	// blocks create sequence gaps; the worker resets all streaming history at
	// the next block, never feeding noncontiguous audio as continuous speech.
	frame.Samples = slices.Clone(frame.Samples)
	select {
	case <-m.done:
		clear(frame.Samples)
	case m.frames <- modelFrame{Frame: frame, epoch: m.epoch.Load()}:
		// Ownership of samples passed to the worker.
	default:
		clear(frame.Samples)
	}
}

func (m *modelSession) reset() {
	for _, model := range m.models {
		if model != nil {
			model.detector.Reset()
			model.armed = false
		}
	}
}

func (m *modelSession) clear() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.reset()
	m.models = [modelSlots]*wakeModel{}
	m.epoch.Add(1)
}

func (m *modelSession) count() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := 0
	for _, model := range m.models {
		if model != nil {
			n++
		}
	}
	return n
}

func (m *modelSession) install(upload *modelUpload) error {
	detector, err := mixednet.New(upload.blob, upload.header.Step, upload.header.Window, upload.header.Cutoff)
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	at := -1
	for i, model := range m.models {
		if model == nil && at < 0 {
			at = i
		}
		if model != nil && model.header.ID == upload.header.ID {
			at = i
			break
		}
	}
	if at < 0 {
		return errors.New("model capacity exceeded")
	}
	m.reset()
	m.models[at] = &wakeModel{header: upload.header, detector: detector}
	m.epoch.Add(1) // New models cannot consume audio queued before installation.
	return nil
}

func (m *modelSession) handle(kind byte, data []byte, send func(byte, any) error) error {
	reply := func(id string, err error) error {
		why := ""
		if err != nil {
			why = err.Error()
		}
		return send(0x07, map[string]any{"ok": err == nil, "id": id, "models": m.count(), "error": why})
	}
	switch kind {
	case 0x23:
		m.upload = nil
		m.clear()
		return m.engine.StopFor(m.generation)
	case 0x20:
		m.upload = nil
		h := modelHeader{Kind: "wake", Cutoff: .85, Window: 5, Step: 10}
		if json.Unmarshal(data, &h) != nil {
			return reply("", errors.New("invalid model header"))
		}
		if h.WakeWord == "" {
			h.WakeWord = h.ID
		}
		digest, err := hex.DecodeString(h.SHA256)
		if err != nil || len(digest) != sha256.Size || h.Format != "mixednet" || h.Size <= 0 || h.Size > 512*1024 || len(h.ID) == 0 || len(h.ID) > 128 || len(h.WakeWord) > 128 || (h.Kind != "wake" && h.Kind != "") || h.Step < 1 || h.Step > 30 || h.Window < 1 || h.Window > 255 || math.IsNaN(h.Cutoff) || h.Cutoff < 0 || h.Cutoff > 1 {
			return reply(h.ID, errors.New("invalid model header, format or settings"))
		}
		if h.WakeWord != "stop" {
			if err := (wake.Detection{Word: h.WakeWord}).Validate(); err != nil {
				return reply(h.ID, err)
			}
		}
		if m.engine.Snapshot().Phase != "idle" {
			return reply(h.ID, errors.New("model upload requires an idle call"))
		}
		m.upload = &modelUpload{header: h, blob: make([]byte, 0, h.Size)}
	case 0x21:
		if u := m.upload; u != nil {
			if len(data) > u.header.Size-len(u.blob) {
				m.upload = nil
				return reply(u.header.ID, errors.New("model exceeds declared size"))
			}
			u.blob = append(u.blob, data...)
		}
	case 0x22:
		u := m.upload
		m.upload = nil
		if u == nil {
			return nil
		}
		var end struct {
			ID string `json:"id"`
		}
		sum := sha256.Sum256(u.blob)
		if json.Unmarshal(data, &end) != nil || end.ID != u.header.ID || len(u.blob) != u.header.Size || hex.EncodeToString(sum[:]) != u.header.SHA256 {
			return reply(u.header.ID, errors.New("model id, size or sha256 mismatch"))
		}
		return reply(u.header.ID, m.install(u))
	}
	return nil
}
