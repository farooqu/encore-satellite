// Package esphomepairing implements the restricted Home Assistant bootstrap
// endpoint. The device host owns physical authorization, discovery and storage.
package esphomepairing

import (
	"bufio"
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"io"
	"net"
	"sync"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"
)

const window = 2 * time.Minute

// RefuseKey overrides the library's default in-memory key setter on an
// authenticated endpoint. First pairing does not authorize future rotation.
func RefuseKey(esphome.PSK) error {
	return errors.New("ESPHome key already installed")
}

// Serve owns l for the authorized window. save must durably store the supplied
// key without replacing an existing key and return only non-secret errors.
func Serve(ctx context.Context, l net.Listener, info esphome.Info, save func(esphome.PSK) error) error {
	ctx, cancel := context.WithTimeout(ctx, window)
	defer cancel()
	bootstrap := &bootstrapListener{Listener: l, conns: make(map[net.Conn]struct{})}
	defer bootstrap.Close()
	// Closing also interrupts handshakes, which may not yet be tracked by Server.
	stop := context.AfterFunc(ctx, func() { _ = bootstrap.Close() })
	defer stop()
	var mu sync.Mutex
	var committed bool
	s := esphome.Server{Info: info, PSK: esphome.Unprovisioned(), WriteTimeout: 2 * time.Second,
		// HA reads identity and the entity catalog together before provisioning.
		// Acknowledge the empty catalog, without binding any real device controls.
		Handler: esphome.HandlerFunc(func(_ context.Context, c *esphome.Conn, msg proto.Message) error {
			if _, ok := msg.(*api.ListEntitiesRequest); ok {
				return c.Send(&api.ListEntitiesDoneResponse{})
			}
			return nil
		}),
		OnSetEncryptionKey: func(key esphome.PSK) error {
			mu.Lock()
			defer mu.Unlock()
			if committed || ctx.Err() != nil {
				return errors.New("ESPHome pairing window closed")
			}
			if err := save(key); err != nil {
				return err
			}
			committed = true
			// Serve drains the current request before closing its connection.
			// Interrupt reads, not writes, so HA can receive the success reply.
			_ = bootstrap.Close()
			return nil
		},
	}
	err := s.Serve(ctx, bootstrap)
	if ctx.Err() != nil || committed {
		return nil
	}
	return err
}

type bootstrapListener struct {
	net.Listener
	mu     sync.Mutex
	closed bool
	conns  map[net.Conn]struct{}
}

func (l *bootstrapListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closed {
		_ = c.Close()
		return nil, net.ErrClosed
	}
	l.conns[c] = struct{}{}
	return &bootstrapConn{Conn: c, owner: l, input: bufio.NewReader(c)}, nil
}

func (l *bootstrapListener) Close() error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.closed = true
	for c := range l.conns {
		_ = c.SetReadDeadline(time.Now())
	}
	return l.Listener.Close()
}

type bootstrapConn struct {
	net.Conn
	owner *bootstrapListener
	input *bufio.Reader
	read  io.Reader
}

func (c *bootstrapConn) Close() error {
	c.owner.mu.Lock()
	delete(c.owner.conns, c.Conn)
	c.owner.mu.Unlock()
	return c.Conn.Close()
}

func (c *bootstrapConn) SetReadDeadline(t time.Time) error {
	c.owner.mu.Lock()
	if c.owner.closed {
		c.owner.mu.Unlock()
		_ = c.Close()
		return net.ErrClosed
	}
	defer c.owner.mu.Unlock()
	return c.Conn.SetReadDeadline(t)
}

func (c *bootstrapConn) Read(p []byte) (int, error) {
	if c.read == nil {
		first, err := c.input.Peek(1)
		if err != nil {
			_ = c.Close()
			return 0, err
		}
		c.read = c.input
		if first[0] == 0 {
			c.read = &identityFrames{input: c.input}
		}
	}
	n, err := c.read.Read(p)
	if err != nil {
		_ = c.Close()
	}
	return n, err
}

// The ESPHome server's provisioning hook doesn't distinguish the transport.
// Allow only the plaintext probe and lifecycle frames HA needs before it
// makes its separate zero-PSK Noise connection. Never pass a plaintext key.
type identityFrames struct {
	input *bufio.Reader
	frame bytes.Reader
}

func (r *identityFrames) Read(p []byte) (int, error) {
	if r.frame.Len() == 0 {
		marker, err := r.input.ReadByte()
		if err != nil {
			return 0, err
		}
		size, err := binary.ReadUvarint(r.input)
		if err != nil || marker != 0 || size > 1024 {
			return 0, errors.New("invalid ESPHome identity frame")
		}
		id, err := binary.ReadUvarint(r.input)
		if err != nil {
			return 0, err
		}
		switch id {
		case 1, 3, 5, 7, 9, 11, 28: // Hello, Auth (ignored), Disconnect, Ping, DeviceInfo, ListEntities, SubscribeLogs (silent).
		case 149: // DeviceCapabilitiesRequest, sent by HA before Noise provisioning.
		default:
			return 0, errors.New("only ESPHome identity is available in plaintext")
		}
		frame := binary.AppendUvarint([]byte{0}, size)
		frame = binary.AppendUvarint(frame, id)
		start := len(frame)
		frame = append(frame, make([]byte, size)...)
		if _, err := io.ReadFull(r.input, frame[start:]); err != nil {
			return 0, err
		}
		r.frame.Reset(frame)
	}
	return r.frame.Read(p)
}
