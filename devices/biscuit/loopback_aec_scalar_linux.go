//go:build !arm64

package biscuit

func loopbackDot(weights, window *[loopbackTaps]float32) (echo float32) {
	for j, weight := range weights {
		echo += weight * window[j]
	}
	return echo
}

func loopbackAddScaled(weights, window *[loopbackTaps]float32, step float32) {
	for j, x := range window {
		weights[j] += step * x
	}
}
