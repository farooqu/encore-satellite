package incubator_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"os"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/backends/incubator"
	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/core/session"
	"github.com/farooqu/encore-satellite/core/wake"
)

type speaker struct {
	mu               sync.Mutex
	pcm              []int16
	entered, release chan struct{}
}

func (s *speaker) Write(ctx context.Context, pcm []int16) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := ctx.Err(); err != nil {
		return err
	}
	s.pcm = append(s.pcm, pcm...)
	return nil
}
func (s *speaker) Flush() {}
func (s *speaker) Drain(ctx context.Context) error {
	if s.entered == nil {
		return nil
	}
	select {
	case s.entered <- struct{}{}:
	default:
	}
	select {
	case <-s.release:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func start(t *testing.T, p *speaker) (*session.Engine, string) {
	s, address := startServer(t, p, false)
	return s.Engine, address
}

func startServer(t *testing.T, p *speaker, models bool) (*incubator.Server, string) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e := session.New(p)
	s := &incubator.Server{Engine: e, Name: "test", MAC: "02:00:00:00:00:01", Version: "0.1.0-dev.0", EnableModels: models}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- s.Serve(ctx, l)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("server leaked connection")
		}
	})
	return s, l.Addr().String()
}
func connect(t *testing.T, address string) net.Conn {
	t.Helper()
	c, err := net.Dial("tcp", address)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	return c
}
func send(t *testing.T, c net.Conn, kind byte, data []byte) {
	t.Helper()
	h := []byte{kind, byte(len(data) >> 8), byte(len(data))}
	_ = c.SetWriteDeadline(time.Now().Add(time.Second))
	if _, err := c.Write(append(h, data...)); err != nil {
		t.Fatal(err)
	}
}
func receive(t *testing.T, c net.Conn, kind byte) []byte {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	var h [3]byte
	if _, err := io.ReadFull(c, h[:]); err != nil {
		t.Fatal(err)
	}
	if h[0] != kind {
		t.Fatalf("frame = %#x, want %#x", h[0], kind)
	}
	p := make([]byte, int(binary.BigEndian.Uint16(h[1:])))
	if _, err := io.ReadFull(c, p); err != nil {
		t.Fatal(err)
	}
	return p
}
func object(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(b, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestNativeWireEvidenceEndpointingDrainAndFollowUp(t *testing.T) {
	p := &speaker{entered: make(chan struct{}, 1), release: make(chan struct{})}
	e, address := start(t, p)
	c := connect(t, address)
	hello := object(t, receive(t, c, 0x01))
	if hello["protocol"] != float64(2) || hello["aec"] != false || hello["monitor"] != false || hello["intercom"] != false {
		t.Fatal(hello)
	}
	if hello["wake_format"] != nil || hello["model_slots"] != nil {
		t.Fatal("manual-only host claimed pushed-model execution", hello)
	}
	if err := e.Capture(audio.Frame{Samples: []int16{7, -11, 300}, Sequence: 0}); err != nil {
		t.Fatal(err)
	}
	if err := e.Wake(wake.Detection{Word: "nabu", Probability: 179, MaxProbability: 223, Cutoff: 161}); err != nil {
		t.Fatal(err)
	}
	call := object(t, receive(t, c, 0x02))
	if call["preroll_bytes"] != float64(6) || call["probability"] != float64(179) || call["max_probability"] != float64(223) || call["cutoff"] != float64(161) {
		t.Fatal(call)
	}
	if got := receive(t, c, 0x03); !slices.Equal(got, []byte{7, 0, 245, 255, 44, 1}) {
		t.Fatal(got)
	}
	send(t, c, 0x12, []byte(`{"type":"listening"}`))
	send(t, c, 0x12, []byte(`{"type":"processing"}`))
	// Synchronize processing through public presentation, then prove capture
	// stays open: native Incubator, not Encore, owns endpointing.
	deadline := time.Now().Add(time.Second)
	for e.Snapshot().Phase != "working" {
		if time.Now().After(deadline) {
			t.Fatal("no processing event")
		}
		time.Sleep(time.Millisecond)
	}
	if err := e.Capture(audio.Frame{Samples: []int16{-32768, 32767}, Sequence: 1, CapturedAt: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	if got := receive(t, c, 0x03); !slices.Equal(got, []byte{0, 128, 255, 127}) {
		t.Fatal(got)
	}
	if err := e.FinishInput(); err != session.ErrUnavailable {
		t.Fatal("invented native half-close", err)
	}
	send(t, c, 0x99, []byte("optional future extension"))
	send(t, c, 0x12, []byte(`{"type":"recognized","data":{"text":"Actual upstream words"}}`))
	send(t, c, 0x12, []byte(`{"type":"responding","data":{"text":"Actual reply"}}`))
	send(t, c, 0x10, []byte{1, 0, 254, 255, 3, 0})
	send(t, c, 0x11, nil)
	select {
	case <-p.entered:
	case <-time.After(time.Second):
		t.Fatal("did not reach playback drain")
	}
	_ = c.SetReadDeadline(time.Now().Add(50 * time.Millisecond))
	var byteRead [1]byte
	if _, err := c.Read(byteRead[:]); err == nil {
		t.Fatal("acknowledged before drain")
	}
	if state := e.Snapshot(); state.Heard != "Actual upstream words" || state.Reply != "Actual reply" {
		t.Fatal(state)
	}
	close(p.release)
	receive(t, c, 0x06)
	p.mu.Lock()
	got := slices.Clone(p.pcm)
	p.mu.Unlock()
	if !slices.Equal(got, []int16{1, -2, 3}) {
		t.Fatal(got)
	}
	send(t, c, 0x13, []byte(`{"reason":"complete","listen":true}`))
	follow := object(t, receive(t, c, 0x02))
	if follow["wake_word"] != "follow_up" || follow["preroll_bytes"] != float64(0) {
		t.Fatal(follow)
	}
	send(t, c, 0x13, []byte(`{"reason":"answered_elsewhere","listen":true}`))
	deadline = time.Now().Add(time.Second)
	for e.Snapshot().Reason != "answered_elsewhere" {
		if time.Now().After(deadline) {
			t.Fatal("upstream election not received")
		}
		time.Sleep(time.Millisecond)
	}
	if err := e.StartManual(); err != nil {
		t.Fatal("losing candidate needs reconnect", err)
	}
	next := object(t, receive(t, c, 0x02))
	if next["wake_word"] != "manual" || next["preroll_bytes"] != float64(0) {
		t.Fatal("loser followed up or reused old audio", next)
	}
}

func TestNativeAnnouncementOwnsMultipleStreamsUntilInactive(t *testing.T) {
	p := &speaker{}
	e, address := start(t, p)
	c := connect(t, address)
	receive(t, c, 0x01)
	send(t, c, 0x12, []byte(`{"type":"announcement","data":{"active":true}}`))
	for _, pcm := range [][]byte{{31, 0, 0, 128}, {254, 255, 1, 1}} {
		send(t, c, 0x10, pcm)
		send(t, c, 0x11, nil)
		receive(t, c, 0x06)
	}
	send(t, c, 0x12, []byte(`{"type":"announcement","data":{"active":false}}`))
	send(t, c, 0x10, []byte{99, 0})
	// A later timer event is a reader barrier, not authority for the earlier PCM.
	send(t, c, 0x12, []byte(`{"type":"timer_ringing"}`))
	deadline := time.Now().Add(time.Second)
	for !e.Snapshot().Alert {
		if time.Now().After(deadline) {
			t.Fatal("wire reader stuck after announcement")
		}
		time.Sleep(time.Millisecond)
	}
	p.mu.Lock()
	got := slices.Clone(p.pcm)
	p.mu.Unlock()
	if !slices.Equal(got, []int16{31, -32768, -2, 257}) {
		t.Fatal("announcement stream lost or inactive output accepted", got)
	}
}

func TestNativeCancelFencesLateOutputAndReconnect(t *testing.T) {
	p := &speaker{}
	e, address := start(t, p)
	c := connect(t, address)
	receive(t, c, 0x01)
	if err := e.StartManual(); err != nil {
		t.Fatal(err)
	}
	receive(t, c, 0x02)
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := object(t, receive(t, c, 0x04)); got["reason"] != "cancelled" {
		t.Fatal(got)
	}
	send(t, c, 0x10, []byte{9, 0})
	send(t, c, 0x12, []byte(`{"type":"responding","data":{"text":"late"}}`))
	send(t, c, 0x11, nil)
	// A timer event is a wire-reader barrier following the cancellation fence.
	send(t, c, 0x12, []byte(`{"type":"timer_ringing"}`))
	deadline := time.Now().Add(time.Second)
	for !e.Snapshot().Alert {
		if time.Now().After(deadline) {
			t.Fatal("wire reader stuck")
		}
		time.Sleep(time.Millisecond)
	}
	p.mu.Lock()
	n := len(p.pcm)
	p.mu.Unlock()
	if n != 0 || e.Snapshot().Reply != "" {
		t.Fatal("late cancelled output revived")
	}
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	if got := object(t, receive(t, c, 0x09)); got["type"] != "stop_timer" {
		t.Fatal(got)
	}
	if err := e.StartManual(); err != nil {
		t.Fatal(err)
	}
	receive(t, c, 0x02)
	// Selecting Assist revokes the native connection; old callbacks cannot win.
	if err := e.Select(session.Assist); err != nil {
		t.Fatal(err)
	}
	var b [1]byte
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := c.Read(b[:]); err != io.EOF {
		t.Fatal(err)
	}
	if err := e.Select(session.Incubator); err != nil {
		t.Fatal(err)
	}
	// The listener may still be finishing the old reader. Wait for its slot
	// to clear by reconnecting, bounded by a deadline rather than sleeping once.
	deadline = time.Now().Add(time.Second)
	for {
		next, err := net.Dial("tcp", address)
		if err != nil {
			t.Fatal(err)
		}
		_ = next.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		_, err = io.ReadFull(next, b[:])
		_ = next.Close()
		if err == nil && b[0] == 0x01 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("listener did not recover")
		}
	}
}

func TestNativeMuteCancelsAndReconnectsWithoutUnmuting(t *testing.T) {
	p := &speaker{}
	e, address := start(t, p)
	reconnect := func() net.Conn {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			next := connect(t, address)
			_ = next.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
			var h [3]byte
			_, err := io.ReadFull(next, h[:])
			if err == nil && h[0] == 0x01 {
				if _, err := io.CopyN(io.Discard, next, int64(binary.BigEndian.Uint16(h[1:]))); err != nil {
					t.Fatal(err)
				}
				return next
			}
			_ = next.Close()
			if time.Now().After(deadline) {
				t.Fatal("muted native connection could not reattach")
			}
		}
	}
	c := connect(t, address)
	receive(t, c, 0x01)
	if err := e.StartManual(); err != nil {
		t.Fatal(err)
	}
	receive(t, c, 0x02)
	e.SetMuted(true)
	if got := object(t, receive(t, c, 0x04)); got["reason"] != "cancelled" {
		t.Fatal("mute did not cancel upstream", got)
	}
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, err := c.Read(b[:]); err != io.EOF {
		t.Fatal("cancel did not retire the old native connection", err)
	}
	c = reconnect()
	if !e.Snapshot().Muted || e.StartManual() != session.ErrUnavailable {
		t.Fatal("replacement connection lost mute", e.Snapshot())
	}
	e.SetMuted(false)
	if err := e.StartManual(); err != nil {
		t.Fatal(err)
	}
	receive(t, c, 0x02)
	send(t, c, 0x12, []byte(`{"type":"listening"}`))
	send(t, c, 0x10, []byte{225, 255, 67, 0})
	send(t, c, 0x11, nil)
	receive(t, c, 0x06)
	p.mu.Lock()
	got := slices.Clone(p.pcm)
	p.mu.Unlock()
	if !slices.Equal(got, []int16{-31, 67}) || e.Snapshot().Reply != "" {
		t.Fatal("old muted response leaked into the next turn", got, e.Snapshot())
	}
	e.SetMuted(true)
	receive(t, c, 0x04)
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := c.Read(b[:]); err != io.EOF {
		t.Fatal("second cancel did not retire native connection", err)
	}
	_ = reconnect()
	if !e.Snapshot().Muted || !e.Snapshot().Connected || e.StartManual() != session.ErrUnavailable {
		t.Fatal("native reconnect lost mute", e.Snapshot())
	}
	e.SetMuted(false)
	if err := e.StartManual(); err != nil {
		t.Fatal("old cancellation survived native reconnect", err)
	}
}

func TestNativeMalformedFramesAndCompetingClient(t *testing.T) {
	for _, tc := range []struct {
		name    string
		kind    byte
		payload []byte
	}{
		{"odd PCM", 0x10, []byte{1}},
		{"bad JSON", 0x12, []byte("{")},
		{"oversized", 0x12, make([]byte, 4097)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, address := start(t, &speaker{})
			c := connect(t, address)
			receive(t, c, 0x01)
			other := connect(t, address)
			_ = other.SetReadDeadline(time.Now().Add(time.Second))
			var b [1]byte
			if _, err := other.Read(b[:]); err != io.EOF {
				t.Fatal("competing native client accepted", err)
			}
			send(t, c, tc.kind, tc.payload)
			_ = c.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := c.Read(b[:]); err == nil {
				t.Fatal("malformed frame accepted")
			}
		})
	}
}

func pushModel(t *testing.T, c net.Conn, id string, blob []byte, edit func(map[string]any), endID string) map[string]any {
	t.Helper()
	header := map[string]any{"format": "mixednet", "id": id, "size": len(blob), "sha256": fmt.Sprintf("%x", sha256.Sum256(blob)), "slot": 6}
	if edit != nil {
		edit(header)
	}
	b, err := json.Marshal(header)
	if err != nil {
		t.Fatal(err)
	}
	send(t, c, 0x20, b)
	for off := 0; off < len(blob); {
		n := min(997, len(blob)-off)
		send(t, c, 0x21, blob[off:off+n])
		off += n
	}
	b, _ = json.Marshal(map[string]string{"id": endID})
	send(t, c, 0x22, b)
	return object(t, receive(t, c, 0x07))
}

func TestPushedModelsValidateBeforeAckAndPreserveReplacement(t *testing.T) {
	blob, err := os.ReadFile("../../core/wake/mixednet/testdata/pooled.mnb")
	if err != nil {
		t.Fatal(err)
	}
	s, address := startServer(t, &speaker{}, true)
	c := connect(t, address)
	hello := object(t, receive(t, c, 0x01))
	if hello["wake_format"] != "mixednet" || hello["model_slots"] != float64(6) || hello["max_pool"] != true || hello["stop_word"] != true || hello["aec"] != false {
		t.Fatal("incorrect execution capabilities", hello)
	}
	check := func(result map[string]any, ok bool, count int) {
		t.Helper()
		if result["ok"] != ok || result["models"] != float64(count) || (!ok && result["error"] == "") {
			t.Fatal(result)
		}
	}
	check(pushModel(t, c, "nabu", blob, nil, "nabu"), true, 1)
	for _, tc := range []struct {
		name string
		edit func(map[string]any)
		end  string
		blob []byte
	}{
		{"wrong digest", func(h map[string]any) { h["sha256"] = fmt.Sprintf("%064d", 1) }, "nabu", blob},
		{"wrong end ID", nil, "another-model", blob},
		{"truncated", func(h map[string]any) { h["size"] = len(blob) + 1 }, "nabu", blob},
		{"invalid executable", nil, "nabu", []byte("MNB1 invalid but correctly hashed")},
		{"unsupported format", func(h map[string]any) { h["format"] = "tflite" }, "nabu", blob},
		{"VAD not executed", func(h map[string]any) { h["kind"] = "vad" }, "nabu", blob},
		{"bad settings", func(h map[string]any) { h["feature_step_size"] = 31 }, "nabu", blob},
		{"oversized declaration", func(h map[string]any) { h["size"] = 512*1024 + 1 }, "nabu", blob},
		{"overrun", func(h map[string]any) { h["size"] = 3 }, "nabu", []byte{1, 2, 3, 4}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			check(pushModel(t, c, "nabu", tc.blob, tc.edit, tc.end), false, 1)
		})
	}
	for i, id := range []string{"jarvis", "mycroft", "custom-one", "custom-two", "stop"} {
		check(pushModel(t, c, id, blob, nil, id), true, i+2)
	}
	check(pushModel(t, c, "seventh", blob, nil, "seventh"), false, 6)
	check(pushModel(t, c, "nabu", blob, nil, "nabu"), true, 6)

	if err := s.Engine.StartManual(); err != nil {
		t.Fatal(err)
	}
	receive(t, c, 0x02)
	send(t, c, 0x23, nil)
	if result := object(t, receive(t, c, 0x04)); result["reason"] != "cancelled" {
		t.Fatal("model clear did not cancel active voice", result)
	}
	check(pushModel(t, c, "after-clear", blob, nil, "after-clear"), true, 1)
	_ = c.Close()
	// Models are volatile per connection; a new connection starts with no
	// residents even when the same server and engine survive.
	deadline := time.Now().Add(time.Second)
	for {
		next := connect(t, address)
		var head [3]byte
		_ = next.SetReadDeadline(deadline)
		if _, err := io.ReadFull(next, head[:]); err == nil {
			body := make([]byte, binary.BigEndian.Uint16(head[1:]))
			if _, err := io.ReadFull(next, body); err != nil || head[0] != 1 {
				t.Fatal("invalid reconnect HELLO", err)
			}
			check(pushModel(t, next, "fresh", blob, nil, "fresh"), true, 1)
			break
		}
		_ = next.Close()
		if time.Now().After(deadline) {
			t.Fatal("model worker prevented reconnect")
		}
	}
}

