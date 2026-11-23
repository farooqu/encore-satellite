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

type stateTestScanner struct {
	fail   chan struct{}
	report chan struct{}
}

func (s stateTestScanner) Scan(ctx context.Context, active bool, ready func() error, found func(ble.Advertisement)) error {
	select {
	case <-s.fail:
		return io.ErrUnexpectedEOF
	default:
	}
	if err := ready(); err != nil {
		return err
	}
	// Use different addresses for the two radio modes, so updating only
	// the state packet without changing the radio cannot pass.
	a := ble.Advertisement{Address: 0xc0ffee123456, AddressType: 1, Data: []byte{2, 1, 6}}
	if active {
		a.Address++
	}
	found(a)
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-s.report:
			found(a)
		}
	}
}

func TestScannerStateAndModeRequests(t *testing.T) {
	const (
		active  = api.BluetoothScannerMode_BLUETOOTH_SCANNER_MODE_ACTIVE
		passive = api.BluetoothScannerMode_BLUETOOTH_SCANNER_MODE_PASSIVE
	)
	for _, tc := range []struct {
		name       string
		modeSwitch bool
		configured api.BluetoothScannerMode
	}{
		{"fixed active", false, active},
		{"switchable", true, passive},
	} {
		t.Run(tc.name, func(t *testing.T) {
			radio := stateTestScanner{fail: make(chan struct{}), report: make(chan struct{}, 1)}
			p := bluetooth.New(radio, bluetooth.Options{Enabled: true, ModeSwitch: tc.modeSwitch})
			l := &pipeListener{conns: make(chan net.Conn), done: make(chan struct{})}
			s := &esphome.Server{Handler: p, Info: esphome.Info{BluetoothFeatures: p.Features()}, Logger: slog.New(slog.DiscardHandler)}
			ctx, cancel := context.WithCancel(context.Background())
			serverDone, proxyDone := make(chan struct{}), make(chan struct{})
			go func() { defer close(serverDone); _ = s.Serve(ctx, l) }()
			go func() { defer close(proxyDone); p.Run(ctx) }()
			a, peer := net.Pipe()
			t.Cleanup(func() { _ = peer.Close(); cancel(); <-serverDone; <-proxyDone })
			_ = peer.SetDeadline(time.Now().Add(5 * time.Second))
			l.conns <- a
			send(t, peer, &api.HelloRequest{})
			receive(t, peer, &api.HelloResponse{})
			send(t, peer, &api.DeviceInfoRequest{})
			info := &api.DeviceInfoResponse{}
			receive(t, peer, info)
			if flags := esphome.BluetoothFeature(info.BluetoothProxyFeatureFlags); flags&esphome.BluetoothRawAdvertisements == 0 || (flags&esphome.BluetoothStateAndMode != 0) != tc.modeSwitch {
				t.Fatalf("unexpected scanner feature flags: %#x", flags)
			}
			checkState := func(state esphome.ScannerState, mode api.BluetoothScannerMode) {
				t.Helper()
				got := &api.BluetoothScannerStateResponse{}
				receive(t, peer, got)
				if got.State != state || got.Mode != mode || got.ConfiguredMode != tc.configured {
					t.Fatalf("got %v, want %s with mode %s and configured mode %s", got, state, mode, tc.configured)
				}
			}
			checkAdvertisement := func(address uint64) {
				t.Helper()
				ads := &api.BluetoothLERawAdvertisementsResponse{}
				receive(t, peer, ads)
				if len(ads.Advertisements) == 0 || ads.Advertisements[0].Address != address {
					t.Fatalf("got %v, want raw advertisements from %x", ads, address)
				}
			}
			checkScan := func(mode api.BluetoothScannerMode, address uint64) {
				t.Helper()
				checkState(esphome.ScannerStarting, mode)
				checkState(esphome.ScannerRunning, mode)
				checkAdvertisement(address)
			}
			send(t, peer, &api.SubscribeBluetoothLEAdvertisementsRequest{Flags: 1})
			if tc.modeSwitch {
				checkScan(passive, 0xc0ffee123456)
				send(t, peer, &api.BluetoothScannerSetModeRequest{Mode: active})
				checkScan(active, 0xc0ffee123457)
				send(t, peer, &api.BluetoothScannerSetModeRequest{Mode: passive})
				checkScan(passive, 0xc0ffee123456)
			} else {
				checkScan(active, 0xc0ffee123457)
				send(t, peer, &api.BluetoothScannerSetModeRequest{Mode: passive})
				send(t, peer, &api.DeviceInfoRequest{})
				receive(t, peer, &api.DeviceInfoResponse{})
				radio.report <- struct{}{}
				checkAdvertisement(0xc0ffee123457)
			}
			p.SetPaused(true)
			checkState(esphome.ScannerStopped, tc.configured)
			close(radio.fail)
			p.SetPaused(false)
			checkState(esphome.ScannerStarting, tc.configured)
			checkState(esphome.ScannerFailed, tc.configured) // Radio failed before ready; no RUNNING.

			send(t, peer, &api.BluetoothScannerSetModeRequest{Mode: api.BluetoothScannerMode(2)})
			if _, err := peer.Read(make([]byte, 1)); err != io.EOF {
				t.Fatalf("invalid mode must be rejected, read error = %v", err)
			}
		})
	}
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
