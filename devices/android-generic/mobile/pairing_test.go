package mobile

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/flynn/noise"
	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"
)

func TestHomeAssistantKeyEnablesEncryptedControlsAfterReopen(t *testing.T) {
	dir := t.TempDir()
	r, err := Open(dir, silentOutput{})
	if err != nil {
		t.Fatal(err)
	}
	r.ConfigureHardware("  ACME Inc. ", " Tab\t10.1  Pro ")
	r.ConfigureUpdates(make(updateRequests, 1), " 1.2.3-dev.4 ")
	r.UpdateStatus("dev", r.UpdateGeneration(), "1.2.4", "Update available", true, false)
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	if err := r.PairHomeAssistant(); err != nil {
		t.Fatal(err)
	}
	first := esphome.PSK{51, 7, 19}
	bootstrap := pairingNoisePeer(t, esphome.PSK{})
	checkInfo := func(peer func(proto.Message, proto.Message), version string, provisionable bool) {
		t.Helper()
		info := &api.DeviceInfoResponse{}
		peer(&api.DeviceInfoRequest{}, info)
		if info.ApiEncryptionProvisionable != provisionable || info.MacAddress != r.MAC() || info.Name != r.Name() {
			t.Fatal("ESPHome did not preserve per-unit identity or report the correct provisioning state", info)
		}
		if info.FriendlyName != "Generic Android" || info.ProjectName != "Encore Satellite.ACME Inc․ Tab 10․1 Pro" ||
			info.Manufacturer != "ACME Inc." || info.Model != "Tab 10.1 Pro" ||
			info.ProjectVersion != version || info.EsphomeVersion != esphome.DefaultESPHomeVersion {
			t.Fatalf("incorrect Android metadata (want installed version %q): %v", version, info)
		}
	}
	checkInfo(bootstrap, "1.2.3-dev.4", true)
	request := &api.NoiseEncryptionSetKeyRequest{Key: []byte(first.String())}
	response := &api.NoiseEncryptionSetKeyResponse{}
	bootstrap(request, response)
	if !response.Success {
		t.Fatal("bootstrap rejected the first key")
	}
	waitForESPHomeState(t, r, "paired")
	paired := pairingNoisePeer(t, first)
	checkInfo(paired, "1.2.3-dev.4", false)
	paired(&api.NoiseEncryptionSetKeyRequest{Key: []byte((esphome.PSK{6, 3}).String())}, response)
	if response.Success {
		t.Fatal("paired endpoint accepted a replacement key")
	}
	r.Close()
	installed, err := Open(dir, silentOutput{})
	if err != nil {
		t.Fatal(err)
	}
	installed.ConfigureHardware("ACME Inc.", "Tab 10.1 Pro")
	installed.ConfigureUpdates(make(updateRequests, 1), "1.2.4")
	if err := installed.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(installed.Close)
	if installed.NoiseKey() != first.String() {
		t.Fatal("runtime did not reload the HA key")
	}
	checkInfo(pairingNoisePeer(t, first), "1.2.4", false)
}

