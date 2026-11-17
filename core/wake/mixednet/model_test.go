package mixednet

import (
	"encoding/binary"
	"math"
	"os"
	"reflect"
	"testing"
)

// encodeModel writes the wire layout independently of the parser, including
// alignment and NULL offsets. Small models make every executor op observable.
func encodeModel(buffers []buffer, ops []operation, shifts []byte, frames, logit int) []byte {
	put16 := binary.LittleEndian.PutUint16
	put32 := binary.LittleEndian.PutUint32
	tail := 32 + len(buffers)*10 + len(ops)*52 + len(shifts)
	start := (tail+3)&^3 + 256
	blob := make([]byte, start)
	copy(blob, "MNB1")
	put16(blob[4:], 1)
	put16(blob[8:], uint16(len(buffers)))
	put16(blob[10:], uint16(len(ops)))
	put16(blob[12:], uint16(len(shifts)))
	blob[14], blob[15], blob[16] = byte(frames), byte(logit), featureSize
	arena := 0
	for i, b := range buffers {
		p := blob[32+i*10:]
		for j, n := range []int{b.frames, b.channels, b.keep, b.push} {
			put16(p[j*2:], uint16(n))
		}
		p[8] = byte(b.init)
		arena += b.frames * b.channels
	}
	put32(blob[20:], uint32(arena))
	// An intentionally simple signed-logit probability mapping.
	for i := 0; i < 256; i++ {
		blob[start-256+i] = byte(int(int8(byte(i))) + 128)
	}
	var data []byte
	place := func(raw []byte) uint32 {
		if len(raw) == 0 {
			return math.MaxUint32
		}
		for len(data)%4 != 0 {
			data = append(data, 0)
		}
		off := len(data)
		data = append(data, raw...)
		return uint32(off)
	}
	ints := func(a []int32) []byte {
		p := make([]byte, len(a)*4)
		for i, n := range a {
			put32(p[i*4:], uint32(n))
		}
		return p
	}
	for i, o := range ops {
		p := blob[32+len(buffers)*10+i*52:]
		p[0], p[1], p[2] = byte(o.kind), byte(o.src), byte(o.dst)
		for j, n := range []int{o.t0, o.c0, o.nt, o.nc, o.stride, o.off, o.channels} {
			put16(p[4+j*2:], uint16(n))
		}
		for j, n := range []int32{o.inOffset, o.outOffset, o.actMin, o.actMax} {
			put32(p[20+j*4:], uint32(n))
		}
		w := make([]byte, len(o.weights))
		for j, n := range o.weights {
			w[j] = byte(n)
		}
		for j, raw := range [][]byte{w, ints(o.bias), ints(o.mult), ints(o.shift)} {
			put32(p[36+j*4:], place(raw))
		}
	}
	copy(blob[tail-len(shifts):], shifts)
	for len(data)%4 != 0 {
		data = append(data, 0)
	}
	put32(blob[24:], uint32(len(data)))
	return append(blob, data...)
}

func passthroughBlob(frames int) []byte {
	return encodeModel([]buffer{{frames: frames, channels: 40, push: frames}}, []operation{{kind: ringPush, src: 255, dst: 0, nt: frames, nc: 40, stride: 40}}, nil, frames, 0)
}

