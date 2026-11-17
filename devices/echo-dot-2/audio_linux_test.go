package dot_test

import (
	"testing"

	"github.com/farooqu/encore-satellite/core/presentation"
	dot "github.com/farooqu/encore-satellite/devices/echo-dot-2"
)

func TestPublicCenterChannelPacked24Asymmetric(t *testing.T) {
	want := []int16{32767, -32768, -1, 0, 0x1234, -12345}
	b := make([]byte, len(want)*27)
	for i, v := range want {
		for ch := 0; ch < 9; ch++ {
			x := int32((ch+1)*0x12345 + i*0x321)
			if ch == 6 {
				x = int32(v)<<8 | 0xab
			}
			for j := 0; j < 3; j++ {
				b[i*27+ch*3+j] = byte(x >> uint(j*8))
			}
		}
	}
	got, err := dot.CenterPCM(b)
	if err != nil {
		t.Fatal(err)
	}
	for i, v := range got {
		if v != want[i] {
			t.Fatalf("frame %d got %d want %d", i, v, want[i])
		}
	}
	for _, bad := range [][]byte{nil, {1}, make([]byte, 26), make([]byte, 28), make([]byte, 1025*27)} {
		if _, err := dot.CenterPCM(bad); err == nil {
			t.Fatal("accepted malformed block")
		}
	}
}

func TestPublicRingPrivacyHasHighestPriority(t *testing.T) {
	for _, phase := range []string{"idle", "listening", "working", "speaking", "error", "disconnected"} {
		if got := dot.RingColor(presentation.State{Muted: true, Alert: true, Phase: phase}); got != [3]byte{32, 0, 0} {
			t.Fatalf("privacy lost to %s: %v", phase, got)
		}
	}
}
