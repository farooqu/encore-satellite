package incubator

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"testing"
	"testing/synctest"
	"time"

	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/core/session"
)

func TestNativeCallWatchdog(t *testing.T) {
	for _, scenario := range []string{"silent peer", "blocked playback"} {
		t.Run(scenario, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				p := &heldRelayPlayback{started: make(chan struct{}), cancelled: make(chan struct{})}
				e := session.New(p)
				s := &Server{Engine: e}
				conn, peer := net.Pipe()
				defer peer.Close()
				defer conn.Close()
				defer func() { e.Disconnect(e.Snapshot().Generation, "test_cleanup") }()
				_ = peer.SetDeadline(time.Now().Add(31 * time.Second))
				go func() { _ = s.serveConn(context.Background(), conn) }()
				receive := func(want byte) []byte {
					t.Helper()
					kind, body, err := readFrame(peer)
					if err != nil || kind != want {
						t.Fatalf("frame = %#x, %s, %v; want %#x", kind, body, err, want)
					}
					return body
				}
				receive(0x01)
				if err := e.StartManual(); err != nil {
					t.Fatal(err)
				}
				receive(0x02)
				time.Sleep(29 * time.Second)
				if err := s.Capture(audio.Frame{Samples: []int16{513}}); err != nil {
					t.Fatal(err)
				}
				if got := string(receive(0x03)); got != "\x01\x02" {
					t.Fatalf("live PCM before expiry = %x", got)
				}
				if scenario == "blocked playback" {
					for _, body := range []string{`{"type":"listening"}`, `{"type":"responding"}`} {
						if err := writeFrame(peer, 0x12, []byte(body)); err != nil {
							t.Fatal(err)
						}
					}
					if err := writeFrame(peer, 0x10, []byte{1, 0}); err != nil {
						t.Fatal(err)
					}
					<-p.started
				}
				body := receive(0x04)
				var end struct{ Reason string }
				if err := json.Unmarshal(body, &end); err != nil || end.Reason != "timeout" {
					t.Fatalf("CALL_END = %s, %v; want timeout", body, err)
				}
				if err := s.Capture(audio.Frame{Samples: []int16{-513}, Sequence: 1, CapturedAt: time.Second}); err != nil {
					t.Fatal(err)
				}
				if kind, _, err := readFrame(peer); !errors.Is(err, io.EOF) {
					t.Fatalf("expired voice socket did not close: frame=%#x err=%v", kind, err)
				}
				synctest.Wait()
				if st := e.Snapshot(); st.InputOpen || st.Connected {
					t.Fatalf("expired call retained capture: %+v", st)
				}
				if scenario == "blocked playback" {
					select {
					case <-p.cancelled:
					default:
						t.Fatal("watchdog did not cancel blocked playback")
					}
				}
			})
		})
	}
}