func TestAllOperations(t *testing.T) {
	bs := []buffer{
		{frames: 3, channels: 2, keep: 2, push: 1, init: -10},
		{frames: 1, channels: 2}, {frames: 1, channels: 2}, {frames: 1, channels: 2},
		{frames: 2, channels: 2, keep: 1, push: 1, init: -7}, {frames: 1, channels: 1},
	}
	ops := []operation{
		{kind: ringPush, src: 255, dst: 0, nt: 1, nc: 2, stride: 2},
		{kind: maxPool, src: 0, dst: 1, nt: 3, nc: 2, stride: 2, actMin: -8, actMax: 8},
		{kind: copyOp, src: 1, dst: 2, nt: 1, nc: 2, stride: 2},
		{kind: depthwise, src: 0, dst: 3, nt: 3, nc: 2, stride: 2, channels: 2, actMin: -128, actMax: 127, weights: []int8{1, 2, 3, 4, 5, 6}, bias: []int32{0, 1}, mult: []int32{1 << 30, 1 << 30}, shift: []int32{0, -1}},
		{kind: ringPush, src: 2, dst: 4, nt: 1, nc: 2, stride: 2},
		{kind: dense, src: 3, dst: 5, nt: 1, nc: 2, stride: 2, channels: 1, actMin: -128, actMax: 127, weights: []int8{1, -1}, bias: []int32{3}, mult: []int32{1 << 30}, shift: []int32{1}},
	}
	m, err := parse(encodeModel(bs, ops, []byte{0, 4}, 1, 5))
	if err != nil {
		t.Fatal(err)
	}
	features := make([]int8, 40)
	features[0], features[1] = 12, -6
	// Depthwise: [20,-95] -> [10,-24]; dense: 10-(-24)+3 = 37.
	if got := m.invoke(features); got != 165 {
		t.Fatalf("probability = %d, want 165", got)
	}
	wants := map[int][]int8{0: {-10, -10, 12, -6, 12, -6}, 1: {8, -6}, 2: {8, -6}, 3: {10, -24}, 4: {8, -6, 8, -6}, 5: {37}}
	for i, want := range wants {
		if !reflect.DeepEqual(m.buffers[i].values, want) {
			t.Errorf("buffer %d = %v, want %v", i, m.buffers[i].values, want)
		}
	}
	m.reset()
	if !reflect.DeepEqual(m.buffers[0].values, []int8{-10, -10, -10, -10, -10, -10}) {
		t.Fatal("reset did not restore init")
	}
}

func TestGemmlowpRounding(t *testing.T) {
	for _, tt := range []struct{ x, m, s, want int32 }{
		{1, 1 << 30, 0, 1}, {-1, 1 << 30, 0, 0}, {-3, 1 << 30, 0, -1},
		{1, 1 << 30, -1, 1}, {-3, 1 << 30, -1, -1}, {3, 1 << 30, -1, 1},
		{3, 1 << 30, 1, 3}, {-3, 1 << 30, 1, -3},
		{math.MinInt32, math.MinInt32, 0, math.MaxInt32},
		{math.MaxInt32, math.MaxInt32, -31, 1}, {math.MinInt32, math.MaxInt32, -31, -1},
	} {
		if got := requantize(tt.x, tt.m, tt.s); got != tt.want {
			t.Errorf("requantize(%d,%d,%d) = %d, want %d", tt.x, tt.m, tt.s, got, tt.want)
		}
	}
}

func TestPooledFirmwareReference(t *testing.T) {
	blob, err := os.ReadFile("testdata/pooled.mnb")
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile("testdata/pooled.golden")
	if err != nil {
		t.Fatal(err)
	}
	m, err := parse(blob)
	if err != nil {
		t.Fatal(err)
	}
	clear(blob) // The caller's bytes must not remain live model storage.
	features := make([]int8, m.inputFrames*40)
	seed := uint32(1234)
	for i := 0; i < 3000; i++ {
		for j := range features {
			seed = seed*1664525 + 1013904223
			features[j] = int8(seed >> 24)
			if i < 3 {
				features[j] = []int8{0, -128, 127}[i]
			}
		}
		p := m.invoke(features)
		logit := uint8(m.buffers[m.logit].values[0])
		if len(want) != 6000 {
			t.Fatal("bad golden length")
		}
		if p != want[2*i] || logit != want[2*i+1] {
			t.Fatalf("step %d: probability/logit %d/%d, firmware %d/%d", i, p, int8(logit), want[2*i], int8(want[2*i+1]))
		}
	}
}

