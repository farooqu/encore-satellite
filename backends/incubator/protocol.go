// Copyright (c) 2026 TECHO5 contributors
// SPDX-License-Identifier: MIT
// Framing adapted from Techo5's internal/feature/incubator/protocol.go at
// 2b091439765d5f881de4b89255b8cf0637d80cba. See licenses/Techo5-MIT.txt.
package incubator

import (
	"encoding/binary"
	"fmt"
	"io"
)

const maxPayload = 4096

func readFrame(r io.Reader) (byte, []byte, error) {
	var h [3]byte
	if _, err := io.ReadFull(r, h[:]); err != nil {
		return 0, nil, err
	}
	n := int(binary.BigEndian.Uint16(h[1:]))
	if n > maxPayload {
		return 0, nil, fmt.Errorf("incubator: frame too large: %d", n)
	}
	b := make([]byte, n)
	_, err := io.ReadFull(r, b)
	return h[0], b, err
}

func writeFrame(w io.Writer, kind byte, payload []byte) error {
	if len(payload) > maxPayload {
		return fmt.Errorf("incubator: frame too large")
	}
	b := make([]byte, 3+len(payload))
	b[0] = kind
	binary.BigEndian.PutUint16(b[1:3], uint16(len(payload)))
	copy(b[3:], payload)
	for len(b) > 0 {
		n, err := w.Write(b)
		if err != nil {
			return err
		}
		if n == 0 {
			return io.ErrShortWrite
		}
		b = b[n:]
	}
	return nil
}
