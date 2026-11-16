//go:build linux

package linuxble

import (
	"context"
	"time"
	"unsafe"

	"golang.org/x/sys/unix"

	"github.com/farooqu/encore-satellite/core/ble"
)

// rawSocket observes HCI events beside BlueZ. It never resets or configures
// the controller. Filter ABI is Linux hci_ufilter (uint32 masks on ARM too).
func rawSocket(index uint16) (int, error) {
	fd, err := unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_RAW|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, unix.BTPROTO_HCI)
	if err != nil {
		return -1, err
	}
	filter := struct {
		Type   uint32
		Event  [2]uint32
		Opcode uint16
	}{Type: 1 << 4, Event: [2]uint32{0xffffffff, 0xffffffff}}
	_, _, errno := unix.Syscall6(unix.SYS_SETSOCKOPT, uintptr(fd), unix.SOL_HCI, 2, uintptr(unsafe.Pointer(&filter)), unsafe.Sizeof(filter), 0)
	if errno != 0 {
		_ = unix.Close(fd)
		return -1, errno
	}
	if err := unix.Bind(fd, &unix.SockaddrHCI{Dev: index, Channel: unix.HCI_CHANNEL_RAW}); err != nil {
		_ = unix.Close(fd)
		return -1, err
	}
	return fd, nil
}

// Monitor receives actual, unmodified advertising bytes while BlueZ owns the
// controller and discovery. start runs after the capture socket is installed,
// so the first advertisement is not lost. Cancellation never changes BlueZ's
// scan settings; its caller owns StartDiscovery/StopDiscovery.
func Monitor(ctx context.Context, index uint16, start func() error, found func(ble.Advertisement)) error {
	fd, err := rawSocket(index)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	s := &Scanner{fd: fd}
	if err := start(); err != nil {
		return err
	}
	for {
		e, err := s.event(ctx)
		if err != nil {
			return err
		}
		if err := reports(e, found); err != nil {
			return err
		}
	}
}

// OfferKernel supplies legacy connectable advertising on a kernel (e.g. 3.18)
// lacking BlueZ's Add Advertising management command. BlueZ must already have
// registered the GATT application. No scan, reset, power cycle or ACL traffic
// is issued here. The caller must stop the proxy first and serialize offers.
func OfferKernel(ctx context.Context, index uint16, advertisement, scanResponse []byte, updates <-chan []byte, ready func() error) (err error) {
	if len(advertisement) > 31 || len(scanResponse) > 31 {
		return unix.EINVAL
	}
	fd, err := rawSocket(index)
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	s := &Scanner{fd: fd}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		if _, disableErr := s.command(cleanup, 0x200a, []byte{0}); disableErr != nil {
			err = disableErr // Cleanup failure must not look like a ready/success probe.
		}
	}()
	params := []byte{160, 0, 160, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 7, 0}
	data := func(b []byte) []byte { r := make([]byte, 32); r[0] = byte(len(b)); copy(r[1:], b); return r }
	for _, c := range []struct {
		op uint16
		p  []byte
	}{{0x200a, []byte{0}}, {0x2006, params}, {0x2008, data(advertisement)}, {0x2009, data(scanResponse)}, {0x200a, []byte{1}}} {
		if _, err := s.command(ctx, c.op, c.p); err != nil {
			return err
		}
	}
	if err := ready(); err != nil {
		return err
	}
	for {
		select {
		case b := <-updates:
			if len(b) > 31 {
				return unix.EINVAL
			}
			if _, err := s.command(ctx, 0x2008, data(b)); err != nil {
				return err
			}
		default:
		}
		poll, cancel := context.WithTimeout(ctx, 100*time.Millisecond)
		e, err := s.event(poll)
		cancel()
		if err == context.DeadlineExceeded && ctx.Err() == nil {
			continue
		}
		if err != nil {
			return err
		}
		if e[1] == 5 { // Controller ends connectable advertising on connection.
			if _, err := s.command(ctx, 0x200a, []byte{1}); err != nil {
				return err
			}
		}
	}
}
