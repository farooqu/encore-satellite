package dot

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/flynn/noise"
	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"

	"github.com/farooqu/encore-satellite/backends/bluetooth"
	"github.com/farooqu/encore-satellite/core/ble"
)

type managementRadio struct{}

func (managementRadio) Scan(ctx context.Context, _ bool, ready func() error, _ func(ble.Advertisement)) error {
	if err := ready(); err != nil {
		return err
	}
	<-ctx.Done()
	return ctx.Err()
}

// Exercise the production encrypted endpoint, including its combined listing
// and actual Action dispatch. Only the radio, network adapter and PCM are fake.
func TestLinuxManagementEncryptedAPI(t *testing.T) {
	m, err := newManagement()
	if err != nil {
		t.Fatal(err)
	}
	m.ssh = &sshManager{dataDir: filepath.Join(t.TempDir(), "ssh"), runDir: filepath.Join(t.TempDir(), "run")}
	m.wifi, _, _ = testWiFiManager(t)
	stop := startTestWiFiWorker(t, m.wifi)
	defer stop()
	c := Config{Schema: 1, Name: "management-test"}
	s := &settings{config: c, path: filepath.Join(t.TempDir(), "config.json")}
	p := newSpeaker(&speakerPCM{})
	defer p.Close()
	d := &dotControls{Proxy: bluetooth.New(managementRadio{}, bluetooth.Options{}), music: newMusic(c, s, p), management: m}
	key, err := esphome.GeneratePSK()
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx, listener, esphome.Info{Name: c.Name}, key) }()
	t.Cleanup(func() { cancel(); listener.Close(); <-done })
	peer := newManagementPeer(t, listener.Addr().String(), key)
	peer.send(&api.HelloRequest{ClientInfo: "management-test", ApiVersionMajor: 1, ApiVersionMinor: 12})
	if _, ok := peer.receive().(*api.HelloResponse); !ok {
		t.Fatal("missing encrypted hello")
	}
	peer.send(&api.ListEntitiesRequest{})
	actions := map[string]uint32{}
	entities := map[string]bool{}
listing:
	for {
		switch v := peer.receive().(type) {
		case *api.ListEntitiesServicesResponse:
			actions[v.Name] = v.Key
		case *api.ListEntitiesSwitchResponse:
			entities[v.ObjectId] = true
		case *api.ListEntitiesTextSensorResponse:
			entities[v.ObjectId] = true
		case *api.ListEntitiesDoneResponse:
			break listing
		}
	}
	if len(actions) != 8 || !entities["bluetooth_proxy"] || !entities["sendspin"] || !entities["ssh_enabled"] || !entities["wifi_ssid"] {
		t.Fatalf("incomplete combined listing: actions=%v entities=%v", actions, entities)
	}
	call := func(name string, args ...*api.ExecuteServiceArgument) *api.ExecuteServiceResponse {
		t.Helper()
		peer.send(&api.ExecuteServiceRequest{Key: actions[name], CallId: 42, ReturnResponse: true, Args: args})
		for {
			if r, ok := peer.receive().(*api.ExecuteServiceResponse); ok {
				if r.CallId != 42 {
					t.Fatal("wrong action response")
				}
				return r
			}
		}
	}
	if r := call("ssh_set_enabled", &api.ExecuteServiceArgument{Bool_: true}); r.Success {
		t.Fatal("enabled SSH without a public key")
	}
	if r := call("ssh_add_key", &api.ExecuteServiceArgument{String_: "command=\"reboot\" " + sshTestKey(t)}); r.Success {
		t.Fatal("accepted authorized_keys options")
	}
	if r := call("ssh_add_key", &api.ExecuteServiceArgument{String_: sshTestKey(t)}); !r.Success {
		t.Fatal("public key action failed:", r.ErrorMessage)
	}
	if r := call("ssh_set_enabled", &api.ExecuteServiceArgument{Bool_: true}); !r.Success {
		t.Fatal("SSH enable failed:", r.ErrorMessage)
	}
	if r := call("ssh_set_enabled"); r.Success {
		t.Fatal("missing bool argument silently disabled SSH")
	}
	if r := call("ssh_status"); !r.Success || !strings.Contains(string(r.ResponseData), `"enabled":true`) || !strings.Contains(string(r.ResponseData), `"running":false`) {
		t.Fatal("configured status confused with running daemon", r)
	}
	password := "synthetic-wifi-password"
	r := call("wifi_stage", &api.ExecuteServiceArgument{String_: "new-network"}, &api.ExecuteServiceArgument{String_: password})
	var transaction wifiStatus
	if !r.Success || json.Unmarshal(r.ResponseData, &transaction) != nil || transaction.ID == "" || strings.Contains(string(r.ResponseData), password) {
		t.Fatal("invalid or secret-bearing Wi-Fi response")
	}
	if r := call("wifi_confirm", &api.ExecuteServiceArgument{String_: "stale-id"}); r.Success {
		t.Fatal("confirmed a stale network transaction")
	}
	until := time.Now().Add(time.Second)
	for {
		r = call("wifi_status")
		if !r.Success || strings.Contains(string(r.ResponseData), password) {
			t.Fatal("Wi-Fi status failed or leaked password")
		}
		if json.Unmarshal(r.ResponseData, &transaction) == nil && transaction.State == "awaiting_confirmation" {
			break
		}
		if time.Now().After(until) {
			t.Fatal("candidate never became confirmable")
		}
		time.Sleep(5 * time.Millisecond)
	}
	if r := call("wifi_confirm", &api.ExecuteServiceArgument{String_: transaction.ID}); !r.Success {
		t.Fatal("valid confirmation failed:", r.ErrorMessage)
	}
	stop()
	peer.send(&api.SubscribeStatesRequest{})
	sshAvailable, wifiUnavailable := false, false
	for !sshAvailable || !wifiUnavailable {
		switch state := peer.receive().(type) {
		case *api.SwitchStateResponse:
			if state.Key == sshEnabledKey {
				sshAvailable = state.State && !state.MissingState
			}
		case *api.TextSensorStateResponse:
			if state.Key == wifiStateKey {
				wifiUnavailable = state.State == "unavailable"
			}
		}
	}
	if r := call("ssh_status"); !r.Success {
		t.Fatal("Wi-Fi worker loss broke SSH management:", r.ErrorMessage)
	}
	peer.conn.Close()
	if err := d.Serve(ctx, listener, esphome.Info{Name: c.Name}, esphome.PSK{}); err == nil {
		t.Fatal("management endpoint accepted an unprovisioned key")
	}
}

