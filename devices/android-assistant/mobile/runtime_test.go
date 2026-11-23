package mobile

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/netip"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/core/presentation"
	"github.com/farooqu/encore-satellite/core/wake"
)

type presentationSignal chan string

func (s presentationSignal) Changed(snapshot string) {
	select {
	case s <- snapshot:
	default:
	}
}

type testAudio struct{}

func (testAudio) WriteVoice([]byte, int64) bool { return true }
func (testAudio) DrainVoice(int64) bool         { return true }
func (testAudio) FlushVoice(int64)              {}

func TestPersonalMicTrustedPeerAndVoice(t *testing.T) {
	dir := t.TempDir()
	r, err := Open(dir, testAudio{})
	if err != nil {
		t.Fatal(err)
	}
	fresh, err := Open(dir, testAudio{})
	if err != nil || fresh.MAC() != r.MAC() || fresh.Name() != r.Name() {
		t.Fatalf("identity changed on reopen: %v", err)
	}
	l, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1")})
	if err != nil {
		t.Fatal(err)
	}
	r.serve(&peerListener{l, netip.MustParseAddr("127.0.0.2")})
	t.Cleanup(r.Close)
	dial := func(ip string) net.Conn {
		t.Helper()
		dialer := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.ParseIP(ip)}, Timeout: time.Second}
		c, err := dialer.Dial("tcp4", l.Addr().String())
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		_ = c.SetDeadline(time.Now().Add(2 * time.Second))
		return c
	}
	wrong := dial("127.0.0.1")
	if n, err := wrong.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatalf("wrong peer received voice protocol or wasn't closed: %d, %v", n, err)
	}
	peer := dial("127.0.0.2")
	receive := func(kind byte) []byte {
		t.Helper()
		var head [3]byte
		if _, err := io.ReadFull(peer, head[:]); err != nil {
			t.Fatal(err)
		}
		body := make([]byte, binary.BigEndian.Uint16(head[1:]))
		if _, err := io.ReadFull(peer, body); err != nil || head[0] != kind {
			t.Fatalf("frame %02x, want %02x: %s, %v", head[0], kind, body, err)
		}
		return body
	}
	var hello struct {
		MAC        string `json:"mac"`
		WakeFormat string `json:"wake_format"`
		Monitor    bool   `json:"monitor"`
		MonitorAEC bool   `json:"monitor_aec"`
		Intercom   bool   `json:"intercom"`
	}
	if err := json.Unmarshal(receive(1), &hello); err != nil || hello.MAC != r.MAC() || hello.WakeFormat != "mixednet" || hello.Monitor || hello.MonitorAEC || hello.Intercom {
		t.Fatalf("wrong advertised capabilities: %+v (%v)", hello, err)
	}
	var snapshot presentation.State
	if err := json.Unmarshal([]byte(r.Snapshot(true)), &snapshot); err != nil || !snapshot.TalkReady || snapshot.WakeReady {
		t.Fatalf("Talk must work before a wake model arrives: %+v (%v)", snapshot, err)
	}
	model, err := os.ReadFile("../../../core/wake/mixednet/testdata/pooled.mnb")
	if err != nil {
		t.Fatal(err)
	}
	send := func(kind byte, payload []byte) {
		t.Helper()
		frame := binary.BigEndian.AppendUint16([]byte{kind}, uint16(len(payload)))
		if _, err := peer.Write(append(frame, payload...)); err != nil {
			t.Fatal(err)
		}
	}
	// The next frame must be the model ACK, not a reply to the unsolicited intercom request.
	send(0x14, []byte(`{"active":true,"intercom":true,"request_id":17}`))
	r.SetWakeWordEnabled(false)
	send(0x20, fmt.Appendf(nil, `{"format":"mixednet","id":"personal-word","size":%d,"sha256":"%x"}`, len(model), sha256.Sum256(model)))
	for chunk := range slices.Chunk(model, 4096) {
		send(0x21, chunk)
	}
	send(0x22, []byte(`{"id":"personal-word"}`))
	if ack := receive(0x07); !bytes.Contains(ack, []byte(`"ok":true`)) {
		t.Fatalf("wake model was rejected: %s", ack)
	}
	if err := r.Capture(make([]byte, 640)); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(r.Snapshot(true)), &snapshot); err != nil || !snapshot.TalkReady || snapshot.WakeReady || snapshot.Active {
		t.Fatalf("intercom or a disabled wake word changed idle Talk readiness: %+v (%v)", snapshot, err)
	}
	if err := r.voice.Engine.WakeFor(snapshot.Generation, wake.Detection{Word: "personal-word"}); err == nil {
		t.Fatal("an ordinary wake could start with wake-word listening disabled")
	}
	r.SetWakeWordEnabled(true)
	if err := r.Capture(make([]byte, 640)); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
		if err := json.Unmarshal([]byte(r.Snapshot(true)), &snapshot); err == nil && snapshot.WakeReady {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("local model did not arm from captured PCM: %+v", snapshot)
		}
	}
	changed := make(presentationSignal, 16)
	r.ObservePresentation(changed)
	readPresentation := func() presentation.State {
		t.Helper()
		var observed string
		select {
		case observed = <-changed:
		case <-time.After(time.Second):
			t.Fatal("presentation changed without notifying Android")
		}
		var state presentation.State
		if err := json.Unmarshal([]byte(observed), &state); err != nil {
			t.Fatal(err)
		}
		return state
	}
	startWake := func() presentation.State {
		t.Helper()
		if err := r.voice.Engine.Wake(wake.Detection{Word: "personal-word"}); err != nil {
			t.Fatal(err)
		}
		var call struct {
			PreRoll int `json:"preroll_bytes"`
		}
		if err := json.Unmarshal(receive(2), &call); err != nil {
			t.Fatal(err)
		}
		for remaining := call.PreRoll; remaining > 0; {
			block := receive(3)
			if len(block) == 0 || len(block) > remaining {
				t.Fatalf("invalid wake preroll block: %d bytes, %d remaining", len(block), remaining)
			}
			remaining -= len(block)
		}
		state := readPresentation()
		if state.Invocation != "wake" || state.Accepted || state.Input != "tentative" || !state.InputOpen {
			t.Fatalf("candidate must capture without claiming acceptance: %+v", state)
		}
		return state
	}
	previous := startWake()
	pcm := []byte{0x91, 0x12, 0x47, 0xf8}
	if err := r.Capture(pcm); err != nil {
		t.Fatal(err)
	}
	if got := receive(3); !bytes.Equal(got, pcm) {
		t.Fatalf("the candidate did not stream live PCM before acceptance: %x", got)
	}
	for _, reason := range []string{"answered_elsewhere", "wake_rejected"} {
		send(0x13, fmt.Appendf(nil, `{"reason":%q}`, reason))
		if state := readPresentation(); state.Active || state.Accepted || state.Reason != reason || !state.Connected {
			t.Fatalf("negative verdict did not return quietly to idle: %+v", state)
		}
		startWake()
	}
	send(0x12, []byte(`{"type":"listening"}`))
	winner := readPresentation()
	if !winner.Accepted || winner.Input != "listening" || winner.Turn == previous.Turn {
		t.Fatalf("acceptance did not promote the current wake: %+v", winner)
	}
	r.SetWakeWordEnabled(false) // The live request and its follow-up must finish normally.
	r.StopTurn(int64(previous.Generation), int64(previous.Turn))
	if !r.voice.Engine.Snapshot().Accepted {
		t.Fatal("stale overlay stop cancelled a newer wake")
	}
	send(0x12, []byte(`{"type":"recognized","data":{"text":"Turn on the porch light"}}`))
	send(0x12, []byte(`{"type":"responding","data":{"text":"The light is on"}}`))
	send(0x13, []byte(`{"reason":"done","listen":true}`))
	if call := receive(2); !bytes.Contains(call, []byte(`"wake_word":"follow_up"`)) {
		t.Fatalf("missing follow-up call: %s", call)
	}
	heard, reply, follow := readPresentation(), readPresentation(), readPresentation()
	if heard.Turn != winner.Turn || heard.Heard != "Turn on the porch light" || heard.Reply != "" ||
		reply.Turn != winner.Turn || reply.Heard != heard.Heard || reply.Reply != "The light is on" ||
		follow.Generation != winner.Generation || follow.Turn <= winner.Turn || follow.Invocation != "follow_up" || follow.Heard != "" || follow.Reply != "" {
		t.Fatalf("transcript snapshots lost or out of order: heard=%+v reply=%+v follow=%+v", heard, reply, follow)
	}
	send(0x13, []byte(`{"reason":"done"}`))
	if state := readPresentation(); state.Active || state.Reason != "done" || state.Turn != follow.Turn {
		t.Fatalf("missing normal completion for the final turn: %+v", state)
	}
	if err := r.Talk(); err != nil {
		t.Fatal(err)
	}
	if call := receive(2); !bytes.Contains(call, []byte(`"wake_word":"manual"`)) || !bytes.Contains(call, []byte(`"preroll_bytes":0`)) {
		t.Fatalf("Talk did not start a manual call: %s", call)
	}
	if err := r.Capture(pcm); err != nil {
		t.Fatal(err)
	}
	if got := receive(3); !bytes.Equal(got, pcm) {
		t.Fatalf("wrong PCM: %x, want %x", got, pcm)
	}
	r.Close()
	if err := json.Unmarshal([]byte(r.Snapshot(true)), &snapshot); err != nil || snapshot.Connected || snapshot.TalkReady || !snapshot.Muted {
		t.Fatalf("pause left the session listening: %+v (%v)", snapshot, err)
	}
	if c, err := net.DialTimeout("tcp4", l.Addr().String(), time.Second); err == nil {
		c.Close()
		t.Fatal("pause left the listener open")
	}
}

