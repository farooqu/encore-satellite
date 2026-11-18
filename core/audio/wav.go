package audio

import (
	"encoding/binary"
	"errors"
	"io"
)

// ReadWAV streams the advertised HA announcement format: PCM16/16kHz/mono WAV.
// It accepts ffmpeg's unknown-length pipe header, never buffers a whole response,
// and bounds both metadata and audio (three minutes). consume must copy samples
// before returning if it retains them. HTTP cancellation belongs to the caller.
func ReadWAV(r io.Reader, consume func([]int16) error) error {
	var header [12]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return err
	}
	if string(header[:4]) != "RIFF" || string(header[8:]) != "WAVE" {
		return errors.New("audio: expected RIFF WAVE")
	}
	formatted := false
	for remaining := int64(64 * 1024); remaining >= 8; {
		var chunk [8]byte
		if _, err := io.ReadFull(r, chunk[:]); err != nil {
			return err
		}
		remaining -= 8
		size := int64(binary.LittleEndian.Uint32(chunk[4:]))
		if string(chunk[:4]) == "data" {
			if !formatted {
				return errors.New("audio: WAV data before format")
			}
			const maxBytes = 3 * 60 * SampleRate * 2
			unknown := size == 0xffffffff
			if !unknown && (size == 0 || size%2 != 0 || size > maxBytes) {
				return errors.New("audio: invalid WAV data size")
			}
			var pcm [640]byte
			var samples [320]int16
			var total int64
			for unknown || total < size {
				want := int64(len(pcm))
				if !unknown {
					want = min(want, size-total)
				}
				n, err := io.ReadFull(r, pcm[:want])
				total += int64(n)
				if total > maxBytes || n%2 != 0 {
					return errors.New("audio: invalid or excessive WAV PCM")
				}
				if err != nil && !(unknown && (err == io.EOF || err == io.ErrUnexpectedEOF)) {
					return io.ErrUnexpectedEOF
				}
				for i := 0; i < n/2; i++ {
					samples[i] = int16(binary.LittleEndian.Uint16(pcm[2*i:]))
				}
				if n > 0 {
					if err := consume(samples[:n/2]); err != nil {
						return err
					}
				}
				if err != nil {
					if total == 0 {
						return errors.New("audio: empty WAV")
					}
					return nil
				}
			}
			return nil
		}
		padded := size + size%2
		if padded > remaining {
			return errors.New("audio: excessive WAV metadata")
		}
		remaining -= padded
		if string(chunk[:4]) == "fmt " {
			if formatted || size < 16 || size > 40 {
				return errors.New("audio: invalid WAV format chunk")
			}
			var format [40]byte
			if _, err := io.ReadFull(r, format[:size]); err != nil {
				return err
			}
			if binary.LittleEndian.Uint16(format[:]) != 1 || binary.LittleEndian.Uint16(format[2:]) != 1 ||
				binary.LittleEndian.Uint32(format[4:]) != SampleRate || binary.LittleEndian.Uint32(format[8:]) != SampleRate*2 ||
				binary.LittleEndian.Uint16(format[12:]) != 2 || binary.LittleEndian.Uint16(format[14:]) != 16 {
				return errors.New("audio: expected PCM16/16kHz/mono WAV")
			}
			formatted = true
			if size%2 == 0 {
				continue
			}
			padded = 1
		}
		if _, err := io.CopyN(io.Discard, r, padded); err != nil {
			return err
		}
	}
	return errors.New("audio: WAV data chunk missing")
}
