// Package mixednet runs Voice Incubator's MNB1 streaming wake models in pure Go.
//
// The executor is a Go adaptation of Voice Incubator's mixednet.cpp (vendored
// from synesthesiam/mixednet-executor); loading follows mixednet.py and
// mixednet_blob.cpp. See NOTICE and LICENSE-APACHE-2.0.txt in this package.
package mixednet

import (
	"encoding/binary"
	"fmt"
)

const (
	featureSize = 40
	maxBlobSize = 512 * 1024
)

const (
	ringPush = iota
	dense
	depthwise
	copyOp
	maxPool
)

type buffer struct {
	frames, channels, keep, push int
	init                         int8
	values                       []int8
}

type operation struct {
	kind, src, dst                        int
	t0, c0, nt, nc, stride, off, channels int
	inOffset, outOffset, actMin, actMax   int32
	weights                               []int8
	bias, mult, shift                     []int32
}

type model struct {
	buffers            []buffer
	ops                []operation
	shifts             []byte
	inputFrames, logit int
	lut                [256]uint8
}

func parse(blob []byte) (*model, error) {
	fail := func(s string) (*model, error) { return nil, fmt.Errorf("mixednet: %s", s) }
	if len(blob) < 32 || len(blob) > maxBlobSize {
		return fail("invalid blob size")
	}
	u16 := func(p []byte) int { return int(binary.LittleEndian.Uint16(p)) }
	u32 := func(p []byte) uint32 { return binary.LittleEndian.Uint32(p) }
	if string(blob[:4]) != "MNB1" || u16(blob[4:]) != 1 {
		return fail("unsupported model format")
	}
	nb, no, ns := u16(blob[8:]), u16(blob[10:]), u16(blob[12:])
	if nb == 0 || nb >= 255 || no == 0 || no > 255 || ns > 255 || blob[14] == 0 || int(blob[15]) >= nb || blob[16] != featureSize {
		return fail("invalid model dimensions")
	}
	tail := 32 + nb*10 + no*52 + ns
	dataStart := (tail+3)&^3 + 256
	if dataStart > len(blob) || uint64(u32(blob[24:])) != uint64(len(blob)-dataStart) {
		return fail("invalid data section size")
	}
	m := &model{buffers: make([]buffer, nb), inputFrames: int(blob[14]), logit: int(blob[15])}
	copy(m.lut[:], blob[dataStart-256:dataStart])
	arena := 0
	for i := range m.buffers {
		p := blob[32+i*10:]
		b := &m.buffers[i]
		b.frames, b.channels, b.keep, b.push, b.init = u16(p), u16(p[2:]), u16(p[4:]), u16(p[6:]), int8(p[8])
		size := int64(b.frames) * int64(b.channels)
		if size == 0 || size > 65535 || b.keep+b.push > b.frames || int64(arena)+size > 65535 {
			return fail("invalid buffer dimensions or arena too large")
		}
		arena += int(size)
	}
	if uint32(arena) != u32(blob[20:]) {
		return fail("arena size mismatch")
	}
	// All views and complete arrays are checked before any model is executed.
	data := blob[dataStart:]
	// Decode shared storage once: repeated array offsets cannot amplify memory
	// use, and no pointers into the caller's mutable blob survive parsing.
	signed := make([]int8, len(data))
	for i, v := range data {
		signed[i] = int8(v)
	}
	words := make([]int32, len(data)/4)
	for i := range words {
		words[i] = int32(u32(data[i*4:]))
	}
	array := func(offset uint32, count, align int) ([]byte, bool) {
		end := uint64(offset) + uint64(count)
		if offset%uint32(align) != 0 || end > uint64(len(data)) {
			return nil, false
		}
		return data[int(offset):int(end)], true
	}
	ints := func(offset uint32, count int) ([]int32, bool) {
		_, ok := array(offset, count*4, 4)
		if !ok {
			return nil, false
		}
		start := int(offset) / 4
		return words[start : start+count], true
	}
	for i := 0; i < no; i++ {
		p := blob[32+nb*10+i*52:]
		o := operation{kind: int(p[0]), src: int(p[1]), dst: int(p[2]), t0: u16(p[4:]), c0: u16(p[6:]), nt: u16(p[8:]), nc: u16(p[10:]), stride: u16(p[12:]), off: u16(p[14:]), channels: u16(p[16:]), inOffset: int32(u32(p[20:])), outOffset: int32(u32(p[24:])), actMin: int32(u32(p[28:])), actMax: int32(u32(p[32:]))}
		if o.kind > maxPool || o.dst >= nb || (o.src != 255 && o.src >= nb) || o.nt == 0 || o.nc == 0 {
			return fail("invalid op dimensions or buffer index")
		}
		dst := m.buffers[o.dst]
		dstSize := int64(dst.frames) * int64(dst.channels)
		start := int64(o.t0)*int64(o.stride) + int64(o.c0)
		end := start + int64(o.nt-1)*int64(o.stride) + int64(o.nc)
		writeStart, writeSize := int64(o.off), int64(o.channels)
		switch o.kind {
		case ringPush:
			writeStart = int64(dst.keep) * int64(dst.channels)
			writeSize = int64(o.nt-1)*int64(dst.channels) + int64(o.nc)
			if o.nt > dst.push || o.nc > dst.channels {
				return fail("ring push exceeds destination")
			}
		case copyOp:
			writeSize = int64(o.nc)
			end = start + int64(o.nc) // COPY reads exactly one row.
		case dense:
			if o.c0 != 0 {
				return fail("dense source must have zero channel offset")
			}
			end = start + int64(o.nt)*int64(o.nc) // Dense reads a contiguous vector.
		case depthwise:
			if o.channels == 0 || o.channels > o.nc {
				return fail("invalid depthwise channels")
			}
		case maxPool:
			writeSize = int64(o.nc)
		}
		if o.src == 255 {
			if o.kind != ringPush || int64(o.nt)*int64(o.nc) > int64(m.inputFrames*featureSize) {
				return fail("invalid external input")
			}
			// External RING_PUSH is one contiguous memcpy, not a strided view.
			writeSize = int64(o.nt) * int64(o.nc)
		} else {
			src := m.buffers[o.src]
			// Serializer views are channel windows within non-overlapping rows.
			// Besides rejecting nonsensical geometry, this prevents tiny blobs
			// from requesting billions of repeated max-pool reads (stride 0/1).
			if o.c0+o.nc > o.stride {
				return fail("invalid source row stride")
			}
			if end > int64(src.frames)*int64(src.channels) {
				return fail("source view exceeds buffer")
			}
		}
		if writeStart+writeSize > dstSize {
			return fail("destination view exceeds buffer")
		}
		if o.kind == dense || o.kind == depthwise || o.kind == maxPool {
			if o.actMin < -128 || o.actMax > 127 || o.actMin > o.actMax {
				return fail("invalid activation range")
			}
		}
		if o.kind == dense || o.kind == depthwise {
			count := int64(o.nt) * int64(o.nc)
			if o.channels == 0 {
				return fail("empty arithmetic output")
			}
			if o.kind == dense {
				count *= int64(o.channels)
			}
			if count > maxBlobSize {
				return fail("weights too large")
			}
			weightOffset := u32(p[36:])
			_, ok := array(weightOffset, int(count), 1)
			if !ok {
				return fail("invalid weights")
			}
			o.weights = signed[int(weightOffset) : int(weightOffset)+int(count)]
			if o.bias, ok = ints(u32(p[40:]), o.channels); !ok {
				return fail("invalid bias")
			}
			if o.mult, ok = ints(u32(p[44:]), o.channels); !ok {
				return fail("invalid multiplier")
			}
			if o.shift, ok = ints(u32(p[48:]), o.channels); !ok {
				return fail("invalid shift")
			}
			for _, s := range o.shift {
				if s < -31 || s > 30 {
					return fail("quantization shift out of range")
				}
			}
		}
		m.ops = append(m.ops, o)
	}
	m.shifts = append([]byte(nil), blob[32+nb*10+no*52:tail]...)
	for _, i := range m.shifts {
		if int(i) >= nb {
			return fail("invalid shift buffer")
		}
	}
	for i := range m.buffers {
		b := &m.buffers[i]
		b.values = make([]int8, b.frames*b.channels)
	}
	m.reset()
	return m, nil
}

