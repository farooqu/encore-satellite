package main

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/backends/bluetooth"
	"github.com/farooqu/encore-satellite/internal/lab"
	esphome "github.com/ygelfand/go-esphome-device"
)

// Pause the real server immediately before it registers the accepted peer.
type pausedManagementConn struct {
	net.Conn
	entered, release chan struct{}
	once             sync.Once
}

func (c *pausedManagementConn) RemoteAddr() net.Addr {
	c.once.Do(func() { close(c.entered); <-c.release })
	return c.Conn.RemoteAddr()
}

type singleManagementListener struct {
	conn     net.Conn
	closed   chan struct{}
	once     sync.Once
	accepted bool
}

func (l *singleManagementListener) Accept() (net.Conn, error) {
	if !l.accepted {
		l.accepted = true
		return l.conn, nil
	}
	<-l.closed
	return nil, net.ErrClosed
}

func (l *singleManagementListener) Close() error {
	l.once.Do(func() { close(l.closed) })
	return nil
}

func (l *singleManagementListener) Addr() net.Addr { return l.conn.LocalAddr() }

func TestManagementRetiresAcceptedPeerBeforeHandshake(t *testing.T) {
	device, peer := net.Pipe()
	defer device.Close()
	defer peer.Close()
	conn := &pausedManagementConn{Conn: device, entered: make(chan struct{}), release: make(chan struct{})}
	raw := &singleManagementListener{conn: conn, closed: make(chan struct{})}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	listener := newManagementListener(ctx, raw)
	defer listener.Close()
	key, err := esphome.GeneratePSK()
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		info := esphome.Info{Name: "retirement-fixture"}
		proxy := bluetooth.New(&lab.VirtualRadio{}, bluetooth.Options{})
		done <- proxy.Serve(ctx, listener, info, key)
	}()
	select {
	case <-conn.entered:
	case <-time.After(2 * time.Second):
		close(conn.release)
		t.Fatal("server never received accepted peer")
	}
	cancel()
	<-raw.closed
	// Let upstream's one-shot registered-peer cleanup finish while the
	// accepted peer is still absent from that registry.
	time.Sleep(100 * time.Millisecond)
	close(conn.release)
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		peer.Close()
		<-done
		t.Fatal("silent accepted peer stranded runtime retirement")
	}
}