func TestPushedModelRunsPCMAndStopCancelsInsteadOfCalling(t *testing.T) {
	// A tiny valid MNB1 model copies the first microWakeWord feature into the
	// logit. Silence (-128) maps to 0; energy maps to 255. This tests the real
	// frontend/executor/transport without asserting trained-model accuracy.
	blob := make([]byte, 32+10+52+2+256)
	copy(blob, "MNB1")
	put := binary.LittleEndian.PutUint16
	put(blob[4:], 1)
	put(blob[8:], 1)
	put(blob[10:], 1)
	blob[14], blob[16] = 1, 40
	binary.LittleEndian.PutUint32(blob[20:], 40)
	put(blob[32:], 1)
	put(blob[34:], 40)
	put(blob[38:], 1)
	blob[43] = 255 // RING_PUSH from external features.
	put(blob[50:], 1)
	put(blob[52:], 40)
	put(blob[54:], 40)
	for i := len(blob) - 256; i < len(blob); i++ {
		blob[i] = 255
	}
	blob[len(blob)-256+128] = 0
	s, address := startServer(t, &speaker{}, true)
	c := connect(t, address)
	receive(t, c, 1)
	if result := pushModel(t, c, "energy-fixture", blob, nil, "energy-fixture"); result["ok"] != true {
		t.Fatal(result)
	}
	if result := pushModel(t, c, "stop", blob, nil, "stop"); result["ok"] != true {
		t.Fatal(result)
	}
	var sequence uint64
	feed := func(energy bool, blocks int) {
		t.Helper()
		for range blocks {
			pcm := make([]int16, 320)
			if energy {
				for i := range pcm {
					pcm[i] = int16(20000 * math.Sin(2*math.Pi*200*float64(i)/16000))
				}
			}
			if err := s.Capture(audio.Frame{Samples: pcm, Sequence: sequence, CapturedAt: time.Duration(sequence) * 20 * time.Millisecond}); err != nil {
				t.Fatal(err)
			}
			clear(pcm) // Capture must copy, not retain the host's buffer.
			sequence++
			time.Sleep(20 * time.Millisecond)
		}
	}
	feed(false, 100)
	feed(true, 5)
	call := object(t, receive(t, c, 2))
	if call["wake_word"] != "energy-fixture" || call["max_probability"] != float64(255) || call["cutoff"] != float64(217) || call["preroll_bytes"] != float64(64000) {
		t.Fatal(call)
	}
	// Flush all pre-roll and live samples before checking Stop's notification.
	// The reader barrier makes timer arming observable without private hooks.
	send(t, c, 0x12, []byte(`{"type":"timer_ringing"}`))
	deadline := time.Now().Add(time.Second)
	for !s.Engine.Snapshot().Alert {
		if time.Now().After(deadline) {
			t.Fatal("timer did not arm")
		}
		time.Sleep(time.Millisecond)
	}
	feed(false, 100)
	feed(true, 5)
	for {
		_ = c.SetReadDeadline(time.Now().Add(time.Second))
		var head [3]byte
		if _, err := io.ReadFull(c, head[:]); err != nil {
			t.Fatal(err)
		}
		body := make([]byte, binary.BigEndian.Uint16(head[1:]))
		if _, err := io.ReadFull(c, body); err != nil {
			t.Fatal(err)
		}
		if head[0] == 0x03 {
			continue
		}
		if head[0] != 0x04 || object(t, body)["reason"] != "cancelled" {
			t.Fatal("stop started a new call instead of cancelling", head[0], string(body))
		}
		break
	}
	if result := object(t, receive(t, c, 0x09)); result["type"] != "stop_timer" {
		t.Fatal("call end discarded timer dismissal", result)
	}
}