func (m *model) reset() {
	for _, b := range m.buffers {
		for i := range b.values {
			b.values[i] = b.init
		}
	}
}

// requantize uses gemmlowp's double rounding, as the ESP32 firmware does.
func requantize(x, multiplier, shift int32) int32 {
	if shift > 0 {
		x *= int32(1) << uint(shift)
	}
	var high int32
	if x == -1<<31 && multiplier == -1<<31 {
		high = 1<<31 - 1
	} else {
		product := int64(x) * int64(multiplier)
		nudge := int64(1 << 30)
		if product < 0 {
			nudge = 1 - (1 << 30)
		}
		high = int32((product + nudge) / (1 << 31))
	}
	if shift >= 0 {
		return high
	}
	exponent := uint(-shift)
	mask := int32((uint32(1) << exponent) - 1)
	threshold := mask >> 1
	if high < 0 {
		threshold++
	}
	rounded := high >> exponent
	if high&mask > threshold {
		rounded++
	}
	return rounded
}

func (m *model) invoke(features []int8) uint8 {
	for _, o := range m.ops {
		dst := &m.buffers[o.dst]
		if o.kind == ringPush && o.src == 255 {
			copy(dst.values[dst.keep*dst.channels:], features[:o.nt*o.nc])
			continue
		}
		src := m.buffers[o.src].values[o.t0*o.stride+o.c0:]
		if o.kind == ringPush {
			for t := 0; t < o.nt; t++ {
				copy(dst.values[(dst.keep+t)*dst.channels:], src[t*o.stride:t*o.stride+o.nc])
			}
			continue
		}
		out := dst.values[o.off:]
		switch o.kind {
		case copyOp:
			copy(out, src[:o.nc])
		case dense, depthwise:
			for c := 0; c < o.channels; c++ {
				acc := o.bias[c]
				if o.kind == dense {
					row := o.nt * o.nc
					for j, w := range o.weights[c*row : (c+1)*row] {
						acc += int32(w) * (int32(src[j]) + o.inOffset)
					}
				} else {
					for k := 0; k < o.nt; k++ {
						acc += int32(o.weights[k*o.nc+c]) * (int32(src[k*o.stride+c]) + o.inOffset)
					}
				}
				v := requantize(acc, o.mult[c], o.shift[c]) + o.outOffset
				out[c] = int8(max(o.actMin, min(o.actMax, v)))
			}
		case maxPool:
			for c := 0; c < o.nc; c++ {
				v := src[c]
				for k := 1; k < o.nt; k++ {
					v = max(v, src[k*o.stride+c])
				}
				out[c] = int8(max(o.actMin, min(o.actMax, int32(v))))
			}
		}
	}
	p := m.lut[uint8(m.buffers[m.logit].values[0])]
	for _, i := range m.shifts {
		b := &m.buffers[i]
		copy(b.values[:b.keep*b.channels], b.values[b.push*b.channels:(b.push+b.keep)*b.channels])
	}
	return p
}
