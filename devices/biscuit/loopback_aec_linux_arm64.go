package biscuit

// The NEON kernels come from Techo5's echod/internal/lib/vec; see
// ACKNOWLEDGEMENTS.md. Keep the filter policy and reference alignment in Go.
func loopbackDot(weights, window *[loopbackTaps]float32) float32 {
	return dotNEON(&weights[0], &window[0], loopbackTaps)
}

func loopbackAddScaled(weights, window *[loopbackTaps]float32, step float32) {
	axpyNEON(&weights[0], &window[0], loopbackTaps, step)
}

//go:noescape
func dotNEON(a, b *float32, n int) float32

//go:noescape
func axpyNEON(dst, x *float32, n int, gain float32)
