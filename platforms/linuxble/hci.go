// HCI event framing and legacy advertising reports adapted from TECHO5 at
// 2b091439765d5f881de4b89255b8cf0637d80cba. See licenses/Techo5-MIT.txt.
package linuxble

import (
	"encoding/binary"
	"errors"
	"fmt"

	"github.com/farooqu/encore-satellite/core/ble"
)

const (
	reset          = 0x0c03
	eventMask      = 0x0c01
	readFeatures   = 0x1003
	leEventMask    = 0x2001
	scanParameters = 0x200b
	scanEnable     = 0x200c
)

// nextEvent keeps at most one incomplete H4 event. Only events are expected:
// this scanner never opens an ACL connection or advertises a connectable role.
func nextEvent(b []byte) (event, rest []byte, err error) {
	if len(b) == 0 {
		return nil, b, nil
	}
	if b[0] != 4 {
		return nil, nil, fmt.Errorf("linuxble: unexpected H4 packet type 0x%02x", b[0])
	}
	if len(b) < 3 || len(b) < 3+int(b[2]) {
		return nil, b, nil
	}
	n := 3 + int(b[2])
	return b[:n], b[n:], nil
}

func commandReply(event []byte, opcode uint16) (data []byte, done bool, err error) {
	switch event[1] {
	case 0x0e: // Command Complete: credits, opcode, status, return parameters
		if len(event) < 7 {
			return nil, false, errors.New("linuxble: short Command Complete")
		}
		if binary.LittleEndian.Uint16(event[4:6]) == opcode {
			if event[6] != 0 {
				return nil, true, fmt.Errorf("linuxble: command 0x%04x status 0x%02x", opcode, event[6])
			}
			return event[7:], true, nil
		}
	case 0x0f: // Command Status is not completion of these synchronous commands.
		if len(event) < 7 {
			return nil, false, errors.New("linuxble: short Command Status")
		}
		if binary.LittleEndian.Uint16(event[5:7]) == opcode && event[3] != 0 {
			return nil, true, fmt.Errorf("linuxble: command 0x%04x status 0x%02x", opcode, event[3])
		}
	}
	return nil, false, nil
}

func reports(event []byte, found func(ble.Advertisement)) error {
	if event[1] != 0x3e || len(event) < 4 || event[3] != 2 {
		return nil // Not an LE legacy advertising report.
	}
	p := event[4:]
	if len(p) == 0 {
		return errors.New("linuxble: missing report count")
	}
	count, p := int(p[0]), p[1:]
	// Validate the entire batch before emitting any of it.
	check := p
	for range count {
		if len(check) < 10 || check[0] > 4 || check[1] > 1 || check[8] > 31 || len(check) < 10+int(check[8]) {
			return errors.New("linuxble: invalid legacy advertising report")
		}
		check = check[10+int(check[8]):]
	}
	if len(check) != 0 {
		return errors.New("linuxble: trailing advertising report bytes")
	}
	for range count {
		n := int(p[8])
		var address uint64
		for i := 7; i >= 2; i-- {
			address = address<<8 | uint64(p[i])
		}
		found(ble.Advertisement{Address: address, AddressType: p[1], RSSI: int8(p[9+n]), Data: p[9 : 9+n]})
		p = p[10+n:]
	}
	return nil
}
