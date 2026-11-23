package incubator_test

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
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
	"github.com/farooqu/encore-satellite/core/presentation"
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
	e := session.New(p)
	s := &incubator.Server{Engine: e, Name: "test", MAC: "02:00:00:00:00:01", Version: "0.1.0-dev.0", EnableModels: models}
	return s, serveTest(t, s)
}

func serveTest(t *testing.T, s *incubator.Server) string {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
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
	return l.Addr().String()
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

// Serve can still refuse clients after the engine has detached the old peer.
func reconnect(t *testing.T, address string) net.Conn {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for {
		c := connect(t, address)
		var head [3]byte
		_ = c.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if _, err := io.ReadFull(c, head[:]); err == nil {
			if head[0] != 0x01 {
				t.Fatalf("frame = %#x, want HELLO", head[0])
			}
			if _, err := io.CopyN(io.Discard, c, int64(binary.BigEndian.Uint16(head[1:]))); err != nil {
				t.Fatal(err)
			}
			return c
		}
		_ = c.Close()
		if time.Now().After(deadline) {
			t.Fatal("listener did not recover")
		}
		time.Sleep(time.Millisecond)
	}
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

func TestNativeTimerDisplayKeepsCountdownsAndRingSeparate(t *testing.T) {
	e, addr := start(t, &speaker{})
	e.SetMuted(true)
	c := connect(t, addr)
	_ = receive(t, c, 0x01)
	wait := func(check func(presentation.State) bool) presentation.State {
		t.Helper()
		for deadline := time.Now().Add(time.Second); ; time.Sleep(time.Millisecond) {
			s := e.Snapshot()
			if check(s) {
				return s
			}
			if time.Now().After(deadline) {
				t.Fatal("unexpected timer state", s)
			}
		}
	}
	// A very short timer vanishes locally; none of the countdowns implies a ring.
	send(t, c, 0x12, []byte(`{"type":"timer_progress","data":{"shown":1,"count":3,"left0":0.02,"left1":33.5,"left2":72.5}}`))
	wait(func(s presentation.State) bool {
		return s.Timers[0].Seconds == 34 && s.Timers[1].Seconds == 73 && !s.Alert
	})
	send(t, c, 0x12, []byte(`{"type":"timer_progress","data":{"shown":1,"remaining":"invalid"}}`))
	send(t, c, 0x12, []byte(`{"type":"timer_ringing","data":{"name":"Pasta","timers":2}}`))
	first := wait(func(s presentation.State) bool { return s.Alert })
	if !first.Muted || first.Timers[1].Seconds < 72 || first.Ring.Name != "Pasta" || first.Ring.Count != 2 || first.Ring.Sequence == 0 {
		t.Fatal("ring replaced countdowns or dropped metadata", first)
	}
	send(t, c, 0x12, []byte(`{"type":"timer_stopped"}`))
	send(t, c, 0x12, []byte(`{"type":"timer_ringing"}`))
	second := wait(func(s presentation.State) bool { return s.Alert && s.Ring.Sequence != first.Ring.Sequence })
	if second.Ring.Name != "" || second.Ring.Count != 0 {
		t.Fatal("new ring reused the previous label", second.Ring)
	}
	send(t, c, 0x10, []byte{1, 0, 2, 0})
	wait(func(s presentation.State) bool { return s.Output.Owner == "alert" && s.Output.State == "playing" })
	e.StopAlert(first.Ring.Sequence)
	if !e.Snapshot().Alert {
		t.Fatal("a stale touch stopped the newer ring")
	}
	e.StopAlert(second.Ring.Sequence)
	if v := object(t, receive(t, c, 0x09)); v["type"] != "stop_timer" {
		t.Fatal(v)
	}
	if s := e.Snapshot(); s.Alert || !s.Muted || s.Ring.Sequence != 0 || s.Timers[0].Seconds == 0 {
		t.Fatal("dismissing the ring changed mic or timers", s)
	}
	send(t, c, 0x12, []byte(`{"type":"timer_progress","data":{"shown":0}}`))
	wait(func(s presentation.State) bool { return s.Timers[0].Seconds == 0 })
	send(t, c, 0x12, []byte(`{"type":"timer_progress","data":{"shown":1,"remaining":123.5,"total":180}}`))
	wait(func(s presentation.State) bool { return s.Timers[0].Seconds == 124 && s.Timers[1].Seconds == 0 })
	_ = c.Close()
	wait(func(s presentation.State) bool { return !s.Connected && s.Timers[0].Seconds == 0 })
}

func object(t *testing.T, b []byte) map[string]any {
	t.Helper()
	var value map[string]any
	if err := json.Unmarshal(b, &value); err != nil {
		t.Fatal(err)
	}
	return value
}

func TestNativeMonitoringRequiresIndicatorAndDoesNotReuseAudio(t *testing.T) {
	e := session.New(&speaker{})
	s := &incubator.Server{Engine: e, Name: "test", MAC: "02:00:00:00:00:01", CallAEC: true, MonitorAEC: true}
	c := connect(t, serveTest(t, s))
	hello := object(t, receive(t, c, 0x01))
	if hello["monitor_aec"] != true || hello["monitor"] != true || hello["intercom"] != true {
		t.Fatal(hello)
	}
	waitMonitor := func(active bool) uint64 {
		t.Helper()
		for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); time.Sleep(time.Millisecond) {
			id := e.Snapshot().MonitorID
			if (id != 0) == active {
				return id
			}
		}
		t.Fatalf("monitor did not reach active=%t", active)
		return 0
	}
	noAudio := func() {
		t.Helper()
		_ = c.SetReadDeadline(time.Now().Add(25 * time.Millisecond))
		if _, err := c.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
			t.Fatalf("expected no frame on an open connection, got %v", err)
		}
	}
	var seq uint64
	capture := func(value int16) {
		t.Helper()
		if err := s.Capture(audio.Frame{Samples: []int16{value}, Sequence: seq, CapturedAt: time.Duration(seq) * time.Millisecond}); err != nil {
			t.Fatal(err)
		}
		seq++
	}
	capture(1200) // ordinary pre-roll must not appear in either monitor route
	noAudio()
	send(t, c, 0x14, []byte(`{"active":true,"intercom":false}`))
	first := waitMonitor(true)
	if err := e.StartManual(); !errors.Is(err, session.ErrUnavailable) {
		t.Fatal("manual wake during monitoring:", err)
	}
	capture(333)
	noAudio()
	e.ConfirmMonitor(first)
	capture(-513)
	for _, kind := range []byte{0x0C, 0x0A} {
		if got := receive(t, c, kind); !slices.Equal(got, []byte{255, 253}) {
			t.Fatalf("monitor PCM on %#x: %v", kind, got)
		}
	}
	noAudio()
	send(t, c, 0x13, []byte(`{"reason":"done","listen":true}`))
	// A second monitor request acknowledges that the reader passed the stray
	// HANGUP. It must keep the same owner and continue streaming.
	send(t, c, 0x14, []byte(`{"active":true,"request_id":1}`))
	if got := object(t, receive(t, c, 0x09)); got["accepted"] != true || e.Snapshot().MonitorID != first {
		t.Fatal("stray follow-up interrupted monitoring", got, e.Snapshot())
	}
	capture(42)
	for _, kind := range []byte{0x0C, 0x0A} {
		if got := receive(t, c, kind); !slices.Equal(got, []byte{42, 0}) {
			t.Fatalf("monitor PCM after stray follow-up on %#x: %v", kind, got)
		}
	}
	send(t, c, 0x14, []byte(`{"active":false}`))
	waitMonitor(false)
	noAudio()
	send(t, c, 0x14, []byte(`{"active":true}`))
	second := waitMonitor(true)
	if first == second {
		t.Fatal("reused revoked monitor request")
	}
	e.ConfirmMonitor(first)
	capture(515)
	noAudio()
	e.ConfirmMonitor(second)
	capture(1000)
	for _, kind := range []byte{0x0C, 0x0A} {
		if got := receive(t, c, kind); !slices.Equal(got, []byte{232, 3}) {
			t.Fatalf("rearmed monitor PCM on %#x: %v", kind, got)
		}
	}
	e.SetMuted(true)
	capture(123)
	if _, err := c.Read(make([]byte, 1)); err != io.EOF || e.Snapshot().MonitorID != 0 {
		t.Fatalf("mute did not revoke the monitor connection: %v", err)
	}
}

