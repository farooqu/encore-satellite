package bluetooth_test

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/backends/bluetooth"
	"github.com/farooqu/encore-satellite/core/ble"
	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"
)

// In-process pipes deterministically stall writes without socket-buffer timing.
// Plaintext framing here isolates backpressure; no network listener is opened.
type pipeListener struct {
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
}

func (l *pipeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		return nil, net.ErrClosed
	}
}
func (l *pipeListener) Close() error   { l.once.Do(func() { close(l.done) }); return nil }
func (l *pipeListener) Addr() net.Addr { return &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)} }

func send(t *testing.T, c net.Conn, m proto.Message) {
	t.Helper()
	i, _ := esphome.Lookup(m)
	d, err := proto.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	b := binary.AppendUvarint([]byte{0}, uint64(len(d)))
	b = binary.AppendUvarint(b, uint64(i.ID))
	if _, err := c.Write(append(b, d...)); err != nil {
		t.Fatal(err)
	}
}

func receive(t *testing.T, c net.Conn, m proto.Message) {
	t.Helper()
	r := bufio.NewReader(c)
	if _, err := binary.ReadUvarint(r); err != nil {
		t.Fatal(err)
	}
	n, err := binary.ReadUvarint(r)
	if err != nil {
		t.Fatal(err)
	}
	id, err := binary.ReadUvarint(r)
	if err != nil {
		t.Fatal(err)
	}
	d := make([]byte, n)
	if _, err := io.ReadFull(r, d); err != nil {
		t.Fatal(err)
	}
	i, _ := esphome.Lookup(m)
	if id != uint64(i.ID) {
		t.Fatalf("message ID %d, want %d", id, i.ID)
	}
	if err := proto.Unmarshal(d, m); err != nil {
		t.Fatal(err)
	}
}

type scanner struct{ started chan context.Context }

func (s scanner) Scan(ctx context.Context, _ bool, ready func() error, _ func(ble.Advertisement)) error {
	if err := ready(); err != nil {
		return err
	}
	s.started <- ctx
	<-ctx.Done()
	return ctx.Err()
}

func TestStalledStateSubscribersDoNotBlockSettingsOrScanPause(t *testing.T) {
	radio := scanner{started: make(chan context.Context, 1)}
	p := bluetooth.New(radio, bluetooth.Options{SaveEnabled: func(bool) error { return nil }})
	l := &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
	s := &esphome.Server{Handler: p, WriteTimeout: 2 * time.Second, Logger: slog.New(slog.DiscardHandler)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); _ = s.Serve(ctx, l) }()
	go p.Run(ctx)
	t.Cleanup(func() { cancel(); <-done })
	connect := func() net.Conn {
		a, b := net.Pipe()
		t.Cleanup(func() { _ = b.Close() })
		_ = b.SetDeadline(time.Now().Add(5 * time.Second))
		l.conns <- a
		send(t, b, &api.HelloRequest{})
		receive(t, b, &api.HelloResponse{})
		return b
	}
	for range 3 {
		peer := connect()
		send(t, peer, &api.SubscribeStatesRequest{})
		initial := &api.SwitchStateResponse{}
		receive(t, peer, initial)
		if initial.State {
			t.Fatal("proxy must default OFF")
		}
		// Stop reading this subscriber. Its next state write will block.
	}
	healthy := connect()
	send(t, healthy, &api.SubscribeStatesRequest{})
	receive(t, healthy, &api.SwitchStateResponse{})
	scanPeer := connect()
	send(t, scanPeer, &api.SubscribeBluetoothLEAdvertisementsRequest{Flags: 1})
	receive(t, scanPeer, &api.BluetoothScannerStateResponse{}) // Stopped while OFF.
	changed := make(chan error, 1)
	go func() { changed <- p.SetEnabled(true) }()
	select {
	case err := <-changed:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatal("local setting blocked behind a network subscriber")
	}
	state := &api.SwitchStateResponse{}
	receive(t, healthy, state)
	if !state.State {
		t.Fatal("healthy subscriber did not receive enabled state")
	}
	receive(t, scanPeer, &api.BluetoothScannerStateResponse{}) // Starting.
	receive(t, scanPeer, &api.BluetoothScannerStateResponse{}) // Running.
	var scanning context.Context
	select {
	case scanning = <-radio.started:
	case <-time.After(time.Second):
		t.Fatal("scanner did not start")
	}
	paused := make(chan struct{})
	go func() { p.SetPaused(true); close(paused) }()
	select {
	case <-paused:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("lifecycle pause blocked behind a network subscriber")
	}
	select {
	case <-scanning.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("pause did not cancel the active scanner")
	}
	// Burst updates may coalesce, but the final wire state must be current.
	for range 20 {
		if err := p.SetEnabled(true); err != nil {
			t.Fatal(err)
		}
	}
	if err := p.SetEnabled(false); err != nil {
		t.Fatal(err)
	}
	for {
		receive(t, healthy, state)
		if !state.State {
			break
		}
	}
}

func TestScanOnlyPauseDoesNotWaitForPersistence(t *testing.T) {
	saving, release := make(chan struct{}), make(chan struct{})
	p := bluetooth.New(nil, bluetooth.Options{SaveEnabled: func(bool) error {
		close(saving)
		<-release
		return nil
	}})
	saved := make(chan error, 1)
	go func() { saved <- p.SetEnabled(true) }()
	<-saving
	defer func() { close(release); <-saved }()
	paused := make(chan struct{})
	go func() { p.SetPaused(true); close(paused) }()
	select {
	case <-paused:
	case <-time.After(500 * time.Millisecond):
		t.Fatal("scan-only lifecycle pause waited for a pending save")
	}
}
