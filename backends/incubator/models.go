package incubator

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/farooqu/encore-satellite/core/wake"
	"github.com/farooqu/encore-satellite/core/wake/mixednet"
)

const modelSlots = 6

type modelHeader struct {
	ID     string  `json:"id"`
	Word   string  `json:"wake_word"`
	Format string  `json:"format"`
	Kind   string  `json:"kind"`
	Size   int     `json:"size"`
	Hash   string  `json:"sha256"`
	Step   int     `json:"feature_step_size"`
	Window int     `json:"sliding_window_size"`
	Cutoff float64 `json:"probability_cutoff"`
}

type loadedModel struct {
	header   modelHeader
	detector *mixednet.Detector
	armed    bool
}

type modelSet struct {
	models []loadedModel
	upload *modelHeader
	blob   []byte
}

// Transfers are bounded, validate before activation and replace by model ID,
// not the upstream wire slot (which can have holes before the stop model).
func (m *modelSet) transfer(kind byte, data []byte) map[string]any {
	reply := func(id string, err error) map[string]any {
		why := ""
		if err != nil {
			why = err.Error()
		}
		return map[string]any{"ok": err == nil, "id": id, "models": len(m.models), "error": why}
	}
	switch kind {
	case 0x23:
		*m = modelSet{}
	case 0x20:
		m.upload, m.blob = nil, nil
		h := modelHeader{Kind: "wake", Step: 10, Window: 5, Cutoff: .85}
		if err := json.Unmarshal(data, &h); err != nil {
			return reply("", err)
		}
		if h.Word == "" {
			h.Word = h.ID
		}
		if h.Format != "mixednet" || (h.Kind != "wake" && h.Kind != "") || h.Size <= 0 || h.Size > 512*1024 || len(h.ID) > 128 || len(h.Hash) != 64 || (h.Word != "stop" && (wake.Detection{Word: h.Word}).Validate() != nil) {
			return reply(h.ID, errors.New("invalid model format, identity or bounds"))
		}
		if h.ID == "" {
			return reply(h.ID, errors.New("empty model id"))
		}
		m.upload, m.blob = &h, make([]byte, 0, h.Size)
	case 0x21:
		if m.upload == nil {
			return reply("", errors.New("no model transfer"))
		}
		if len(m.blob)+len(data) > m.upload.Size {
			id := m.upload.ID
			m.upload, m.blob = nil, nil
			return reply(id, errors.New("model exceeds declared size"))
		}
		m.blob = append(m.blob, data...)
	case 0x22:
		h, blob := m.upload, m.blob
		m.upload, m.blob = nil, nil
		if h == nil {
			return reply("", errors.New("no model transfer"))
		}
		var end struct {
			ID string `json:"id"`
		}
		sum := sha256.Sum256(blob)
		if json.Unmarshal(data, &end) != nil || end.ID != h.ID || len(blob) != h.Size || hex.EncodeToString(sum[:]) != h.Hash {
			return reply(h.ID, errors.New("model id, size or sha256 mismatch"))
		}
		d, err := mixednet.New(blob, h.Step, h.Window, h.Cutoff)
		if err != nil {
			return reply(h.ID, err)
		}
		for i := range m.models {
			if m.models[i].header.ID == h.ID {
				m.models[i] = loadedModel{header: *h, detector: d}
				return reply(h.ID, nil)
			}
		}
		if len(m.models) == modelSlots {
			return reply(h.ID, fmt.Errorf("at most %d models", modelSlots))
		}
		m.models = append(m.models, loadedModel{header: *h, detector: d})
		return reply(h.ID, nil)
	}
	return nil
}
