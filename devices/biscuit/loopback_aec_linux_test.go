package biscuit

import (
	"math"
	"math/rand/v2"
	"slices"
	"testing"
)

func TestLoopbackAECEchoAndFrozenDoubleTalk(t *testing.T) {
	const block, total = 320, 32000
	rng := rand.New(rand.NewPCG(8, 71))
	render := make([]int, total)
	for i := range render {
		render[i] = rng.IntN(8001) - 4000
	}
	for _, lag := range []int{168, 892} {
		var a loopbackAEC
		var echoEnergy, residualEnergy, speechEnergy, doubleTalkError float64
		for start := 0; start < total; start += block {
			var packed [block * 27]byte
			var mic, near [block]int16
			for j := range mic {
				i := start + j
				x := int32(render[i] * 256)
				for k := range 3 {
					packed[j*27+21+k] = byte(x >> (8 * k))
					// Adjacent lane is unrelated; picking it does not cancel.
					packed[j*27+24+k] = byte(rng.Uint32())
				}
				if i >= lag+97 {
					mic[j] = int16(render[i-lag]/2 - render[i-lag-97]/4)
				}
				if start >= 24000 {
					near[j] = int16(4000 * math.Sin(float64(i)*0.17))
				}
				if start >= 16000 && start < 24000 {
					echoEnergy += float64(mic[j]) * float64(mic[j])
				}
				mic[j] += near[j]
			}
			a.process(packed[:], mic[:], start < 24000)
			for j, sample := range mic {
				if start >= 16000 && start < 24000 {
					residualEnergy += float64(sample) * float64(sample)
				} else if start >= 24000 {
					e := float64(sample) - float64(near[j])
					doubleTalkError += e * e
					speechEnergy += float64(near[j]) * float64(near[j])
				}
			}
		}
		erle := 10 * math.Log10(echoEnergy/residualEnergy)
		nearSNR := 10 * math.Log10(speechEnergy/doubleTalkError)
		t.Logf("lag=%d: synthetic ERLE %.1f dB, frozen double-talk speech SNR %.1f dB", lag, erle, nearSNR)
		if erle < 12 || nearSNR < 20 {
			t.Fatalf("cancelled echo or retained near speech poorly: %.1f / %.1f dB", erle, nearSNR)
		}
		for range 4 { // let the last co-captured reference leave the filter history
			a.process(make([]byte, block*27), make([]int16, block), false)
		}
		nearOnly := []int16{-16001, -771, 16007}
		want := slices.Clone(nearOnly)
		otherLane := make([]byte, len(nearOnly)*27)
		for i := range nearOnly {
			otherLane[27*i+26] = 0x40
		}
		a.process(otherLane, nearOnly, true)
		if !slices.Equal(nearOnly, want) {
			t.Fatalf("silent lane 7 changed near-only speech: got %v, want %v", nearOnly, want)
		}
	}
}

func BenchmarkLoopbackAEC(b *testing.B) {
	for _, name := range []string{"quiet", "active"} {
		b.Run(name, func(b *testing.B) {
			var a loopbackAEC
			var packed [320 * 27]byte
			var mic [320]int16
			if name == "active" {
				rng := rand.New(rand.NewPCG(3, 61))
				for i := range mic {
					packed[i*27+23] = byte(rng.IntN(64) - 32)
				}
			}
			b.ReportAllocs()
			for b.Loop() {
				a.process(packed[:], mic[:], true)
			}
		})
	}
}
