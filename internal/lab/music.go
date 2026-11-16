package lab

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"sync"
	"time"
)

// Music is a virtual, bounded 48 kHz stereo PCM16 render route. It is lab
// instrumentation, not a Sendspin implementation or a physical audio driver.
// The upstream C++ decoder feeds it; the voice clock consumes it every 10 ms.
type Music struct {
	mu                                     sync.Mutex
	enabled, privacy, stopped, muted, duck bool
	generation, stream                     uint64
	streamEnded                            bool
	queue                                  []int16
	volume, gain                           float64
	received, played                       uint64
	blocks                                 []MusicBlock
	recording                              bool
	record                                 []int16
}

type MusicBlock struct {
	At           time.Time `json:"at"`
	Frame        uint64    `json:"frame"`
	Frames       int       `json:"frames"`
	VoiceSamples int       `json:"voice_samples"`
	Ducking      bool      `json:"ducking"`
	Gain         float64   `json:"gain"`
	InputRMS     float64   `json:"input_rms"`
	OutputRMS    float64   `json:"output_rms"`
}

type MusicState struct {
	Enabled        bool    `json:"enabled"`
	Generation     uint64  `json:"generation"`
	Stream         uint64  `json:"stream"`
	Ducking        bool    `json:"ducking"`
	Privacy        bool    `json:"privacy"`
	Stopped        bool    `json:"stopped"`
	Muted          bool    `json:"muted"`
	Volume         float64 `json:"volume"`
	Gain           float64 `json:"gain"`
	QueuedFrames   int     `json:"queued_frames"`
	ReceivedFrames uint64  `json:"received_frames"`
	PlayedFrames   uint64  `json:"played_frames"`
}

func NewMusic() *Music { return &Music{volume: 1} }

func (m *Music) Enable(enabled bool) uint64 {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generation++
	m.enabled = enabled
	m.stream = 0
	m.queue = nil
	m.gain = 0
	return m.generation
}

func (m *Music) SetVoiceActive(active bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.duck = active
}

func (m *Music) SetPrivacy(muted bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.privacy = muted
	if muted {
		m.stopped, m.gain = true, 0
	}
}

func (m *Music) Stop() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.stopped, m.gain = true, 0
}

// Play is an explicit local action, not a voice-completion side effect.
func (m *Music) Play() {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.privacy {
		m.stopped = false
	}
}

func (m *Music) BeginStream(generation, stream uint64) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enabled || generation != m.generation || stream <= m.stream {
		return errors.New("stale music stream")
	}
	m.stream, m.queue, m.streamEnded = stream, nil, false
	return nil
}

func (m *Music) EndStream(generation, stream uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if generation == m.generation && stream == m.stream {
		m.queue = nil
		m.streamEnded = true
	}
}

func (m *Music) SetVolume(generation uint64, volume float64, muted bool) error {
	if math.IsNaN(volume) || volume < 0 || volume > 1 {
		return errors.New("invalid music gain")
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if !m.enabled || generation != m.generation {
		return errors.New("stale music volume")
	}
	m.volume, m.muted = volume, muted
	if muted {
		m.gain = 0
	}
	return nil
}

// Write waits for these frames to be consumed by the virtual clock, not merely
// accepted. A cancelled/replaced stream cannot acknowledge or render old PCM.
func (m *Music) Write(ctx context.Context, generation, stream uint64, pcm []int16) error {
	if len(pcm) == 0 || len(pcm) > 960 || len(pcm)%2 != 0 {
		return errors.New("expected at most 480 stereo frames")
	}
	m.mu.Lock()
	if !m.enabled || m.streamEnded || generation != m.generation || stream == 0 || stream != m.stream {
		m.mu.Unlock()
		return errors.New("stale music PCM")
	}
	if len(m.queue)+len(pcm) > 9600 {
		m.mu.Unlock()
		return errors.New("music queue full")
	}
	m.queue = append(m.queue, pcm...)
	m.received += uint64(len(pcm) / 2)
	target := m.played + uint64(len(m.queue)/2)
	m.mu.Unlock()
	ticker := time.NewTicker(time.Millisecond)
	defer ticker.Stop()
	for {
		m.mu.Lock()
		valid := m.enabled && !m.streamEnded && generation == m.generation && stream == m.stream
		done := m.played >= target
		m.mu.Unlock()
		if !valid {
			return errors.New("music stream invalidated")
		}
		if done {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func (m *Music) render(voice []int16) {
	m.mu.Lock()
	defer m.mu.Unlock()
	n := min(len(m.queue)/2, 480)
	target := m.volume
	if m.duck {
		target *= 0.2
	} // Lab choice: approximately -14 dB.
	if !m.enabled || m.privacy || m.stopped || m.muted {
		target, m.gain = 0, 0
	}
	var inputEnergy, outputEnergy float64
	for frame := range 480 {
		// A bounded 20 ms linear ramp. Safety gates above silence immediately.
		m.gain += max(-1.0/960, min(1.0/960, target-m.gain))
		for channel := range 2 {
			var sample int16
			if frame < n {
				sample = m.queue[frame*2+channel]
			}
			music := int16(math.Round(float64(sample) * m.gain))
			inputEnergy += float64(sample) * float64(sample)
			outputEnergy += float64(music) * float64(music)
			mixed := int(music)
			// Explicit lab resampler: repeat each mono 16 kHz voice sample
			// three times, then duplicate to stereo. Not a production resampler.
			if frame/3 < len(voice) {
				mixed += int(voice[frame/3])
			}
			if m.recording && len(m.record) < 48000*2*60 {
				m.record = append(m.record, int16(max(-32768, min(32767, mixed))))
			}
		}
	}
	if n > 0 {
		m.played += uint64(n)
		m.queue = m.queue[n*2:]
		m.blocks = append(m.blocks, MusicBlock{time.Now().UTC(), m.played - uint64(n), n, len(voice), m.duck, m.gain, math.Sqrt(inputEnergy / float64(n*2)), math.Sqrt(outputEnergy / float64(n*2))})
		if len(m.blocks) > 4096 {
			m.blocks = m.blocks[len(m.blocks)-4096:]
		}
	}
}

func (m *Music) Snapshot() MusicState {
	m.mu.Lock()
	defer m.mu.Unlock()
	return MusicState{m.enabled, m.generation, m.stream, m.duck, m.privacy, m.stopped, m.muted, m.volume, m.gain, len(m.queue) / 2, m.received, m.played}
}

func (m *Music) Blocks() []MusicBlock {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]MusicBlock(nil), m.blocks...)
}

func (m *Music) Record() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.record, m.recording = nil, true
}

func (m *Music) WAV() []byte {
	m.mu.Lock()
	defer m.mu.Unlock()
	pcmBytes := len(m.record) * 2
	wav := make([]byte, 44+pcmBytes)
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(36+pcmBytes))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 2)
	binary.LittleEndian.PutUint32(wav[24:], 48000)
	binary.LittleEndian.PutUint32(wav[28:], 192000)
	binary.LittleEndian.PutUint16(wav[32:], 4)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], uint32(pcmBytes))
	for i, s := range m.record {
		binary.LittleEndian.PutUint16(wav[44+i*2:], uint16(s))
	}
	return wav
}