func TestHomeAssistantPairingExpiresAndRequiresAnotherTap(t *testing.T) {
	r, err := Open(t.TempDir(), silentOutput{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.PairHomeAssistant(); err == nil {
		t.Fatal("pairing opened before starting the satellite")
	}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	const addr = "127.0.0.1:6053"
	assertClosed := func() {
		t.Helper()
		if c, err := net.DialTimeout("tcp", addr, time.Second); err == nil {
			c.Close()
			t.Fatal("ESPHome was reachable outside the pairing window")
		}
	}
	assertClosed()
	r.mu.Lock()
	// Use a short parent deadline; the shared bootstrap caps a real window at two minutes.
	ctx, cancel := context.WithTimeout(r.running, 300*time.Millisecond)
	defer cancel()
	err = r.pairHomeAssistant(ctx)
	r.mu.Unlock()
	if err != nil {
		t.Fatal(err)
	}
	send, receive := loopbackPeer(t, addr)
	send(&api.DeviceInfoRequest{})
	info := receive().(*api.DeviceInfoResponse)
	if !info.ApiEncryptionProvisionable {
		t.Fatal("fresh advertised identity did not accept provisioning")
	}
	if info.ProjectName != "Encore Satellite.Unknown" || info.ProjectVersion != "unknown" {
		t.Fatal("missing hardware or version must not produce blank display fields", info)
	}
	if err := r.PairHomeAssistant(); err == nil {
		t.Fatal("second tap replaced an active window")
	}
	waitForESPHomeState(t, r, "expired")
	if r.NoiseKey() != "" {
		t.Fatal("expiry installed a key")
	}
	assertClosed()
	if err := r.PairHomeAssistant(); err != nil {
		t.Fatal("explicit retry failed", err)
	}
	if got := espHomeState(t, r); got != "waiting" {
		t.Fatalf("explicit retry left setup in %q", got)
	}
	r.Close()
	assertClosed()
}

func TestHomeAssistantPairingPreservesExistingInstallation(t *testing.T) {
	dir := t.TempDir()
	key := (esphome.PSK{1, 42}).String()
	before := settings{MAC: "02:11:22:ab:cd:ef", Key: key, Dashboard: "https://ha.example/dashboard",
		Muted: true, Channel: "stable", ChannelGeneration: 7, BluetoothProxy: true}
	stored, err := json.Marshal(before)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "encore.json")
	if err := os.WriteFile(path, stored, 0600); err != nil {
		t.Fatal(err)
	}
	r, err := Open(dir, silentOutput{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	if r.NoiseKey() != key || espHomeState(t, r) != "paired" || r.PairHomeAssistant() == nil {
		t.Fatal("existing installation did not keep its key and skip setup")
	}
	r.Close()
	after, err := os.ReadFile(path)
	if err != nil || string(after) != string(stored) {
		t.Fatal("startup or rejected pairing changed existing settings", err)
	}
}

func waitForESPHomeState(t *testing.T, r *Runtime, want string) {
	t.Helper()
	deadline := time.After(3 * time.Second)
	for espHomeState(t, r) != want {
		select {
		case <-deadline:
			t.Fatalf("ESPHome setup did not reach %q", want)
		case <-time.After(5 * time.Millisecond):
		}
	}
}

func espHomeState(t *testing.T, r *Runtime) string {
	t.Helper()
	var state struct{ ESPHome string }
	if err := json.Unmarshal([]byte(r.Snapshot(false)), &state); err != nil {
		t.Fatal(err)
	}
	return state.ESPHome
}

func pairingNoisePeer(t *testing.T, key esphome.PSK) func(proto.Message, proto.Message) {
	t.Helper()
	send, receive := noisePeer(t, "127.0.0.1:6053", key)
	return func(request, response proto.Message) {
		t.Helper()
		send(request)
		got := receive()
		if got.ProtoReflect().Descriptor() != response.ProtoReflect().Descriptor() {
			t.Fatalf("unexpected encrypted response %T, want %T", got, response)
		}
		proto.Reset(response)
		proto.Merge(response, got)
	}
}

func noisePeer(t *testing.T, addr string, key esphome.PSK) (func(proto.Message), func() proto.Message) {
	t.Helper()
	c, err := net.DialTimeout("tcp", addr, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	if err := c.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	write := func(b []byte) {
		if _, err := c.Write(append([]byte{1, byte(len(b) >> 8), byte(len(b))}, b...)); err != nil {
			t.Fatal(err)
		}
	}
	read := func() []byte {
		var h [3]byte
		if _, err := io.ReadFull(c, h[:]); err != nil || h[0] != 1 {
			t.Fatal("expected ESPHome Noise frame", err)
		}
		b := make([]byte, binary.BigEndian.Uint16(h[1:]))
		if _, err := io.ReadFull(c, b); err != nil {
			t.Fatal(err)
		}
		return b
	}
	write(nil)
	read() // server identity
	hs, err := noise.NewHandshakeState(noise.Config{CipherSuite: noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256),
		Pattern: noise.HandshakeNN, Initiator: true, Prologue: []byte("NoiseAPIInit\x00\x00"), PresharedKey: key[:]})
	if err != nil {
		t.Fatal(err)
	}
	frame, _, _, err := hs.WriteMessage([]byte{0}, nil)
	if err != nil {
		t.Fatal(err)
	}
	write(frame)
	frame = read()
	if len(frame) == 0 || frame[0] != 0 {
		t.Fatal("Noise handshake rejected")
	}
	_, tx, rx, err := hs.ReadMessage(nil, frame[1:])
	if err != nil {
		t.Fatal(err)
	}
	send := func(request proto.Message) {
		t.Helper()
		payload, err := proto.Marshal(request)
		if err != nil {
			t.Fatal(err)
		}
		in, _ := esphome.Lookup(request)
		frame := binary.BigEndian.AppendUint16(nil, uint16(in.ID))
		frame = binary.BigEndian.AppendUint16(frame, uint16(len(payload)))
		wire, err := tx.Encrypt(nil, nil, append(frame, payload...))
		if err != nil {
			t.Fatal(err)
		}
		write(wire)
	}
	receive := func() proto.Message {
		t.Helper()
		frame, err := rx.Decrypt(nil, nil, read())
		if err != nil || len(frame) < 4 || int(binary.BigEndian.Uint16(frame[2:4])) != len(frame)-4 {
			t.Fatal("unexpected encrypted response", err)
		}
		m, ok := esphome.NewMessage(uint32(binary.BigEndian.Uint16(frame[:2])))
		if !ok || proto.Unmarshal(frame[4:], m) != nil {
			t.Fatal("invalid ESPHome message")
		}
		return m
	}
	return send, receive
}