func TestNativeIntercomSeparatesCuesAndRelay(t *testing.T) {
	for _, end := range []string{"peer", "local", "mute"} {
		t.Run(end, func(t *testing.T) {
			p := &speaker{}
			e := session.New(p)
			s := &incubator.Server{Engine: e, Name: "test", MAC: "02:00:00:00:00:01", CallAEC: true, MonitorAEC: true}
			c := connect(t, serveTest(t, s))
			receive(t, c, 0x01)
			pcm := func(value byte) { send(t, c, 0x10, []byte{value, 0}) }
			cue := func(value byte) {
				send(t, c, 0x12, []byte(`{"type":"announcement","data":{"active":true}}`))
				pcm(value)
				send(t, c, 0x11, nil)
				receive(t, c, 0x06)
				send(t, c, 0x12, []byte(`{"type":"announcement","data":{"active":false}}`))
			}
			pcm(1) // unowned, including PCM on a newly connected idle peer
			cue(2)
			send(t, c, 0x14, []byte(`{"active":true,"intercom":true}`))
			pcm(3)
			deadline := time.Now().Add(time.Second)
			for e.Snapshot().Output.Owner != "intercom" && time.Now().Before(deadline) {
				time.Sleep(time.Millisecond)
			}
			st := e.Snapshot()
			if st.Output.Owner != "intercom" || st.MonitorID == 0 || st.WakeAllowed {
				t.Fatalf("intercom did not own playback: %+v", st)
			}
			if err := s.Capture(audio.Frame{Samples: []int16{123}}); err != nil {
				t.Fatal(err)
			}
			e.ConfirmMonitor(st.MonitorID)
			if err := s.Capture(audio.Frame{Samples: []int16{-513}, Sequence: 1, CapturedAt: time.Millisecond}); err != nil {
				t.Fatal(err)
			}
			if got := receive(t, c, 0x0A); !slices.Equal(got, []byte{255, 253}) {
				t.Fatalf("intercom PCM: %v; want only capture after indicator confirmation", got)
			}
			// Reading PCM does not prove the server's write has returned. Wait for
			// a reply from the same writer before testing the idle-writer teardown.
			send(t, c, 0x14, []byte(`{"active":true,"intercom":true,"request_id":1}`))
			if got := object(t, receive(t, c, 0x09)); got["type"] != "monitor_result" || got["request_id"] != float64(1) || got["accepted"] != true {
				t.Fatalf("intercom monitor result: %v", got)
			}
			if end == "mute" {
				e.SetMuted(true)
				_ = c.SetReadDeadline(time.Now().Add(time.Second))
				if _, err := c.Read(make([]byte, 1)); err != io.EOF || e.Snapshot().MonitorID != 0 {
					t.Fatalf("physical mute did not retire intercom: %v", err)
				}
				return
			}
			if end == "local" {
				if err := e.Stop(); err != nil {
					t.Fatal(err)
				}
				if event := object(t, receive(t, c, 0x09)); event["type"] != "intercom_end" {
					t.Fatal(event)
				}
				pcm(4) // already on its way when the local button was pressed
			}
			send(t, c, 0x14, []byte(`{"active":false}`))
			pcm(5) // after monitor-off, still part of the relay
			send(t, c, 0x11, nil)
			cue(6)
			_ = c.SetReadDeadline(time.Now().Add(25 * time.Millisecond))
			if _, err := c.Read(make([]byte, 1)); !errors.Is(err, os.ErrDeadlineExceeded) {
				t.Fatalf("relay terminator must not send a Drain ACK: %v", err)
			}
			p.mu.Lock()
			defer p.mu.Unlock()
			if !slices.Equal(p.pcm, []int16{2, 3, 6}) {
				t.Fatalf("speaker got %v; want only connect cue, live relay and end cue", p.pcm)
			}
		})
	}
}

