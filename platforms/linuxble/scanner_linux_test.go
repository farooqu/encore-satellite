//go:build linux

package linuxble

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/core/ble"
	"golang.org/x/sys/unix"
)

func TestH4ScannerReportsAndCancellationDisable(t *testing.T) {
	fds, err := unix.Socketpair(unix.AF_UNIX, unix.SOCK_STREAM, 0)
	if err != nil {
		t.Fatal(err)
	}
	if err := unix.SetNonblock(fds[0], true); err != nil {
		t.Fatal(err)
	}
	peer := os.NewFile(uintptr(fds[1]), "H4 controller emulator")
	defer peer.Close()
	emulator := make(chan error, 1)
	go func() {
		for {
			var head [4]byte
			if _, err := io.ReadFull(peer, head[:]); err != nil {
				emulator <- err
				return
			}
			p := make([]byte, int(head[3]))
			if _, err := io.ReadFull(peer, p); err != nil {
				emulator <- err
				return
			}
			op := uint16(head[1]) | uint16(head[2])<<8
			if op == scanParameters && !bytes.Equal(p, []byte{1, 0x40, 1, 30, 0, 0, 0}) {
				emulator <- errors.New("wrong active scan duty cycle")
				return
			}
			result := []byte{}
			if op == readFeatures {
				result = []byte{0, 0, 0, 0, 0x40, 0, 0, 0}
			}
			reply := append([]byte{4, 0x0e, byte(4 + len(result)), 1, head[1], head[2], 0}, result...)
			if _, err := peer.Write(reply); err != nil {
				emulator <- err
				return
			}
			if op == scanEnable && bytes.Equal(p, []byte{0, 0}) {
				emulator <- nil
				return
			}
			if op == scanEnable && bytes.Equal(p, []byte{1, 0}) {
				// Two reports, asymmetric addresses/types/data/RSSI, fragmented H4.
				e := []byte{4, 0x3e, 27, 2, 2, 0, 1, 0x56, 0x34, 0x12, 0xee, 0xff, 0xc0, 3, 2, 1, 6, 183, 4, 0, 6, 5, 4, 3, 2, 1, 2, 1, 0xff, 214}
				_, _ = peer.Write(e[:2])
				_, _ = peer.Write(e[2:])
			}
		}
	}()
	root, cancelRoot := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelRoot()
	s, err := initialize(root, fds[0])
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	ctx, cancel := context.WithCancel(root)
	defer cancel()
	var got []ble.Advertisement
	err = s.Scan(ctx, true, func() error { return nil }, func(a ble.Advertisement) {
		a.Data = append([]byte(nil), a.Data...)
		got = append(got, a)
		if len(got) == 2 {
			cancel()
		}
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Address != 0xc0ffee123456 || got[0].AddressType != 1 || got[0].RSSI != -73 || !bytes.Equal(got[0].Data, []byte{2, 1, 6}) || got[1].Address != 0x010203040506 || got[1].AddressType != 0 || got[1].RSSI != -42 || !bytes.Equal(got[1].Data, []byte{1, 0xff}) {
		t.Fatalf("reports=%+v", got)
	}
	if err := <-emulator; err != nil {
		t.Fatal(err)
	}
}

func TestMalformedBatchEmitsNothing(t *testing.T) {
	// Valid first report followed by a truncated second report must be atomic.
	count := 0
	err := reports([]byte{4, 0x3e, 13, 2, 2, 0, 0, 6, 5, 4, 3, 2, 1, 0, 190, 4}, func(ble.Advertisement) { count++ })
	if err == nil || count != 0 {
		t.Fatalf("err=%v reports=%d", err, count)
	}
}
