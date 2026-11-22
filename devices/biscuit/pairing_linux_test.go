package biscuit

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"github.com/ygelfand/go-esphome-device/mdns"
	"google.golang.org/protobuf/proto"

	"github.com/farooqu/encore-satellite/backends/bluetooth"
	"github.com/farooqu/encore-satellite/internal/esphomepairing"
)

var pairingInfo = esphome.Info{Name: "test-dot", MACAddress: "02:11:22:33:44:55"}

func runPairingServer(t *testing.T, serve func(context.Context, net.Listener) error) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	done := make(chan error, 1)
	go func() { defer l.Close(); done <- serve(ctx, l) }()
	return l.Addr().String(), cancel, done
}

func waitPairingServer(t *testing.T, done <-chan error) {
	t.Helper()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("pairing listener did not shut down")
	}
}

func dialPairing(t *testing.T, addr string) net.Conn {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	_ = c.SetDeadline(time.Now().Add(time.Second))
	return c
}

func TestESPHomeFirstPairing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "noise.psk")
	l, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	ad := mdns.Config{Name: pairingInfo.Name, Port: l.Addr().(*net.TCPAddr).Port, IPs: []net.IP{net.IPv4(127, 0, 0, 1)}}
	l.Close()
	ctx, cancel := context.WithCancel(t.Context())
	t.Cleanup(cancel)
	action := make(chan struct{})
	ready := make(chan struct{})
	done := make(chan error, 1)
	var loaded esphome.PSK
	go func() {
		var err error
		loaded, err = waitForESPHomeKey(ctx, path, action, pairingInfo, ad, func() error { close(ready); return nil })
		done <- err
	}()
	select {
	case <-ready:
	case <-time.After(time.Second):
		t.Fatal("unpaired runtime did not become ready for onboarding")
	}
	if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
		c.Close()
		t.Fatal("ESPHome listened before Action")
	}
	select {
	case action <- struct{}{}:
	case <-time.After(time.Second):
		t.Fatal("pairing did not wait for Action")
	}
	buttonWait(t, "ESPHome listener did not open after Action", func() bool {
		c, err := net.DialTimeout("tcp", addr, time.Second)
		if err != nil {
			return false
		}
		c.Close()
		return true
	})
	key := esphome.PSK{3, 9, 8}
	request := &api.NoiseEncryptionSetKeyRequest{Key: []byte(key.String())}
	plain := dialPairing(t, addr)
	r := bufio.NewReader(plain)
	sendPlain := func(id uint64, msg proto.Message) {
		t.Helper()
		payload, err := proto.Marshal(msg)
		if err != nil {
			t.Fatal(err)
		}
		frame := binary.AppendUvarint([]byte{0}, uint64(len(payload)))
		frame = binary.AppendUvarint(frame, id)
		if _, err := plain.Write(append(frame, payload...)); err != nil {
			t.Fatal(err)
		}
	}
	readPlain := func(wantID uint64, msg proto.Message) {
		t.Helper()
		marker, err := r.ReadByte()
		if marker != 0 || err != nil {
			t.Fatalf("plaintext frame marker: %v", err)
		}
		size, err := binary.ReadUvarint(r)
		if err != nil || size > 1024 {
			t.Fatalf("plaintext frame length: %v", err)
		}
		id, err := binary.ReadUvarint(r)
		if id != wantID || err != nil {
			t.Fatalf("plaintext response ID: %d, %v", id, err)
		}
		b := make([]byte, size)
		if _, err := io.ReadFull(r, b); err != nil || proto.Unmarshal(b, msg) != nil {
			t.Fatalf("invalid plaintext response: %v", err)
		}
	}
	sendPlain(1, &api.HelloRequest{ClientInfo: "Home Assistant"})
	// aioesphomeapi sends the deprecated request with Hello on manager login,
	// but neither waits for an auth response nor uses password authentication.
	sendPlain(3, &api.AuthenticationRequest{})
	readPlain(2, &api.HelloResponse{})
	sendPlain(28, &api.SubscribeLogsRequest{})
	sendPlain(9, &api.DeviceInfoRequest{})
	sendPlain(11, &api.ListEntitiesRequest{})
	info := &api.DeviceInfoResponse{}
	readPlain(10, info)
	readPlain(19, &api.ListEntitiesDoneResponse{})
	if !info.ApiEncryptionSupported || !info.ApiEncryptionProvisionable || info.MacAddress != pairingInfo.MACAddress {
		t.Fatal("missing first-pair identity or capability flags")
	}
	requestID, _ := esphome.Lookup(request)
	sendPlain(uint64(requestID.ID), request)
	if _, err := r.ReadByte(); err != io.EOF {
		t.Fatalf("plaintext key request did not close connection: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("plaintext key installed")
	}

	// Hold open both an established bootstrap peer and an incomplete handshake
	// when a second Noise peer commits. Neither may hold up transition.
	other := newManagementPeer(t, addr, esphome.PSK{})
	_ = dialPairing(t, addr)
	peer := newManagementPeer(t, addr, esphome.PSK{})
	for _, invalid := range [][]byte{nil, {0, 1}, key[:], []byte((esphome.PSK{}).String())} {
		peer.send(&api.NoiseEncryptionSetKeyRequest{Key: invalid})
		if peer.receive().(*api.NoiseEncryptionSetKeyResponse).Success {
			t.Fatal("accepted malformed, raw, empty, or zero key")
		}
	}
	peer.send(request)
	if !peer.receive().(*api.NoiseEncryptionSetKeyResponse).Success {
		t.Fatal("valid first pairing rejected")
	}
	waitPairingServer(t, done)
	if _, err := other.conn.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("bootstrap peer survived key installation: %v", err)
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("key not persisted privately")
	}
	if loaded != key {
		t.Fatal("first pairing did not release the runtime with the saved key")
	}
	stored, err := waitForESPHomeKey(t.Context(), path, nil, pairingInfo, ad, func() error {
		t.Fatal("valid saved key unexpectedly entered onboarding")
		return nil
	})
	if err != nil || stored != key {
		t.Fatal("startup did not accept the exact saved HA key", err)
	}

	d := &biscuitControls{Proxy: bluetooth.New(managementRadio{}, bluetooth.Options{}), player: newSpeaker(&speakerPCM{})}
	t.Cleanup(func() { _ = d.player.Close() })
	addr, cancel, served := runPairingServer(t, func(ctx context.Context, l net.Listener) error {
		return d.Serve(ctx, l, pairingInfo, loaded)
	})
	peer = newManagementPeer(t, addr, key)
	peer.send(&api.DeviceInfoRequest{})
	if peer.receive().(*api.DeviceInfoResponse).ApiEncryptionProvisionable {
		t.Fatal("paired device still provisionable")
	}
	peer.send(&api.NoiseEncryptionSetKeyRequest{Key: []byte((esphome.PSK{4, 1}).String())})
	if peer.receive().(*api.NoiseEncryptionSetKeyResponse).Success {
		t.Fatal("paired endpoint accepted replacement key")
	}
	peer = newManagementPeer(t, addr, loaded)
	peer.send(&api.PingRequest{})
	if _, ok := peer.receive().(*api.PingResponse); !ok {
		t.Fatal("original key no longer authenticates")
	}
	cancel()
	waitPairingServer(t, served)
}

func TestESPHomePairingWindowExpiryAndFailedPersistence(t *testing.T) {
	// A nonexistent parent causes publishing to fail even as root.
	parent := filepath.Join(t.TempDir(), "state")
	path := filepath.Join(parent, "noise.psk")
	addr, _, done := runPairingServer(t, func(ctx context.Context, l net.Listener) error {
		ctx, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		defer cancel()
		return esphomepairing.Serve(ctx, l, pairingInfo, func(key esphome.PSK) error { return saveNoiseKey(path, key) })
	})
	peer := newManagementPeer(t, addr, esphome.PSK{})
	peer.send(&api.NoiseEncryptionSetKeyRequest{Key: []byte((esphome.PSK{2, 5}).String())})
	if peer.receive().(*api.NoiseEncryptionSetKeyResponse).Success {
		t.Fatal("acknowledged a key that could not be persisted")
	}
	waitPairingServer(t, done)
	if _, err := peer.conn.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("bootstrap peer survived window closure: %v", err)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatal("failed write left a key")
	}
}
