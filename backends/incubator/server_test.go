package incubator_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
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
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	e := session.New(p)
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() {
		done <- (&incubator.Server{Engine: e, Name: "test", MAC: "02:00:00:00:00:01", Version: "0.1.0-dev.0"}).Serve(ctx, l)
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
	return e, l.Addr().String()
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