func TestPersonalMicRecoversWithoutRearming(t *testing.T) {
	r, err := Open(t.TempDir(), testAudio{})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	first, second, server := netip.MustParseAddr("127.0.0.1"), netip.MustParseAddr("127.0.0.3"), netip.MustParseAddr("127.0.0.2")
	address := func(ip netip.Addr) string { return netip.AddrPortFrom(ip, Port).String() }
	bind := func(ip netip.Addr) {
		t.Helper()
		if _, err := r.listen(ip, server); err != nil {
			t.Fatal(err)
		}
	}
	dialer := net.Dialer{LocalAddr: &net.TCPAddr{IP: server.AsSlice()}, Timeout: time.Second}
	connect := func(ip netip.Addr) net.Conn {
		t.Helper()
		conn, err := dialer.Dial("tcp4", address(ip))
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { conn.Close() })
		_ = conn.SetDeadline(time.Now().Add(time.Second))
		var header [3]byte
		if _, err := io.ReadFull(conn, header[:]); err != nil || header[0] != 1 {
			t.Fatalf("missing HELLO: %x (%v)", header, err)
		}
		if _, err := io.CopyN(io.Discard, conn, int64(binary.BigEndian.Uint16(header[1:]))); err != nil {
			t.Fatal(err)
		}
		return conn
	}
	bind(first)
	old := connect(first)
	bind(first) // An unchanged route refresh must not disrupt a live connection.
	if err := r.Talk(); err != nil {
		t.Fatalf("unchanged route broke Talk: %v", err)
	}
	generation := r.voice.Engine.Snapshot().Generation
	bind(netip.Addr{}) // No route while away.
	if state := r.voice.Engine.Snapshot(); state.Connected || state.Active || state.Muted {
		t.Fatalf("route loss must retire the turn but not mute the runtime: %+v", state)
	}
	if _, err := io.Copy(io.Discard, old); err != nil {
		t.Fatalf("previous connection did not close: %v", err)
	}
	bind(second)
	if c, err := dialer.Dial("tcp4", address(first)); err == nil {
		c.Close()
		t.Fatal("old route still had a listener")
	}
	if err := r.Talk(); err == nil {
		t.Fatal("new route alone did not establish a session")
	}
	connect(second)
	if state := r.voice.Engine.Snapshot(); !state.Connected || state.Active || state.Generation == generation {
		t.Fatalf("expected a fresh idle session after returning: %+v", state)
	}
	if err := r.Talk(); err != nil {
		t.Fatalf("reconnect did not restore Talk: %v", err)
	}
}