func TestNativeModelTransferValidationCapacityAndPrivacy(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	e := session.New(&speaker{})
	s := &incubator.Server{Engine: e, Name: "models", MAC: "02:00:00:00:00:02", EnableModels: true}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.Serve(ctx, l) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Error(err)
		}
	})
	c := connect(t, l.Addr().String())
	hello := object(t, receive(t, c, 0x01))
	if hello["wake_format"] != "mixednet" || hello["model_slots"] != float64(6) || hello["stop_word"] != true || hello["aec"] != false {
		t.Fatal(hello)
	}
	blob, err := os.ReadFile("../../core/wake/mixednet/testdata/pooled.mnb")
	if err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(blob)
	upload := func(id, hash string) map[string]any {
		t.Helper()
		header, _ := json.Marshal(map[string]any{"id": id, "slot": 99, "wake_word": id, "format": "mixednet", "size": len(blob), "sha256": hash})
		send(t, c, 0x20, header)
		for offset := 0; offset < len(blob); offset += 4000 {
			send(t, c, 0x21, blob[offset:min(offset+4000, len(blob))])
		}
		end, _ := json.Marshal(map[string]string{"id": id})
		send(t, c, 0x22, end)
		return object(t, receive(t, c, 0x07))
	}
	if got := upload("nabu", string(make([]byte, 64))); got["ok"] != false || got["models"] != float64(0) {
		t.Fatal("bad hash activated a model", got)
	}
	for i, id := range []string{"nabu", "jarvis", "computer", "okay", "hey", "stop"} {
		if got := upload(id, hex.EncodeToString(sum[:])); got["ok"] != true || got["models"] != float64(i+1) {
			t.Fatal("valid model rejected or slot treated as array offset", got)
		}
	}
	if got := upload("nabu", hex.EncodeToString(sum[:])); got["ok"] != true || got["models"] != float64(6) {
		t.Fatal("replacement consumed a new slot", got)
	}
	if got := upload("seventh", hex.EncodeToString(sum[:])); got["ok"] != false || got["models"] != float64(6) {
		t.Fatal("capacity not enforced", got)
	}
	// Execute the installed detectors through the same public capture entry
	// point the Android host uses.
	if err := s.Capture(audio.Frame{Samples: make([]int16, 320)}); err != nil {
		t.Fatal(err)
	}
	// Idle mute preserves the connection upstream; an active native turn
	// must cancel and retire it, including its connection-scoped models.
	if err := e.StartManual(); err != nil {
		t.Fatal(err)
	}
	receive(t, c, 0x02)
	e.SetMuted(true)
	if got := object(t, receive(t, c, 0x04)); got["reason"] != "cancelled" {
		t.Fatal("mute did not cancel the model-backed turn", got)
	}
	var b [1]byte
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := c.Read(b[:]); err != io.EOF {
		t.Fatal("privacy left the model connection open", err)
	}
	deadline := time.Now().Add(time.Second)
	for s.ModelCount() != 0 {
		if time.Now().After(deadline) {
			t.Fatal("models survived privacy disconnect")
		}
		time.Sleep(time.Millisecond)
	}
}
