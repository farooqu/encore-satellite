package incubator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/core/session"
	"github.com/farooqu/encore-satellite/core/wake"
)

type observedMonitorConn struct {
	net.Conn
	kind         byte
	wroteMonitor chan struct{}
}

func (c observedMonitorConn) Write(b []byte) (int, error) {
	if b[0] == c.kind {
		c.wroteMonitor <- struct{}{}
	}
	return c.Conn.Write(b)
}

func TestMonitorOffRevokesBlockedWrite(t *testing.T) {
	for _, test := range []struct {
		name string
		kind byte
	}{{"audio", 0x0C}, {"wake-report", 0x0B}} {
		t.Run(test.name, func(t *testing.T) { testMonitorOffRevokesBlockedWrite(t, test.kind) })
	}
}

func testMonitorOffRevokesBlockedWrite(t *testing.T, frameType byte) {
	engine := session.New(monitorSilentPlayback{})
	s := &Server{Engine: engine, CallAEC: true, MonitorAEC: true}
	conn, peer := net.Pipe()
	defer peer.Close()
	defer conn.Close()
	_ = peer.SetDeadline(time.Now().Add(time.Second))
	started := make(chan struct{}, 1)
	done := make(chan error, 1)
	go func() { done <- s.serveConn(context.Background(), observedMonitorConn{conn, frameType, started}) }()
	if kind, _, err := readFrame(peer); err != nil || kind != 0x01 {
		t.Fatalf("HELLO = %#x, %v", kind, err)
	}
	if err := writeFrame(peer, 0x14, []byte(`{"active":true}`)); err != nil {
		t.Fatal(err)
	}
	var id uint64
	for deadline := time.Now().Add(time.Second); id == 0 && time.Now().Before(deadline); time.Sleep(time.Millisecond) {
		id = engine.Snapshot().MonitorID
	}
	engine.ConfirmMonitor(id)
	var err error
	if frameType == 0x0B {
		err = engine.ReportWake(engine.Snapshot().Generation, id, wake.Report{Detected: []string{"fixture"}})
	} else {
		err = s.Capture(audio.Frame{Samples: []int16{71}})
	}
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-started: // no receiver is reading the monitor frame
	case <-time.After(time.Second):
		t.Fatal("monitor writer did not start")
	}
	if err := writeFrame(peer, 0x14, []byte(`{"active":false}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-done: // no reader was needed to finish the blocked write
	case <-time.After(time.Second):
		t.Fatal("monitor off waited for a blocked peer")
	}
	if _, _, err := readFrame(peer); !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrClosedPipe) {
		t.Fatalf("revoked data reached peer: %v", err)
	}
}

type monitorSilentPlayback struct{}

func (monitorSilentPlayback) Write(context.Context, []int16) error { return nil }
func (monitorSilentPlayback) Drain(context.Context) error          { return nil }
func (monitorSilentPlayback) Flush()                               {}

type heldRelayPlayback struct {
	monitorSilentPlayback
	started, cancelled chan struct{}
	played             chan int16
}

func (p *heldRelayPlayback) Write(ctx context.Context, pcm []int16) error {
	if pcm[0] == 1 {
		close(p.started)
		<-ctx.Done()
		close(p.cancelled)
		return errors.New("write retired during recovery")
	}
	p.played <- pcm[0]
	return nil
}

func TestRelayControlDoesNotWaitForSpeaker(t *testing.T) {
	for _, action := range []string{"monitor-off", "eof"} {
		t.Run(action, func(t *testing.T) {
			p := &heldRelayPlayback{started: make(chan struct{}), cancelled: make(chan struct{}), played: make(chan int16, 16)}
			engine := session.New(p)
			s := &Server{Engine: engine, CallAEC: true, MonitorAEC: true}
			conn, peer := net.Pipe()
			defer peer.Close()
			defer conn.Close()
			defer engine.SetMuted(true) // Release the fixture even on assertion failure.
			_ = peer.SetDeadline(time.Now().Add(2 * time.Second))
			done := make(chan error, 1)
			go func() { done <- s.serveConn(context.Background(), conn) }()
			if kind, _, err := readFrame(peer); err != nil || kind != 1 {
				t.Fatalf("HELLO = %#x, %v", kind, err)
			}
			send := func(kind byte, payload []byte) {
				t.Helper()
				if err := writeFrame(peer, kind, payload); err != nil {
					t.Fatal(err)
				}
			}
			send(0x14, []byte(`{"active":true,"intercom":true}`))
			send(0x10, []byte{1, 0})
			select {
			case <-p.started:
			case <-time.After(time.Second):
				t.Fatal("relay never reached playback")
			}
			if action == "eof" {
				_ = peer.Close()
				select {
				case <-done:
				case <-time.After(time.Second):
					t.Fatal("EOF did not retire blocked relay")
				}
			} else {
				for range 8 { // exceed the bounded relay queue while its worker is held
					send(0x10, []byte{2, 0})
				}
				send(0x14, []byte(`{"active":false}`))
				send(0x11, nil)
				send(0x12, []byte(`{"type":"announcement","data":{"active":true}}`))
				send(0x10, []byte{3, 0})
				select {
				case sample := <-p.played:
					if sample != 3 {
						t.Fatalf("old relay played across the fence: %d", sample)
					}
				case <-time.After(time.Second):
					t.Fatal("following announcement did not reach speaker")
				}
			}
			select {
			case <-p.cancelled:
			case <-time.After(time.Second):
				t.Fatal("stale playback never cancelled")
			}
			if state := engine.Snapshot(); state.MonitorID != 0 {
				t.Fatalf("monitor still active: %+v", state)
			}
			select {
			case sample := <-p.played:
				t.Fatalf("old relay played after cancellation: %d", sample)
			default:
			}
		})
	}
}

func TestMonitorDenialDoesNotArmOnUnmute(t *testing.T) {
	engine := session.New(monitorSilentPlayback{})
	engine.SetMuted(true)
	s := &Server{Engine: engine, CallAEC: true, MonitorAEC: true}
	conn, peer := net.Pipe()
	defer peer.Close()
	defer conn.Close()
	_ = peer.SetDeadline(time.Now().Add(time.Second))
	go func() { _ = s.serveConn(context.Background(), conn) }()
	if kind, _, err := readFrame(peer); err != nil || kind != 1 {
		t.Fatalf("HELLO = %#x, %v", kind, err)
	}
	request := func(body string, id uint64, accepted bool) {
		t.Helper()
		if err := writeFrame(peer, 0x14, []byte(body)); err != nil {
			t.Fatal(err)
		}
		kind, data, err := readFrame(peer)
		var result struct {
			Type      string
			RequestID uint64 `json:"request_id"`
			Accepted  bool
		}
		if err != nil || kind != 9 || json.Unmarshal(data, &result) != nil || result.Type != "monitor_result" || result.RequestID != id || result.Accepted != accepted {
			t.Fatalf("unexpected monitor result: %#x %s %v", kind, data, err)
		}
	}
	request(`{"active":true,"request_id":7}`, 7, false)
	engine.SetMuted(false)
	if state := engine.Snapshot(); !state.Connected || state.MonitorID != 0 {
		t.Fatalf("denied request armed on unmute or retired link: %+v", state)
	}
	if err := writeFrame(peer, 0x12, []byte(`{"type":"announcement","data":{"active":true}}`)); err != nil {
		t.Fatal(err)
	}
	request(`{"active":true,"request_id":8}`, 8, false)
	if err := writeFrame(peer, 0x12, []byte(`{"type":"announcement","data":{"active":false}}`)); err != nil {
		t.Fatal(err)
	}
	request(`{"active":true,"request_id":9}`, 9, true)
}