func TestMalformedModels(t *testing.T) {
	valid := passthroughBlob(1)
	for n := 0; n < len(valid); n++ {
		if _, err := parse(valid[:n]); err == nil {
			t.Fatalf("accepted truncation %d", n)
		}
	}
	for _, tt := range []struct {
		name  string
		off   int
		value uint32
		width int
	}{
		{"magic", 0, 0, 4}, {"version", 4, 2, 2}, {"buffers", 8, 255, 2}, {"ops", 10, 256, 2},
		{"shifts", 12, 256, 2}, {"frames", 14, 0, 1}, {"logit", 15, 1, 1}, {"features", 16, 39, 1},
		{"arena", 20, 65536, 4}, {"data", 24, 1, 4}, {"buffer frames", 32, 65535, 2},
		{"keep", 36, 1, 2}, {"push", 38, 2, 2}, {"op", 42, 5, 1}, {"source", 43, 1, 1},
		{"destination", 44, 1, 1}, {"rows", 50, 2, 2}, {"columns", 52, 41, 2},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := append([]byte(nil), valid...)
			switch tt.width {
			case 1:
				b[tt.off] = byte(tt.value)
			case 2:
				binary.LittleEndian.PutUint16(b[tt.off:], uint16(tt.value))
			case 4:
				binary.LittleEndian.PutUint32(b[tt.off:], tt.value)
			}
			if _, err := parse(b); err == nil {
				t.Fatal("accepted malformed model")
			}
		})
	}
	if _, err := parse(make([]byte, maxBlobSize+1)); err == nil {
		t.Fatal("accepted oversized model")
	}
}

func FuzzParseAndExecute(f *testing.F) {
	f.Add(passthroughBlob(1))
	if b, err := os.ReadFile("testdata/pooled.mnb"); err == nil {
		f.Add(b)
	}
	f.Fuzz(func(t *testing.T, b []byte) {
		m, err := parse(b)
		if err == nil {
			m.invoke(make([]int8, m.inputFrames*40))
			m.reset()
		}
	})
}

func TestMalformedViewsAndArrays(t *testing.T) {
	blob := encodeModel([]buffer{{frames: 1, channels: 40, push: 1}, {frames: 1, channels: 1}}, []operation{
		{kind: ringPush, src: 255, dst: 0, nt: 1, nc: 40, stride: 40},
		{kind: dense, src: 0, dst: 1, nt: 1, nc: 40, stride: 40, channels: 1, actMin: -128, actMax: 127, weights: make([]int8, 40), bias: []int32{0}, mult: []int32{1 << 30}, shift: []int32{0}},
	}, []byte{0}, 1, 1)
	if _, err := parse(blob); err != nil {
		t.Fatal(err)
	}
	const op = 32 + 2*10 + 52
	for _, tt := range []struct {
		name  string
		field int
		value uint32
		width int
	}{
		{"external arithmetic", 1, 255, 1}, {"source row", 4, 65535, 2}, {"dense channel offset", 6, 1, 2},
		{"source length", 8, 2, 2}, {"source stride", 12, 0, 2}, {"destination offset", 14, 1, 2},
		{"output channels", 16, 2, 2}, {"activation min", 28, 128, 4}, {"activation max", 32, 128, 4},
		{"null weights", 36, math.MaxUint32, 4}, {"short weights", 36, 48, 4},
		{"unaligned bias", 40, 41, 4}, {"short bias", 40, 52, 4},
		{"null mult", 44, math.MaxUint32, 4}, {"null shift", 48, math.MaxUint32, 4},
	} {
		t.Run(tt.name, func(t *testing.T) {
			b := append([]byte(nil), blob...)
			p := b[op+tt.field:]
			switch tt.width {
			case 1:
				p[0] = byte(tt.value)
			case 2:
				binary.LittleEndian.PutUint16(p, uint16(tt.value))
			case 4:
				binary.LittleEndian.PutUint32(p, tt.value)
			}
			if _, err := parse(b); err == nil {
				t.Fatal("accepted malformed op")
			}
		})
	}
	for _, s := range []int32{-32, 31, math.MinInt32, math.MaxInt32} {
		b := append([]byte(nil), blob...)
		binary.LittleEndian.PutUint32(b[len(b)-4:], uint32(s))
		if _, err := parse(b); err == nil {
			t.Fatalf("accepted shift %d", s)
		}
	}
	b := append([]byte(nil), blob...)
	b[32+2*10+2*52] = 2
	if _, err := parse(b); err == nil {
		t.Fatal("accepted out-of-range shift buffer")
	}
}
