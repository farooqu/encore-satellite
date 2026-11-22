package main

import (
	"context"
	"net"
)

// managementListener owns accepted sockets before the ESPHome server registers
// them. Its registered-connection snapshot can miss a just-accepted peer that
// subsequently blocks in the Noise handshake during runtime retirement.
type managementListener struct {
	net.Listener
	ctx    context.Context
	cancel context.CancelFunc
}

func newManagementListener(ctx context.Context, listener net.Listener) net.Listener {
	ctx, cancel := context.WithCancel(ctx)
	return &managementListener{Listener: listener, ctx: ctx, cancel: cancel}
}

func (l *managementListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err != nil {
		return nil, err
	}
	// Register before returning, even if cancellation already happened.
	stop := context.AfterFunc(l.ctx, func() { _ = c.Close() })
	return &managementConn{Conn: c, stop: stop}, nil
}

func (l *managementListener) Close() error {
	l.cancel()
	return l.Listener.Close()
}

type managementConn struct {
	net.Conn
	stop func() bool
}

func (c *managementConn) Close() error {
	c.stop() // Do not retain normally closed sockets until shutdown.
	return c.Conn.Close()
}
