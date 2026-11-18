package biscuit_test

import (
	"testing"

	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/core/presentation"
	"github.com/farooqu/encore-satellite/devices/biscuit"
)

func TestPublicAveragePacked24(t *testing.T) {
	cases := []struct {
		channels [9]int32
		want     int16
	}{
		{channels: [9]int32{8388607, 8388607, 8388607, 8388607, 8388607, 8388607, 8388607}, want: 32767},
		{channels: [9]int32{-8388608, -8388608, -8388608, -8388608, -8388608, -8388608, -8388608}, want: -32768},
		{channels: [9]int32{7 * 256, 14 * 256, -21 * 256, 28 * 256, -35 * 256, 42 * 256, -49 * 256}, want: -2},
		{channels: [9]int32{8388607, -8388608, 1}, want: 0},
		{channels: [9]int32{511, 511, 511, 511}, want: 1},
		{channels: [9]int32{-511, -511, -511, -511}, want: -1},
		{channels: [9]int32{1791}, want: 0},
		{channels: [9]int32{-1791}, want: 0},
		{channels: [9]int32{1792}, want: 1},
		{channels: [9]int32{-1792}, want: -1},
		{channels: [9]int32{2688}, want: 1},
		{channels: [9]int32{-2688}, want: -1},
		{channels: [9]int32{7: 8388607}, want: 0},
		{channels: [9]int32{8: -8388608}, want: 0},
	}
	for mic := range 7 {
		// Each microphone alone must contribute exactly one seventh, with
		// asymmetric signed values and independently nonzero references.
		for _, sign := range []int16{-1, 1} {
			want := sign * int16(101+37*mic)
			channels := [9]int32{7: 8388607, 8: -1234567}
			channels[mic] = int32(want) * 7 * 256
			cases = append(cases, struct {
				channels [9]int32
				want     int16
			}{channels, want})
		}
	}
	b := make([]byte, len(cases)*27)
	for i, c := range cases {
		for ch, x := range c.channels {
			for j := range 3 {
				b[i*27+ch*3+j] = byte(x >> uint(j*8))
			}
		}
	}
	got, err := biscuit.AveragePCM(b)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != len(cases) {
		t.Fatalf("output samples: got %d want %d", len(got), len(cases))
	}
	for i, v := range got {
		if v != cases[i].want {
			t.Fatalf("frame %d channels %v: got %d want %d", i, cases[i].channels, v, cases[i].want)
		}
	}
	for _, bad := range [][]byte{nil, {1}, make([]byte, 26), make([]byte, 28), make([]byte, (audio.MaxSamples+1)*27)} {
		if _, err := biscuit.AveragePCM(bad); err == nil {
			t.Fatal("accepted malformed block")
		}
	}
	if got, err := biscuit.AveragePCM(make([]byte, audio.MaxSamples*27)); err != nil || len(got) != audio.MaxSamples {
		t.Fatalf("maximum-size block: samples=%d err=%v", len(got), err)
	}
}

func TestPublicRingPrivacyHasHighestPriority(t *testing.T) {
	for _, phase := range []string{"idle", "listening", "working", "speaking", "error", "disconnected"} {
		frame := biscuit.RingFrame(presentation.State{Muted: true, Alert: true, Phase: phase, Night: "off"}, 1, 0, biscuit.RingOptions{Brightness: 100})
		for i := range 12 {
			if got := [3]byte{frame[i*3], frame[i*3+1], frame[i*3+2]}; got != [3]byte{223, 0, 0} {
				t.Fatalf("privacy lost to %s: %v", phase, frame)
			}
		}
	}
}