func TestNativeCallAECAdvertisesAndPairsCapture(t *testing.T) {
	e := session.New(&speaker{})
	s := &incubator.Server{Engine: e, Name: "test", MAC: "02:00:00:00:00:01", CallAEC: true}
	c := connect(t, serveTest(t, s))
	hello := object(t, receive(t, c, 0x01))
	if hello["aec"] != true || hello["aec_lead_samples"] != float64(0) || hello["monitor_aec"] == true || hello["monitor"] != false || hello["intercom"] != false {
		t.Fatal(hello)
	}
	if err := s.Capture(audio.Frame{Samples: []int16{-513, 1000, 5}}); err != nil {
		t.Fatal(err)
	}
	if err := e.Wake(wake.Detection{Word: "nabu"}); err != nil {
		t.Fatal(err)
	}
	call := object(t, receive(t, c, 0x02))
	if call["preroll_bytes"] != float64(6) || call["aec_preroll_bytes"] != float64(6) {
		t.Fatal(call)
	}
	for _, kind := range []byte{0x08, 0x03} {
		if got := receive(t, c, kind); !slices.Equal(got, []byte{255, 253, 232, 3, 5, 0}) {
			t.Fatalf("pre-roll on %#x: %v", kind, got)
		}
	}
	if err := s.Capture(audio.Frame{Samples: []int16{32767, -32768}, Sequence: 1, CapturedAt: time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []byte{0x08, 0x03} {
		if got := receive(t, c, kind); !slices.Equal(got, []byte{255, 127, 0, 128}) {
			t.Fatalf("live PCM on %#x: %v", kind, got)
		}
	}

	send(t, c, 0x12, []byte(`{"type":"listening"}`))
	send(t, c, 0x12, []byte(`{"type":"responding"}`)) // External reply, no native PCM.
	send(t, c, 0x13, []byte(`{"reason":"complete","listen":true}`))
	follow := object(t, receive(t, c, 0x02))
	if follow["wake_word"] != "follow_up" || follow["preroll_bytes"] != float64(0) || follow["aec_preroll_bytes"] != float64(0) {
		t.Fatal(follow)
	}
	if err := s.Capture(audio.Frame{Samples: []int16{-42}, Sequence: 2, CapturedAt: 2 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	for _, kind := range []byte{0x08, 0x03} {
		if got := receive(t, c, kind); !slices.Equal(got, []byte{214, 255}) {
			t.Fatalf("follow-up PCM on %#x: %v", kind, got)
		}
	}

	e.SetMuted(true)
	if err := s.Capture(audio.Frame{Samples: []int16{123}, Sequence: 3, CapturedAt: 3 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	receive(t, c, 0x04)
	if _, err := c.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("muting did not end call audio: %v", err)
	}
}

func TestNativeWireEvidenceEndpointingDrainAndFollowUp(t *testing.T) {
	p := &speaker{entered: make(chan struct{}, 1), release: make(chan struct{})}
	e, address := start(t, p)
	c := connect(t, address)
	hello := object(t, receive(t, c, 0x01))
	if hello["protocol"] != float64(2) || hello["preroll_ms"] != float64(1500) || hello["aec"] != false || hello["monitor"] != false || hello["intercom"] != false {
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
	waitFor := func(ready func(presentation.State) bool) presentation.State {
		t.Helper()
		deadline := time.Now().Add(time.Second)
		for {
			state := e.Snapshot()
			if ready(state) {
				return state
			}
			if time.Now().After(deadline) {
				t.Fatal("missing native event", state)
			}
			time.Sleep(time.Millisecond)
		}
	}
	send(t, c, 0x99, []byte("optional future extension"))
	send(t, c, 0x12, []byte(`{"type":"recognized","data":{"text":"Actual upstream words"}}`))
	if state := waitFor(func(s presentation.State) bool { return s.Heard != "" }); state.Heard != "Actual upstream words" || state.Reply != "" || state.Phase != "working" {
		t.Fatal("transcript was not visible on its own", state)
	}
	send(t, c, 0x12, []byte(`{"type":"responding","data":{"text":"Actual reply"}}`))
	if state := waitFor(func(s presentation.State) bool { return s.Reply != "" }); state.Reply != "Actual reply" || state.Output.State != "queued" || state.Output.Received {
		t.Fatal("reply was not visible before audio", state)
	}
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
	if state := e.Snapshot(); state.Heard != "" || state.Reply != "" {
		t.Fatal("follow-up inherited old captions", state)
	}
	send(t, c, 0x12, []byte(`{"type":"listening","data":{"stage":"listening"}}`))
	send(t, c, 0x12, []byte(`{"type":"processing","data":{"speaker":"unknown"}}`))
	send(t, c, 0x12, []byte(`{"type":"recognized","data":{"text":"Follow-up question","command":"answer"}}`))
	send(t, c, 0x12, []byte(`{"type":"responding","data":{"text":""}}`))
	if state := waitFor(func(s presentation.State) bool { return s.Phase == "speaking" }); state.Heard != "Follow-up question" || state.Reply != "" {
		t.Fatal("empty response replaced or invented a caption", state)
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
	send(t, c, 0x12, []byte(`{"type":"listening","data":{"stage":"listening"}}`))
	send(t, c, 0x12, []byte(`{"type":"processing","data":{"speaker":"unknown"}}`))
	send(t, c, 0x12, []byte(`{"type":"not_recognized","data":{"heard":"Unmatched upstream words"}}`))
	if state := waitFor(func(s presentation.State) bool { return s.Phase == "error" }); state.Heard != "Unmatched upstream words" || state.Reply != "" {
		t.Fatal("discarded the unrecognized transcript", state)
	}
}

func TestNativeAnnouncementOwnsMultipleStreamsUntilInactive(t *testing.T) {
	p := &speaker{}
	s, address := startServer(t, p, true) // Biscuit enables pushed wake models.
	e := s.Engine
	c := connect(t, address)
	hello := object(t, receive(t, c, 0x01))
	if hello["monitor"] != false || (hello["monitor_aec"] != nil && hello["monitor_aec"] != false) {
		t.Fatal("playback and wake-model support must not claim People recording support", hello)
	}
	// An unsupported MONITOR request must not stream the microphone during playback.
	send(t, c, 0x14, []byte(`{"active":true,"intercom":false}`))
	send(t, c, 0x12, []byte(`{"type":"announcement","data":{"active":true}}`))
	for i, pcm := range [][]byte{{31, 0, 0, 128}, {254, 255, 1, 1}} {
		if i == 1 {
			// The first drain confirms the server already read MONITOR.
			if err := s.Capture(audio.Frame{Samples: []int16{11, -257, 513}}); err != nil {
				t.Fatal(err)
			}
		}
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
	// Stop retires the socket: no ambiguous ordinary end/HANGUP can be
	// mistaken for acknowledgement and then reopen input on this generation.
	var b [1]byte
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := c.Read(b[:]); err != io.EOF {
		t.Fatal(err)
	}
	next := reconnect(t, address)
	if err := e.StartManual(); err != nil {
		t.Fatal(err)
	}
	receive(t, next, 0x02)
	send(t, next, 0x12, []byte(`{"type":"listening"}`))
	send(t, next, 0x10, []byte{17, 0, 173, 255})
	send(t, next, 0x11, nil)
	receive(t, next, 0x06)
	p.mu.Lock()
	got := slices.Clone(p.pcm)
	p.mu.Unlock()
	if !slices.Equal(got, []int16{17, -83}) {
		t.Fatal("fresh native connection lost playback", got)
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

func TestStartupModelUploadPreservesIdlePlaybackAndMute(t *testing.T) {
	blob, err := os.ReadFile("../../core/wake/mixednet/testdata/pooled.mnb")
	if err != nil {
		t.Fatal(err)
	}
	for _, muted := range []bool{false, true} {
		for _, kind := range []string{"announcement", "timer_ringing"} {
			t.Run(fmt.Sprintf("muted=%v/%s", muted, kind), func(t *testing.T) {
				p := &speaker{}
				s, address := startServer(t, p, true)
				s.Engine.SetMuted(muted)
				c := connect(t, address)
				receive(t, c, 0x01)
				send(t, c, 0x23, nil)
				if got := pushModel(t, c, "nabu", blob, nil, "nabu"); got["ok"] != true || got["models"] != float64(1) {
					t.Fatal("startup upload rejected", got)
				}
				if state := s.Snapshot(true); state.Muted != muted || state.InputOpen || state.WakeArmed || state.WakeReady || s.ModelCount() != 1 {
					t.Fatal("model storage armed capture or lost mute", state)
				}
				s.Engine.SetMuted(false)
				if s.ModelCount() != 1 {
					t.Fatal("unmute lost startup model")
				}
				// No local call repairs the output context between CLEAR and PCM.
				b, _ := json.Marshal(map[string]any{"type": kind, "data": map[string]any{"active": true}})
				send(t, c, 0x12, b)
				send(t, c, 0x10, []byte{17, 0, 173, 255})
				send(t, c, 0x11, nil)
				receive(t, c, 0x06)
				p.mu.Lock()
				got := slices.Clone(p.pcm)
				p.mu.Unlock()
				if !slices.Equal(got, []int16{17, -83}) {
					t.Fatal("first startup stream did not play", got)
				}
			})
		}
	}
}

func TestPushedModelsDeferActiveOwnership(t *testing.T) {
	blob, err := os.ReadFile("../../core/wake/mixednet/testdata/pooled.mnb")
	if err != nil {
		t.Fatal(err)
	}
	for _, kind := range []string{"call", "announcement", "timer_ringing", "hung_up_output", "call_during_upload"} {
		t.Run(kind, func(t *testing.T) {
			s, address := startServer(t, &speaker{}, true)
			c := connect(t, address)
			receive(t, c, 0x01)
			if kind == "call_during_upload" {
				header, _ := json.Marshal(map[string]any{"format": "mixednet", "id": "nabu", "size": len(blob), "sha256": fmt.Sprintf("%x", sha256.Sum256(blob))})
				send(t, c, 0x20, header)
				for off := 0; off < len(blob); off += 4096 {
					send(t, c, 0x21, blob[off:min(off+4096, len(blob))])
				}
				// Defer a valid transfer that became busy before END as well
				// as one that was already busy when START arrived.
				send(t, c, 0x12, []byte(`{"type":"night","data":{"on":true}}`))
				deadline := time.Now().Add(time.Second)
				for s.Engine.Snapshot().Night != "on" {
					if time.Now().After(deadline) {
						t.Fatal("upload staging barrier timed out")
					}
					time.Sleep(time.Millisecond)
				}
			}
			if kind == "call" || kind == "hung_up_output" || kind == "call_during_upload" {
				if err := s.Engine.StartManual(); err != nil {
					t.Fatal(err)
				}
				receive(t, c, 0x02)
			} else {
				b, _ := json.Marshal(map[string]any{"type": kind, "data": map[string]any{"active": true}})
				send(t, c, 0x12, b)
			}
			if kind == "hung_up_output" {
				send(t, c, 0x12, []byte(`{"type":"listening"}`))
				send(t, c, 0x10, []byte{17, 0})
				send(t, c, 0x13, []byte(`{"reason":"done"}`))
			}
			var got map[string]any
			if kind == "call_during_upload" {
				send(t, c, 0x22, []byte(`{"id":"nabu"}`))
				got = object(t, receive(t, c, 0x07))
			} else {
				got = pushModel(t, c, "nabu", blob, nil, "nabu")
			}
			if got["ok"] != true || got["deferred"] != true || got["models"] != float64(1) || s.ModelCount() != 0 {
				t.Fatal("upload was not staged behind active owner", got, s.ModelCount())
			}
			switch kind {
			case "call", "call_during_upload":
				send(t, c, 0x12, []byte(`{"type":"listening"}`))
				send(t, c, 0x12, []byte(`{"type":"responding"}`))
				send(t, c, 0x13, []byte(`{"reason":"done","listen":true}`))
				if got := object(t, receive(t, c, 0x02)); got["wake_word"] != "follow_up" || s.ModelCount() != 0 {
					t.Fatal("refresh did not wait for follow-up", got, s.ModelCount())
				}
				send(t, c, 0x13, []byte(`{"reason":"done"}`))
			case "hung_up_output":
				if st := s.Engine.Snapshot(); st.InputOpen || st.Output.State != "playing" {
					t.Fatal("ordinary HANGUP did not close input independently of playback", st)
				}
				send(t, c, 0x11, nil)
				receive(t, c, 0x06)
			case "announcement":
				send(t, c, 0x12, []byte(`{"type":"announcement","data":{"active":false}}`))
			case "timer_ringing":
				send(t, c, 0x12, []byte(`{"type":"timer_stopped"}`))
			}
			deadline := time.Now().Add(time.Second)
			for s.ModelCount() != 1 {
				if time.Now().After(deadline) {
					t.Fatal("staged model was not activated at idle without capture", s.Snapshot(true))
				}
				time.Sleep(time.Millisecond)
			}
		})
	}
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
	if got := pushModel(t, c, "replacement", blob, nil, "replacement"); got["ok"] != true || got["deferred"] != true || got["models"] != float64(1) || s.ModelCount() != 6 {
		t.Fatal("busy refresh changed the running model set", got, s.ModelCount())
	}
	check(pushModel(t, c, "replacement-stop", blob, func(h map[string]any) { h["wake_word"] = "stop" }, "replacement-stop"), true, 2)
	check(pushModel(t, c, "replacement", []byte("invalid executable"), nil, "replacement"), false, 2)

	// Finish the turn and drain its response on this same connection before
	// activating the replacement set.
	send(t, c, 0x12, []byte(`{"type":"listening"}`))
	send(t, c, 0x10, []byte{17, 0, 173, 255})
	send(t, c, 0x13, []byte(`{"reason":"done"}`))
	send(t, c, 0x12, []byte(`{"type":"night","data":{"on":true}}`))
	deadline := time.Now().Add(time.Second)
	for s.Snapshot(true).Night != "on" {
		if time.Now().After(deadline) {
			t.Fatal("hangup did not arrive")
		}
		time.Sleep(time.Millisecond)
	}
	if s.ModelCount() != 6 {
		t.Fatal("refresh activated before the output drained")
	}
	send(t, c, 0x11, nil)
	receive(t, c, 0x06)
	deadline = time.Now().Add(time.Second)
	for s.ModelCount() != 2 {
		if time.Now().After(deadline) {
			t.Fatal("idle did not apply the replacement set")
		}
		time.Sleep(time.Millisecond)
	}
	if err := s.Engine.StartManual(); err != nil {
		t.Fatal(err)
	}
	receive(t, c, 0x02)
	send(t, c, 0x23, nil)
	if got := pushModel(t, c, "discard-on-close", blob, nil, "discard-on-close"); got["ok"] != true || got["deferred"] != true {
		t.Fatal(got)
	}
	if err := s.Engine.Stop(); err != nil {
		t.Fatal(err)
	}
	if result := object(t, receive(t, c, 0x04)); result["reason"] != "cancelled" {
		t.Fatal("deliberate Stop did not cancel active voice", result)
	}
	var b [1]byte
	_ = c.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := c.Read(b[:]); err != io.EOF {
		t.Fatal("deliberate Stop did not retire voice connection", err)
	}
	_ = c.Close()
	// Models are volatile per connection; a new connection starts with no
	// residents even when the same server and engine survive.
	next := reconnect(t, address)
	if state := s.Snapshot(true); s.ModelCount() != 0 || state.WakeModels != 0 || state.WakeArmed || state.WakeReady {
		t.Fatal("replacement connection inherited model readiness", state)
	}
	check(pushModel(t, next, "fresh", blob, nil, "fresh"), true, 1)
}

func TestModelReloadOnReconnectPreservesLocalAnnouncement(t *testing.T) {
	blob, err := os.ReadFile("../../core/wake/mixednet/testdata/pooled.mnb")
	if err != nil {
		t.Fatal(err)
	}
	p := &speaker{}
	s, address := startServer(t, p, true)
	previous := connect(t, address)
	receive(t, previous, 0x01)
	if err := previous.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(t.Context(), 3*time.Second)
	defer cancel()
	for s.Engine.Snapshot().Connected {
		if ctx.Err() != nil {
			t.Fatal("previous connection did not detach")
		}
		time.Sleep(time.Millisecond)
	}
	started, resume := make(chan struct{}), make(chan struct{})
	finished := make(chan session.AnnouncementResult, 1)
	go func() {
		result, _ := s.Engine.Announce(ctx, func(output context.Context, write func([]int16) error) error {
			if err := write([]int16{17, -83}); err != nil {
				return err
			}
			close(started)
			select {
			case <-resume:
				return write([]int16{-201})
			case <-output.Done():
				return output.Err()
			}
		})
		finished <- result
	}()
	select {
	case <-started:
	case result := <-finished:
		t.Fatal("announcement did not start", result)
	case <-ctx.Done():
		t.Fatal("announcement did not start", ctx.Err())
	}

	c := reconnect(t, address)
	send(t, c, 0x23, nil)
	if ack := pushModel(t, c, "nabu", blob, nil, "nabu"); ack["ok"] != true || ack["deferred"] != true || ack["models"] != float64(1) || s.ModelCount() != 0 {
		t.Fatal("reconnect could not reload its wake model", ack)
	}
	close(resume)
	select {
	case result := <-finished:
		if result.Outcome != "completed" || result.Drain != "ok" || result.AcceptedSamples != 3 {
			t.Fatal("announcement did not drain both blocks", result)
		}
	case <-ctx.Done():
		t.Fatal("announcement did not finish", ctx.Err())
	}
	for s.ModelCount() != 1 {
		if ctx.Err() != nil {
			t.Fatal("staged model did not activate after the announcement")
		}
		time.Sleep(time.Millisecond)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if !slices.Equal(p.pcm, []int16{17, -83, -201}) {
		t.Fatal("reconnect changed announcement audio", p.pcm)
	}
}

func energyModel() []byte {
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
	return blob
}

func TestLiveMonitorReportsScoresWithoutWakeActions(t *testing.T) {
	e := session.New(&speaker{})
	s := &incubator.Server{Engine: e, Name: "test", MAC: "02:00:00:00:00:01", CallAEC: true, MonitorAEC: true, EnableModels: true}
	c := connect(t, serveTest(t, s))
	receive(t, c, 0x01)
	for _, model := range []struct{ id, word string }{{"test-model", "okay_test"}, {"stop", "stop"}} {
		if got := pushModel(t, c, model.id, energyModel(), func(h map[string]any) { h["wake_word"] = model.word }, model.id); got["ok"] != true {
			t.Fatal(got)
		}
	}
	send(t, c, 0x14, []byte(`{"active":true,"request_id":1}`))
	if got := object(t, receive(t, c, 0x09)); got["accepted"] != true {
		t.Fatal(got)
	}
	e.ConfirmMonitor(e.Snapshot().MonitorID)
	// Keep a ringing alert active: a spoken Stop must not cancel it during
	// monitoring, even though the same model would be armed outside monitoring.
	send(t, c, 0x12, []byte(`{"type":"timer_ringing"}`))
	for deadline := time.Now().Add(time.Second); !e.Snapshot().Alert; time.Sleep(time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("timer did not ring")
		}
	}
	for i := range 111 {
		pcm := make([]int16, 320)
		if i >= 101 && i <= 105 {
			for j := range pcm {
				pcm[j] = int16(20000 * math.Sin(2*math.Pi*200*float64(j)/16000))
			}
		}
		if err := s.Capture(audio.Frame{Samples: pcm, Sequence: uint64(i), CapturedAt: time.Duration(i) * 20 * time.Millisecond}); err != nil {
			t.Fatal(err)
		}
		receive(t, c, 0x0C)
		receive(t, c, 0x0A)
		if i != 0 && i%5 == 0 {
			var report wake.Report
			if err := json.Unmarshal(receive(t, c, 0x0B), &report); err != nil {
				t.Fatal(err)
			}
			if len(report.Models) != 2 || report.Models[0].ID != "test-model" || report.Models[1].ID != "stop" || report.Models[0].Cutoff != 217 || report.Detected == nil {
				t.Fatalf("block %d: malformed report %+v", i, report)
			}
			score := report.Models[0]
			switch {
			case i < 105 && (len(report.Detected) != 0 || score.Peak != 0 || score.Window != 0):
				t.Fatalf("silence reported as wake: %+v", report)
			case i == 105 && (!slices.Equal(report.Detected, []string{"test-model", "stop"}) || score.Peak != 255 || score.Window <= score.Cutoff):
				t.Fatalf("monitor did not retain the hit and 8-bit scores: %+v", report)
			case i > 105 && len(report.Detected) != 0:
				t.Fatalf("hit leaked into next report: %+v", report)
			}
		}
		time.Sleep(10 * time.Millisecond) // Let the bounded DSP queue consume the block.
	}
	if state := s.Snapshot(true); state.Turn != 0 || state.InputOpen || state.WakeArmed || !state.Alert || state.MonitorID == 0 {
		t.Fatalf("monitored detection dispatched an action: %+v", state)
	}
	e.SetMuted(true)
	if _, err := c.Read(make([]byte, 1)); err != io.EOF {
		t.Fatalf("monitor remained open after mute: %v", err)
	}
}

func TestPushedModelRunsPCMAndStopCancelsInsteadOfCalling(t *testing.T) {
	blob := energyModel()
	s, address := startServer(t, &speaker{}, true)
	s.Engine.SetMuted(true)
	c := connect(t, address)
	receive(t, c, 1)
	send(t, c, 0x23, nil)
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
	if state := s.Snapshot(true); state.WakeArmed || state.WakeReady || state.InputOpen || state.Turn != 0 || !state.Muted {
		t.Fatal("muted startup models executed or armed capture", state)
	}
	s.Engine.SetMuted(false) // Upstream uploads only once; do not resend models.
	feed(false, 100)
	feed(true, 5)
	call := object(t, receive(t, c, 2))
	if call["wake_word"] != "energy-fixture" || call["max_probability"] != float64(255) || call["cutoff"] != float64(217) || call["preroll_bytes"] != float64(48000) {
		t.Fatal(call)
	}
	// Flush all pre-roll and live samples before checking Stop's notification.
	// The timer event is also a reader barrier after the deferred clear.
	send(t, c, 0x23, nil)
	send(t, c, 0x12, []byte(`{"type":"timer_ringing"}`))
	deadline := time.Now().Add(time.Second)
	for !s.Engine.Snapshot().Alert {
		if time.Now().After(deadline) {
			t.Fatal("timer did not arm")
		}
		time.Sleep(time.Millisecond)
	}
	if s.ModelCount() != 2 {
		t.Fatal("refresh removed the spoken Stop model during the call")
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
		if head[0] != 0x09 || object(t, body)["type"] != "stop_timer" {
			t.Fatal("stop did not dismiss timer before retiring call", head[0], string(body))
		}
		break
	}
	if result := object(t, receive(t, c, 0x04)); result["reason"] != "cancelled" {
		t.Fatal("stop started a new call instead of cancelling", result)
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

func TestNativeOptionalAttributionAndNightDoNotBreakCall(t *testing.T) {
	e, address := start(t, &speaker{})
	c := connect(t, address)
	receive(t, c, 1)
	_ = e.StartManual()
	receive(t, c, 2)
	send(t, c, 0x12, []byte(`{"type":"listening"}`))
	turn := e.Snapshot().Turn
	var step int
	apply := func(event string) presentation.State {
		t.Helper()
		send(t, c, 0x12, []byte(event))
		step++
		marker := fmt.Sprintf("Actual transcript %d", step)
		barrier, _ := json.Marshal(map[string]any{"type": "recognized", "data": map[string]string{"text": marker}})
		send(t, c, 0x12, barrier)
		deadline := time.Now().Add(time.Second)
		for {
			s := e.Snapshot()
			if s.Heard == marker {
				if !s.Connected || s.Turn != turn {
					t.Fatal("optional metadata replaced the call", s)
				}
				return s
			}
			if time.Now().After(deadline) {
				t.Fatal("reader stopped on optional metadata", s)
			}
			time.Sleep(time.Millisecond)
		}
	}
	valid := `{"speaker":"Person","score":0,"r":0,"g":37,"b":255}`
	for _, tc := range []struct {
		data string
		want presentation.Speaker
	}{
		{valid, presentation.Speaker{Name: "Person", ScoreKnown: true, RGB: [3]byte{0, 37, 255}, ColorKnown: true}},
		{`{"speaker":"Replacement","score":-2.75,"r":255,"g":0,"b":3}`, presentation.Speaker{Name: "Replacement", Score: -2.75, ScoreKnown: true, RGB: [3]byte{255, 0, 3}, ColorKnown: true}},
		{`{"speaker":"unknown","r":1,"g":2,"b":3}`, presentation.Speaker{}},
		{`{}`, presentation.Speaker{}},
		{`{"speaker":42,"score":0.9,"r":1,"g":2,"b":3}`, presentation.Speaker{}},
		{`{"speaker":"Person"}`, presentation.Speaker{Name: "Person"}},
		{`{"speaker":"Person","score":null,"r":0,"g":0,"b":0}`, presentation.Speaker{Name: "Person", ColorKnown: true}},
		{`{"speaker":"Person","score":"bad","r":1,"g":2,"b":3}`, presentation.Speaker{Name: "Person", RGB: [3]byte{1, 2, 3}, ColorKnown: true}},
		{`{"speaker":"Person","score":1e999,"r":1,"g":2,"b":3}`, presentation.Speaker{Name: "Person", RGB: [3]byte{1, 2, 3}, ColorKnown: true}},
		{`{"speaker":"Person","r":0,"g":37}`, presentation.Speaker{Name: "Person"}},
		{`{"speaker":"Person","r":1,"g":2,"b":null}`, presentation.Speaker{Name: "Person"}},
		{`{"speaker":"Person","r":1,"g":2,"b":256}`, presentation.Speaker{Name: "Person"}},
		{`{"speaker":"Person","r":-1,"g":2,"b":3}`, presentation.Speaker{Name: "Person"}},
		{`{"speaker":"Person","r":1,"g":2.5,"b":3}`, presentation.Speaker{Name: "Person"}},
		{`{"speaker":"Person","r":1,"g":"2","b":3}`, presentation.Speaker{Name: "Person"}},
	} {
		// Seed a preceding valid accent before every replacement, so absent or
		// malformed fields cannot accidentally retain a previous person's RGB.
		apply(`{"type":"processing","data":` + valid + `}`)
		s := apply(`{"type":"processing","data":` + tc.data + `}`)
		if s.Speaker != tc.want || s.Phase != "working" || s.Input != "" || s.Output.State != "" {
			t.Fatalf("data %s: %+v, want %+v", tc.data, s, tc.want)
		}
	}
	if s := apply(`{"type":"night","data":{"on":true}}`); s.Night != "on" {
		t.Fatal(s)
	}
	for _, data := range []string{`{}`, `{"on":null}`, `{"on":"false"}`, `{"on":0}`} {
		if s := apply(`{"type":"night","data":` + data + `}`); s.Night != "on" {
			t.Fatal("invalid optional policy changed last valid value", s)
		}
	}
	if s := apply(`{"type":"night","data":{"on":false}}`); s.Night != "off" {
		t.Fatal(s)
	}
	apply(`{"type":"responding","data":{"text":"Actual reply"}}`)
	if s := apply(`{"type":"processing","data":` + valid + `}`); s.Phase != "speaking" || s.Speaker != (presentation.Speaker{Name: "Person"}) || !s.InputOpen || s.Output.State != "queued" || s.Output.Received {
		t.Fatal("late speaker data reopened processing or claimed playback", s)
	}
}

func TestNativeReadinessNeedsOrdinaryArmedModelAndHealthyCapture(t *testing.T) {
	blob, err := os.ReadFile("../../core/wake/mixednet/testdata/pooled.mnb")
	if err != nil {
		t.Fatal(err)
	}
	s, address := startServer(t, &speaker{}, true)
	c := connect(t, address)
	receive(t, c, 1)
	if state := s.Snapshot(true); state.WakeReady || state.WakeArmed || state.WakeModels != 0 || !state.TalkReady {
		t.Fatal("connection without models claimed wake readiness", state)
	}
	for _, id := range []string{"stop", "stop-alias"} {
		result := pushModel(t, c, id, blob, func(h map[string]any) { h["wake_word"] = "stop" }, id)
		if result["ok"] != true {
			t.Fatal(result)
		}
	}
	if state := s.Snapshot(true); s.ModelCount() != 2 || state.WakeModels != 0 || state.WakeReady || state.WakeArmed {
		t.Fatal("stop-only registry claimed ordinary wake readiness", state)
	}
	if result := pushModel(t, c, "nabu", blob, nil, "nabu"); result["ok"] != true {
		t.Fatal(result)
	}
	if state := s.Snapshot(true); state.WakeModels != 1 || state.WakeArmed || state.WakeReady {
		t.Fatal("installed model claimed it had already armed", state)
	}
	if err := s.Capture(audio.Frame{Samples: make([]int16, 320)}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(time.Second)
	for !s.Snapshot(true).WakeReady {
		if time.Now().After(deadline) {
			t.Fatal("ordinary model did not arm on healthy idle capture", s.Snapshot(true))
		}
		time.Sleep(time.Millisecond)
	}
	if state := s.Snapshot(false); state.WakeReady || state.TalkReady || state.CaptureReady {
		t.Fatal("missing host capture health claimed readiness", state)
	}
	if result := pushModel(t, c, "nabu", []byte("invalid executable"), nil, "nabu"); result["ok"] != false {
		t.Fatal(result)
	}
	if state := s.Snapshot(true); state.WakeModels != 1 || !state.WakeReady {
		t.Fatal("failed replacement removed working readiness", state)
	}
	s.Engine.SetMuted(true)
	if state := s.Snapshot(true); state.WakeReady || state.WakeArmed || state.TalkReady || state.CaptureReady || !state.Inhibited {
		t.Fatal("privacy did not override readiness", state)
	}
	s.Engine.SetMuted(false)
	if result := pushModel(t, c, "nabu", blob, func(h map[string]any) { h["wake_word"] = "stop" }, "nabu"); result["ok"] != true {
		t.Fatal(result)
	}
	if state := s.Snapshot(true); state.WakeReady || state.WakeModels != 0 || state.WakeArmed {
		t.Fatal("ordinary-to-stop replacement retained readiness", state)
	}
	if result := pushModel(t, c, "nabu", blob, nil, "nabu"); result["ok"] != true {
		t.Fatal(result)
	}
	if err := s.Capture(audio.Frame{Samples: make([]int16, 320), Sequence: 1, CapturedAt: 20 * time.Millisecond}); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(time.Second)
	for !s.Snapshot(true).WakeReady {
		if time.Now().After(deadline) {
			t.Fatal("replacement ordinary model did not arm", s.Snapshot(true))
		}
		time.Sleep(time.Millisecond)
	}
	send(t, c, 0x23, nil)
	// Ordered policy event is a reader barrier after MODEL_CLEAR.
	send(t, c, 0x12, []byte(`{"type":"night","data":{"on":true}}`))
	deadline = time.Now().Add(time.Second)
	for s.Snapshot(true).Night != "on" {
		if time.Now().After(deadline) {
			t.Fatal("model clear did not finish")
		}
		time.Sleep(time.Millisecond)
	}
	if state := s.Snapshot(true); state.WakeReady || state.WakeArmed || state.WakeModels != 0 || s.ModelCount() != 0 || !state.TalkReady {
		t.Fatal("model clear retained wake readiness or disabled manual Talk", state)
	}
	_ = s.Capture(audio.Frame{Samples: []int16{0}, Discontinuity: true})
	if state := s.Snapshot(true); state.Connected || state.WakeReady || state.TalkReady || state.CaptureReady || state.Night != "unknown" || !state.Inhibited {
		t.Fatal("capture loss retained ready/connected claims", state)
	}
}
