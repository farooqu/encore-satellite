//go:build linux

// Package linuxble scans an exclusively owned Linux HCI controller. The Dot's
// MediaTek H4 node works without CONFIG_BT; ordinary controllers use Linux's
// exclusive HCI user channel, not a raw socket competing with bluetoothd.
package linuxble

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"github.com/farooqu/encore-satellite/core/ble"
)

// Config selects exactly one transport. Device is an H4 character device such
// as /dev/stpbt. Empty Device uses HCI's user channel on Index (usually hci0).
// The HCI adapter must be DOWN and not managed by bluetoothd. Device must not
// be opened by btbridge or another daemon. No automatic transport fallback.
type Config struct {
	Device string
	Index  uint16
}

type Scanner struct {
	mu     sync.Mutex // Whole scan/close, not callbacks or host voice locks.
	fd     int
	held   []byte
	ctx    context.Context
	cancel context.CancelFunc
}

var _ ble.Scanner = (*Scanner)(nil)

// Open resets and probes the exclusively owned controller. Failure must leave
// Bluetooth capabilities disabled; it must not prevent the host's USB setup or
// voice runtime from starting. The caller owns Close, even with no subscriber.
// wmtup/firmware/driver initialization must have completed before this call.
func Open(ctx context.Context, config Config) (*Scanner, error) {
	if config.Device != "" && config.Index != 0 {
		return nil, errors.New("linuxble: select a device or HCI index, not both")
	}
	var fd int
	var err error
	if config.Device != "" {
		// Refuse a node already bridged into the kernel. flock below also
		// prevents two Encore processes from owning the direct node.
		if config.Device == "/dev/stpbt" {
			if entries, _ := os.ReadDir("/sys/class/bluetooth"); len(entries) != 0 {
				return nil, errors.New("linuxble: HCI adapter exists; do not open /dev/stpbt beside btbridge")
			}
		}
		fd, err = unix.Open(config.Device, unix.O_RDWR|unix.O_NONBLOCK|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
		if err == nil {
			var stat unix.Stat_t
			err = unix.Fstat(fd, &stat)
			if err == nil && stat.Mode&unix.S_IFMT != unix.S_IFCHR {
				err = errors.New("H4 device must be a character device")
			}
			if err == nil {
				err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB)
			}
			if err != nil {
				_ = unix.Close(fd)
			}
		}
	} else {
		fd, err = unix.Socket(unix.AF_BLUETOOTH, unix.SOCK_RAW|unix.SOCK_NONBLOCK|unix.SOCK_CLOEXEC, unix.BTPROTO_HCI)
		if err == nil {
			err = unix.Bind(fd, &unix.SockaddrHCI{Dev: config.Index, Channel: unix.HCI_CHANNEL_USER})
			if err != nil {
				_ = unix.Close(fd)
			}
		}
	}
	if err != nil {
		return nil, fmt.Errorf("linuxble: open controller: %w", err)
	}
	return initialize(ctx, fd)
}

func initialize(ctx context.Context, fd int) (*Scanner, error) {
	root, cancel := context.WithCancel(ctx)
	s := &Scanner{fd: fd, ctx: root, cancel: cancel}
	if err := s.initialize(ctx); err != nil {
		_ = s.Close()
		return nil, err
	}
	return s, nil
}

func (s *Scanner) initialize(ctx context.Context) error {
	if _, err := s.command(ctx, reset, nil); err != nil {
		return err
	}
	features, err := s.command(ctx, readFeatures, nil)
	if err != nil {
		return err
	}
	if len(features) != 8 || features[4]&0x40 == 0 {
		return errors.New("linuxble: controller does not report LE support")
	}
	// Command complete/status, hardware error and LE meta events only.
	mask := make([]byte, 8)
	binary.LittleEndian.PutUint64(mask, 1<<13|1<<14|1<<15|1<<61)
	if _, err := s.command(ctx, eventMask, mask); err != nil {
		return err
	}
	_, err = s.command(ctx, leEventMask, []byte{2, 0, 0, 0, 0, 0, 0, 0})
	return err
}

