package biscuit

import (
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"sync"
	"sync/atomic"

	"github.com/farooqu/encore-satellite/backends/esphome/controls"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"
)

const ringSettingsPath = "/data/encore/ring.json"

// Keep visual policy out of config.json: old good-slot binaries reject unknown
// fields there. They may ignore this file safely and cannot erase it on save.
// Persistence never blocks the frame loop or privacy indication.
type ringSettings struct {
	path  string
	mu    sync.Mutex
	value atomic.Uint32
}

func loadRingSettings(path string) (*ringSettings, error) {
	options := &RingOptions{Brightness: 100}
	f, err := os.Open(path)
	if err == nil {
		defer f.Close()
		d := json.NewDecoder(io.LimitReader(f, 4097))
		d.DisallowUnknownFields()
		if d.Decode(&options) != nil || options == nil || options.Brightness < 1 || options.Brightness > 100 || d.Decode(new(any)) != io.EOF {
			return nil, errors.New("dot: invalid ring policy")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	r := &ringSettings{path: path}
	r.store(*options)
	return r, nil
}

func (r *ringSettings) store(options RingOptions) {
	v := uint32(options.Brightness)
	if options.ReducedMotion {
		v |= 1 << 8
	}
	r.value.Store(v)
}

func (r *ringSettings) snapshot() RingOptions {
	v := r.value.Load()
	return RingOptions{Brightness: int(v & 255), ReducedMotion: v&(1<<8) != 0}
}

func (r *ringSettings) configure(brightness *float32, reduced *bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	next := r.snapshot()
	if brightness != nil {
		v := float64(*brightness)
		if math.IsNaN(v) || v < 1 || v > 100 || math.Trunc(v) != v {
			return errors.New("dot: ring brightness must be an integer from 1 to 100")
		}
		next.Brightness = int(v)
	}
	if reduced != nil {
		next.ReducedMotion = *reduced
	}
	if next == r.snapshot() {
		return nil
	}
	b, err := json.Marshal(next)
	if err != nil {
		return err
	}
	if err := writePrivate(r.path, b); err != nil {
		return err
	}
	r.store(next)
	return nil
}

var (
	ringBrightnessKey = controls.RingBrightness.Key()
	ringMotionKey     = controls.RingMotion.Key()
)

func (r *ringSettings) states() []proto.Message {
	s := r.snapshot()
	return []proto.Message{
		&api.NumberStateResponse{Key: ringBrightnessKey, State: float32(s.Brightness)},
		&api.SwitchStateResponse{Key: ringMotionKey, State: s.ReducedMotion},
	}
}
