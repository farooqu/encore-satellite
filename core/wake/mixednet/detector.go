// Adapted from Voice Incubator's wake_detector.cpp: Go PCM buffering and API,
// with the firmware's ModelStream score window and quiet-slice guard.
// See NOTICE and LICENSE-APACHE-2.0.txt in this package.
package mixednet

import (
	"fmt"
	"math"

	"github.com/farooqu/encore-satellite/core/wake/mwwfront"
)

// Detector consumes mono, signed 16-bit PCM at 16 kHz. It owns its model and
// streaming state and is not safe for concurrent use.
type Detector struct {
	model         *model
	frontend      *mwwfront.Frontend
	audio         [mwwfront.SampleRate * mwwfront.WindowMs / 1000]int16
	held, step    int
	input         []int8
	stride        int
	recent        []uint8
	index, ignore int
	cutoff        uint8
}

// New loads an MNB1 model serialized by Voice Incubator. stepMS must be 1..30,
// window 1..255, and cutoff a finite probability in [0,1]. Cutoff is quantized
// with firmware's float32 round-to-nearest rule. Blob storage may be reused
// immediately after New returns. Models are limited to 512 KiB and 65535 arena
// bytes, as on the device; every executable view and array is bounds checked.
func New(blob []byte, stepMS, window int, cutoff float64) (*Detector, error) {
	if stepMS < 1 || stepMS > mwwfront.WindowMs || window < 1 || window > 255 || math.IsNaN(cutoff) || cutoff < 0 || cutoff > 1 {
		return nil, fmt.Errorf("mixednet: invalid detector settings")
	}
	m, err := parse(blob)
	if err != nil {
		return nil, err
	}
	d := &Detector{model: m, frontend: mwwfront.New(stepMS), step: mwwfront.SampleRate * stepMS / 1000, input: make([]int8, m.inputFrames*featureSize), recent: make([]uint8, window), cutoff: uint8(float32(float32(cutoff)*255) + 0.5)}
	d.Reset()
	return d, nil
}

// Reset forgets all audio, frontend estimates, model state and scores and
// reinstates firmware's initial guard of 100 quiet feature slices.
func (d *Detector) Reset() {
	d.model.reset()
	d.frontend.Reset()
	d.held, d.stride, d.index = 0, 0, 0
	clear(d.audio[:])
	clear(d.input)
	d.resetProbabilities()
}

func (d *Detector) resetProbabilities() {
	clear(d.recent)
	d.ignore = -100
}

func (d *Detector) summarize() (average, peak uint8) {
	sum := 0
	for _, p := range d.recent {
		sum += int(p)
		peak = max(peak, p)
	}
	return uint8((sum + len(d.recent)/2) / len(d.recent)), peak
}

func (d *Detector) feedFeature(features []int8) (average, peak uint8, hit bool) {
	copy(d.input[d.stride*featureSize:], features)
	d.stride++
	fresh := d.stride == d.model.inputFrames
	if fresh {
		d.stride = 0
		p := d.model.invoke(d.input)
		d.index = (d.index + 1) % len(d.recent)
		d.recent[d.index] = p
	}
	// The guard counts feature slices (not invocations), and only while quiet.
	if d.recent[d.index] < d.cutoff {
		d.ignore = min(d.ignore+1, 0)
	}
	average, peak = d.summarize()
	if fresh && d.ignore == 0 {
		sum := 0
		for _, p := range d.recent {
			sum += int(p)
		}
		hit = sum > int(d.cutoff)*len(d.recent) // deliberately strict
		if hit {
			d.resetProbabilities()
		}
	}
	return
}

// Feed consumes any number of PCM samples, retaining incomplete feature windows
// and model strides. Normally it returns the latest score window's rounded mean
// and maximum. If any inference detects a wake word, it returns true and the
// FIRST detection's mean and maximum, even when later samples in this call are
// quiet. A hit clears the score window and rearms the 100-quiet-slice guard, but
// preserves frontend/model history, exactly as firmware does after a detection.
func (d *Detector) Feed(samples []int16) (probability, maxProbability uint8, detected bool) {
	probability, maxProbability = d.summarize()
	for len(samples) > 0 {
		n := copy(d.audio[d.held:], samples)
		d.held += n
		samples = samples[n:]
		if d.held < len(d.audio) {
			break
		}
		var features [featureSize]int8
		mwwfront.Quantize(d.frontend.ProcessFrame(d.audio[:]), features[:])
		p, peak, hit := d.feedFeature(features[:])
		if !detected {
			probability, maxProbability = p, peak
		}
		detected = detected || hit
		d.held = copy(d.audio[:], d.audio[d.step:])
	}
	return
}