// Scan uses Techo5's conservative MTK antenna budget: 18.75 ms per 200 ms
// (9.375%). Duplicates are retained for HA's presence/RSSI consumers. There
// is no advertising, pairing, GATT, adapter power cycling or audio routing.
func (s *Scanner) Scan(ctx context.Context, active bool, ready func() error, found func(ble.Advertisement)) (err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fd < 0 || s.ctx.Err() != nil {
		return errors.New("linuxble: scanner closed")
	}
	ctx, cancel := context.WithCancel(ctx)
	stop := context.AfterFunc(s.ctx, cancel)
	defer stop()
	defer cancel()
	params := []byte{0, 0x40, 0x01, 30, 0, 0, 0}
	if active {
		params[0] = 1
	}
	if _, err := s.command(ctx, scanParameters, params); err != nil {
		return err
	}
	// Even a timeout may mean the enable reached the chip. Always disable.
	defer func() {
		cleanup, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		defer cancel()
		_, disableErr := s.command(cleanup, scanEnable, []byte{0, 0})
		if disableErr != nil {
			// The radio's state is unknown; it must not be reused or claimed
			// running. Closing the transport is the last available boundary.
			s.cancel()
			_ = unix.Close(s.fd)
			s.fd = -1
			err = errors.Join(err, disableErr)
		}
	}()
	if _, err := s.command(ctx, scanEnable, []byte{1, 0}); err != nil {
		return err
	}
	if err := ready(); err != nil {
		return err
	}
	for {
		event, err := s.event(ctx)
		if err != nil {
			return err
		}
		if event[1] == 0x10 {
			return errors.New("linuxble: controller hardware error")
		}
		if err := reports(event, found); err != nil {
			return err
		}
	}
}

// Close cancels a quiet scan and waits for its bounded disable/cleanup.
func (s *Scanner) Close() error {
	s.cancel()
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.fd < 0 {
		return nil
	}
	err := unix.Close(s.fd)
	s.fd = -1
	return err
}

func (s *Scanner) command(ctx context.Context, opcode uint16, params []byte) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	packet := []byte{1, byte(opcode), byte(opcode >> 8), byte(len(params))}
	packet = append(packet, params...)
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := unix.Write(s.fd, packet)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if errors.Is(err, unix.EAGAIN) {
			if err := pause(ctx); err != nil {
				return nil, err
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		if n != len(packet) {
			return nil, io.ErrShortWrite // HCI commands are packets, never split writes.
		}
		break
	}
	for {
		event, err := s.event(ctx)
		if err != nil {
			return nil, fmt.Errorf("linuxble: command 0x%04x: %w", opcode, err)
		}
		if event[1] == 0x10 {
			return nil, errors.New("linuxble: controller hardware error")
		}
		data, done, err := commandReply(event, opcode)
		if done || err != nil {
			return data, err
		}
	}
}

func (s *Scanner) event(ctx context.Context) ([]byte, error) {
	// MediaTek rejects reads larger than its internal buffer. This also
	// bounds framing memory to 512 + one incomplete 258-byte HCI event.
	var buf [512]byte
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		event, rest, err := nextEvent(s.held)
		if err != nil {
			s.held = nil
			return nil, err
		}
		if event != nil {
			s.held = rest
			return event, nil
		}
		n, err := unix.Read(s.fd, buf[:])
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil && !errors.Is(err, unix.EAGAIN) {
			return nil, err
		}
		if n > 0 {
			s.held = append(s.held, buf[:n]...)
			continue
		}
		// stpbt can return zero immediately on an empty queue, even when
		// poll says readable. A timer prevents a busy loop in that case.
		if err := pause(ctx); err != nil {
			return nil, err
		}
	}
}

func pause(ctx context.Context) error {
	timer := time.NewTimer(10 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}