type managementPeer struct {
	t      *testing.T
	conn   net.Conn
	tx, rx *noise.CipherState
}

func newManagementPeer(t *testing.T, address string, key esphome.PSK) *managementPeer {
	t.Helper()
	c, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.Close() })
	p := &managementPeer{t: t, conn: c}
	p.writePacket(nil)
	p.readPacket() // server name/MAC announcement
	hs, err := noise.NewHandshakeState(noise.Config{CipherSuite: noise.NewCipherSuite(noise.DH25519, noise.CipherChaChaPoly, noise.HashSHA256), Pattern: noise.HandshakeNN, Initiator: true, Prologue: []byte("NoiseAPIInit\x00\x00"), PresharedKey: key[:], PresharedKeyPlacement: 0})
	if err != nil {
		t.Fatal(err)
	}
	b, _, _, err := hs.WriteMessage([]byte{0}, nil)
	if err != nil {
		t.Fatal(err)
	}
	p.writePacket(b)
	b = p.readPacket()
	if len(b) == 0 || b[0] != 0 {
		t.Fatal("Noise handshake rejected")
	}
	_, p.tx, p.rx, err = hs.ReadMessage(nil, b[1:])
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func (p *managementPeer) writePacket(b []byte) {
	p.t.Helper()
	p.conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	if _, err := p.conn.Write(append([]byte{1, byte(len(b) >> 8), byte(len(b))}, b...)); err != nil {
		p.t.Fatal(err)
	}
}

func (p *managementPeer) readPacket() []byte {
	p.t.Helper()
	p.conn.SetReadDeadline(time.Now().Add(5 * time.Second))
	var h [3]byte
	if _, err := io.ReadFull(p.conn, h[:]); err != nil {
		p.t.Fatal(err)
	}
	if h[0] != 1 {
		p.t.Fatal("expected Noise packet")
	}
	b := make([]byte, binary.BigEndian.Uint16(h[1:]))
	if _, err := io.ReadFull(p.conn, b); err != nil {
		p.t.Fatal(err)
	}
	return b
}

func (p *managementPeer) send(msg proto.Message) {
	p.t.Helper()
	info, ok := esphome.Lookup(msg)
	if !ok {
		p.t.Fatal("unknown message")
	}
	b, err := proto.Marshal(msg)
	if err != nil {
		p.t.Fatal(err)
	}
	frame := binary.BigEndian.AppendUint16(nil, uint16(info.ID))
	frame = binary.BigEndian.AppendUint16(frame, uint16(len(b)))
	frame, err = p.tx.Encrypt(nil, nil, append(frame, b...))
	if err != nil {
		p.t.Fatal(err)
	}
	p.writePacket(frame)
}

func (p *managementPeer) receive() proto.Message {
	p.t.Helper()
	b, err := p.rx.Decrypt(nil, nil, p.readPacket())
	if err != nil || len(b) < 4 || int(binary.BigEndian.Uint16(b[2:4])) != len(b)-4 {
		p.t.Fatal("invalid encrypted message", err)
	}
	msg, ok := esphome.NewMessage(uint32(binary.BigEndian.Uint16(b[:2])))
	if !ok || proto.Unmarshal(b[4:], msg) != nil {
		p.t.Fatal("invalid API message")
	}
	return msg
}
