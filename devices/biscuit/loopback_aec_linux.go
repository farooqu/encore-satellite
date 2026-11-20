package biscuit

import "math"

const (
	loopbackLane       = 7    // Techo5 precedent; the effective channel on Linux 6.1 is unqualified.
	loopbackTaps       = 1024 // 64 ms at 16 kHz, including the reference-to-mic delay.
	loopbackQuietPower = 1e-6 * loopbackTaps
)

// loopbackAEC is owned only by capture. It predicts the speaker component of
// the fixed mic average from a reference in the same packed capture frames.
// The doubled history keeps the newest 1024 samples contiguous across wrap.
type loopbackAEC struct {
	weights [loopbackTaps]float32
	history [2 * loopbackTaps]float32
	next    int
	power   float64
}

// process replaces the mic mix in place. Disabling learning still subtracts
// the predicted echo during a listening turn. Quiet history bypasses exactly,
// but does not forget the learned path between replies.
func (a *loopbackAEC) process(packed []byte, mic []int16, learn bool) {
	for i, sample := range mic {
		x := float32(packed24(packed[27*i+3*loopbackLane:])) * (1.0 / (1 << 23))
		old := a.history[a.next]
		a.history[a.next], a.history[a.next+loopbackTaps] = x, x
		a.next = (a.next + 1) % loopbackTaps
		a.power += float64(x)*float64(x) - float64(old)*float64(old)
		if a.power <= loopbackQuietPower {
			continue
		}

		window := (*[loopbackTaps]float32)(a.history[a.next:])
		var echo float32
		for j, weight := range &a.weights {
			echo += weight * window[j]
		}
		residual := float32(sample) - echo
		mic[i] = int16(max(math.MinInt16, min(math.MaxInt16, residual)))
		if learn && sample != math.MinInt16 && sample != math.MaxInt16 {
			step := 0.1 * residual / float32(a.power+loopbackQuietPower)
			for j, x := range window {
				a.weights[j] += step * x
			}
		}
	}
}
