// Package audio defines the initial single-stream PCM contract. It does not
// claim AEC, reference alignment or a particular OS audio route.
package audio

import (
	"context"
	"errors"
	"time"
)

const (
	SampleRate     = 16000
	MaxSamples     = 1024 // At most 64 ms per mono PCM16 block.
	PreRollSamples = 3 * SampleRate / 2
)

// Frame is a host-owned capture block. Samples are copied before retention.
// CapturedAt is monotonic time since capture opened, not a wall-clock timestamp.
// A host must flag dropped samples, route changes and capture restarts.
type Frame struct {
	Samples       []int16
	Sequence      uint64
	CapturedAt    time.Duration
	Discontinuity bool
}

func (f Frame) Validate() error {
	if len(f.Samples) == 0 || len(f.Samples) > MaxSamples || f.CapturedAt < 0 {
		return errors.New("audio: expected 1..1024 mono PCM16 samples and nonnegative capture time")
	}
	return nil
}

// Playback is implemented by the host, not a backend. Write applies bounded
// backpressure. Drain returns only after physical playback, never socket receipt
// or queue acceptance. Both operations must honor cancellation. Flush must
// promptly silence and invalidate in-flight writes, is concurrent-safe, and must
// not wait for network I/O. A new write with a cancelled context must not play.
type Playback interface {
	Write(context.Context, []int16) error
	Drain(context.Context) error
	Flush()
}

// PlaybackStream is implemented by outputs that can invalidate a whole stream
// even when no Write or Drain is in progress. StreamContext must not block on
// output. Use the child for the source, Writes and Drain; cancel its parent at
// the end of the stream.
type PlaybackStream interface {
	StreamContext(context.Context) context.Context
}

var (
	ErrPlaybackUnderrun    = errors.New("audio: playback underrun")
	ErrPlaybackInterrupted = errors.New("audio: playback interrupted")
)

// PreRoll is a bounded mono ring, owned by a single session lock.
type PreRoll struct {
	samples [PreRollSamples]int16
	n, next int
}

func (p *PreRoll) Add(samples []int16) {
	for _, s := range samples {
		p.samples[p.next] = s
		p.next = (p.next + 1) % PreRollSamples
		if p.n < PreRollSamples {
			p.n++
		}
	}
}

func (p *PreRoll) Snapshot() []int16 {
	out := make([]int16, p.n)
	start := (p.next - p.n + PreRollSamples) % PreRollSamples
	for i := range out {
		out[i] = p.samples[(start+i)%PreRollSamples]
	}
	return out
}

func (p *PreRoll) Clear() { *p = PreRoll{} }
