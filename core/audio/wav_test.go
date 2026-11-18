package audio_test

import (
	"bytes"
	"encoding/binary"
	"errors"
	"slices"
	"testing"
	"testing/iotest"

	"github.com/farooqu/encore-satellite/core/audio"
)

func TestReadWAVStreamsKnownAndPipeLengths(t *testing.T) {
	want := make([]int16, 1027)
	for i := range want {
		want[i] = int16(i*193 - 29001)
	}
	wav := make([]byte, 44+2*len(want))
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], uint32(len(wav)-8))
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 16000)
	binary.LittleEndian.PutUint32(wav[28:], 32000)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], uint32(2*len(want)))
	for i, sample := range want {
		binary.LittleEndian.PutUint16(wav[44+2*i:], uint16(sample))
	}
	for _, pipe := range []bool{false, true} {
		b := slices.Clone(wav)
		if pipe {
			binary.LittleEndian.PutUint32(b[4:], 0xffffffff)
			binary.LittleEndian.PutUint32(b[40:], 0xffffffff)
		}
		// Odd-length ancillary chunks have one pad byte. They are common in
		// WAV output, and must never be decoded as samples or shift fmt/data.
		b = append(append(append([]byte{}, b[:12]...), []byte{'J', 'U', 'N', 'K', 3, 0, 0, 0, 11, 72, 39, 0}...), b[12:]...)
		var got []int16
		err := audio.ReadWAV(iotest.OneByteReader(bytes.NewReader(b)), func(pcm []int16) error {
			if len(pcm) > 320 {
				t.Fatal("unbounded playback block", len(pcm))
			}
			got = append(got, pcm...)
			return nil
		})
		if err != nil || !slices.Equal(got, want) {
			t.Fatalf("pipe=%v: wrong decoded PCM: length%d/%d, err%v", pipe, len(got), len(want), err)
		}
	}
	for name, change := range map[string]func([]byte) []byte{
		"compressed": func(b []byte) []byte { b[20] = 3; return b },
		"stereo":     func(b []byte) []byte { b[22] = 2; return b },
		"wrong rate": func(b []byte) []byte { binary.LittleEndian.PutUint32(b[24:], 24000); return b },
		"wrong bits": func(b []byte) []byte { b[34] = 24; return b },
		"not WAV":    func(b []byte) []byte { b[0] = 'X'; return b },
		"truncated":  func(b []byte) []byte { return b[:len(b)-2] },
		"odd PCM":    func(b []byte) []byte { binary.LittleEndian.PutUint32(b[40:], 3); return b },
		"too long":   func(b []byte) []byte { binary.LittleEndian.PutUint32(b[40:], 180*32000+2); return b },
		"huge metadata": func(b []byte) []byte {
			copy(b[12:], "JUNK")
			binary.LittleEndian.PutUint32(b[16:], 65536)
			return b
		},
	} {
		t.Run(name, func(t *testing.T) {
			if err := audio.ReadWAV(bytes.NewReader(change(slices.Clone(wav))), func([]int16) error { return nil }); err == nil {
				t.Fatal("accepted invalid WAV")
			}
		})
	}
	stop := errors.New("cancel output")
	if err := audio.ReadWAV(bytes.NewReader(wav), func([]int16) error { return stop }); !errors.Is(err, stop) {
		t.Fatal("consumer cancellation swallowed", err)
	}
}
