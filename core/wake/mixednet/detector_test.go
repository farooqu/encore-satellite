package mixednet

import (
	"math"
	"os"
	"reflect"
	"strconv"
	"testing"
)

func newTestDetector(t *testing.T, frames, window int, cutoff float64) *Detector {
	t.Helper()
	d, err := New(passthroughBlob(frames), 10, window, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	return d
}

func TestSettings(t *testing.T) {
	for _, tt := range []struct {
		step, window int
		cutoff       float64
	}{
		{0, 5, .85}, {-1, 5, .85}, {31, 5, .85}, {10, 0, .85}, {10, 256, .85},
		{10, 5, -.1}, {10, 5, 1.01}, {10, 5, math.NaN()}, {10, 5, math.Inf(1)},
	} {
		if _, err := New(passthroughBlob(1), tt.step, tt.window, tt.cutoff); err == nil {
			t.Errorf("accepted settings %+v", tt)
		}
	}
	for _, tt := range []struct {
		cutoff float64
		want   uint8
	}{{0, 0}, {.5, 128}, {.85, 217}, {1, 255}} {
		d := newTestDetector(t, 1, 5, tt.cutoff)
		if d.cutoff != tt.want {
			t.Errorf("cutoff %f = %d, want %d", tt.cutoff, d.cutoff, tt.want)
		}
	}
}

func TestWarmupThresholdWindowAndRearm(t *testing.T) {
	d := newTestDetector(t, 1, 3, .5)
	var frame [40]int8
	frame[0] = 127 // probability 255, not quiet: the guard must freeze.
	for i := 0; i < 150; i++ {
		if _, _, hit := d.feedFeature(frame[:]); hit {
			t.Fatal("detected during warmup")
		}
	}
	if d.ignore != -100 {
		t.Fatalf("loud slices advanced guard to %d", d.ignore)
	}
	frame[0] = -128
	for i := 0; i < 99; i++ {
		d.feedFeature(frame[:])
	}
	if d.ignore != -1 {
		t.Fatalf("after 99 quiet slices guard = %d", d.ignore)
	}
	d.feedFeature(frame[:])
	if d.ignore != 0 {
		t.Fatal("100 quiet slices did not clear guard")
	}
	frame[0] = 0 // 128 exactly: cutoff comparison is strict, not >=.
	for i := 0; i < 4; i++ {
		if _, _, hit := d.feedFeature(frame[:]); hit {
			t.Fatal("detected at equal cutoff")
		}
	}
	frame[0] = 1 // scores [128,128,129], rounded mean 128 but sum > 384.
	if p, peak, hit := d.feedFeature(frame[:]); !hit || p != 128 || peak != 129 {
		t.Fatalf("strict sum/rounded summary = %d,%d,%v", p, peak, hit)
	}
	if d.ignore != -100 {
		t.Fatal("hit did not rearm guard")
	}
	if p, peak := d.summarize(); p != 0 || peak != 0 {
		t.Fatal("hit did not clear score window")
	}
	if d.model.buffers[0].values[0] != 1 {
		t.Fatal("hit reset executor history")
	}
}

func TestGuardCountsFeaturesAndOnlyFreshStridesDetect(t *testing.T) {
	d := newTestDetector(t, 3, 1, .5)
	var frame [40]int8
	frame[0] = -128
	for i := 0; i < 100; i++ {
		d.feedFeature(frame[:])
	}
	if d.ignore != 0 || d.stride != 1 {
		t.Fatalf("guard/stride = %d/%d", d.ignore, d.stride)
	}
	d.feedFeature(frame[:])
	d.feedFeature(frame[:]) // complete the quiet stride
	frame[0] = 127
	for i := 0; i < 2; i++ {
		if _, _, hit := d.feedFeature(frame[:]); hit {
			t.Fatal("detected without a fresh inference")
		}
	}
	if p, peak, hit := d.feedFeature(frame[:]); !hit || p != 255 || peak != 255 {
		t.Fatalf("completed stride = %d,%d,%v", p, peak, hit)
	}
}

func TestCutoffEndpointsMatchFirmware(t *testing.T) {
	for _, cutoff := range []float64{0, 1} {
		d := newTestDetector(t, 1, 1, cutoff)
		var frame [40]int8
		frame[0] = -128
		for i := 0; i < 101; i++ {
			d.feedFeature(frame[:])
		}
		frame[0] = 127
		if _, _, hit := d.feedFeature(frame[:]); hit {
			t.Fatalf("cutoff %v detected", cutoff)
		}
		// Firmware never warms up at cutoff 0: no uint8 score is < 0.
		if cutoff == 0 && d.ignore != -100 {
			t.Fatal("zero cutoff guard differs from firmware")
		}
	}
}

func TestPCMStreamingAndReset(t *testing.T) {
	blob, err := os.ReadFile("testdata/pooled.mnb")
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range []int{1, 10, 20, 30} {
		t.Run(strconv.Itoa(step)+"ms", func(t *testing.T) {
			whole, err := New(blob, step, 5, .85)
			if err != nil {
				t.Fatal(err)
			}
			split, _ := New(blob, step, 5, .85)
			audio := make([]int16, 32000+137)
			seed := uint32(37)
			for i := range audio {
				seed = seed*1664525 + 1013904223
				audio[i] = int16(seed >> 16)
			}
			p, peak, hit := whole.Feed(audio)
			var firstP, firstPeak, lastP, lastPeak uint8
			var splitHit bool
			for off := 0; off < len(audio); {
				n := min(1+(off*17+79)%997, len(audio)-off)
				var h bool
				lastP, lastPeak, h = split.Feed(audio[off : off+n])
				if h && !splitHit {
					firstP, firstPeak = lastP, lastPeak
				}
				splitHit = splitHit || h
				off += n
			}
			if !reflect.DeepEqual(whole, split) {
				t.Fatal("chunk boundaries changed streaming state")
			}
			if splitHit != hit {
				t.Fatal("chunk boundaries changed detection")
			}
			if hit {
				lastP, lastPeak = firstP, firstPeak
			}
			if p != lastP || peak != lastPeak {
				t.Fatal("chunk boundaries changed summary")
			}
			whole.Reset()
			fresh, _ := New(blob, step, 5, .85)
			wp, wm, wh := whole.Feed(audio[:7231])
			fp, fm, fh := fresh.Feed(audio[:7231])
			if wp != fp || wm != fm || wh != fh || !reflect.DeepEqual(whole, fresh) {
				t.Fatal("Reset differs from fresh detector")
			}
		})
	}
}

func TestPCMFeatureTiming(t *testing.T) {
	d := newTestDetector(t, 3, 1, .5)
	d.Feed(make([]int16, 479))
	if d.stride != 0 || d.ignore != -100 {
		t.Fatal("feature emitted before 30 ms")
	}
	d.Feed([]int16{0})
	if d.stride != 1 || d.ignore != -99 {
		t.Fatal("first feature not emitted at 30 ms")
	}
	d.Feed(make([]int16, 319))
	if d.stride != 2 {
		t.Fatal("step timing wrong")
	}
	d.Feed([]int16{0})
	if d.stride != 0 || d.ignore != -97 {
		t.Fatal("inference stride timing wrong")
	}
}

func TestChunkRetainsHitAndItsScores(t *testing.T) {
	// A one-shot synthetic model: copy initial -128 to its logit, then replace
	// the source with +127 so all later invocations are quiet. This isolates
	// chunk latching from the separately tested frontend and warmup.
	blob := encodeModel([]buffer{{frames: 1, channels: 1, init: -128}, {frames: 1, channels: 1, init: 127}, {frames: 1, channels: 1}},
		[]operation{{kind: copyOp, src: 0, dst: 2, nt: 1, nc: 1, stride: 1}, {kind: copyOp, src: 1, dst: 0, nt: 1, nc: 1, stride: 1}}, nil, 1, 2)
	blob[len(blob)-256+128] = 255
	blob[len(blob)-256+127] = 0
	d, err := New(blob, 10, 1, .5)
	if err != nil {
		t.Fatal(err)
	}
	d.ignore = 0
	p, peak, hit := d.Feed(make([]int16, 16000))
	if !hit || p != 255 || peak != 255 {
		t.Fatalf("chunk lost first hit: %d,%d,%v", p, peak, hit)
	}
	if p, peak := d.summarize(); p != 0 || peak != 0 {
		t.Fatal("model did not go quiet later in the same chunk")
	}
}
