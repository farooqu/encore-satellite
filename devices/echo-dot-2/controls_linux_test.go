package dot

import (
	"context"
	"encoding/binary"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/core/session"
	"github.com/farooqu/encore-satellite/platforms/echo"
	"github.com/farooqu/encore-satellite/platforms/echo/input"
)

// Use real evdev records through the production input reader, not direct calls
// to a key mapping helper. Only the event node and audio hardware are virtual.
func buttonEvents(t *testing.T, gate *echo.Privacy, engine *session.Engine, player *speaker) func(uint16, int32) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "events")
	if err := syscall.Mkfifo(path, 0600); err != nil {
		t.Fatal(err)
	}
	w, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	d, err := input.Open(path)
	if err != nil {
		w.Close()
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- buttons(ctx, d, gate, engine, player) }()
	t.Cleanup(func() {
		cancel()
		d.Close()
		w.Close()
		select {
		case err := <-done:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(time.Second):
			t.Error("button reader did not stop")
		}
	})
	return func(code uint16, value int32) {
		t.Helper()
		offset := 2 * (strconv.IntSize / 8)
		b := make([]byte, offset+8)
		binary.LittleEndian.PutUint16(b[offset:], input.EvKey)
		binary.LittleEndian.PutUint16(b[offset+2:], code)
		binary.LittleEndian.PutUint32(b[offset+4:], uint32(value))
		if _, err := w.Write(b); err != nil {
			t.Fatal(err)
		}
	}
}

func buttonWait(t *testing.T, message string, ready func() bool) {
	t.Helper()
	until := time.Now().Add(time.Second)
	for !ready() {
		if time.Now().After(until) {
			t.Fatal(message)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func buttonMusic(t *testing.T) (*speaker, func() int16) {
	t.Helper()
	p := &speakerPCM{}
	s := newSpeaker(p)
	t.Cleanup(func() { s.Close() })
	s.MusicStart(1, nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		defer close(done)
		b := make([]byte, 480*4)
		for i := 0; i < len(b); i += 2 {
			binary.LittleEndian.PutUint16(b[i:], 12000)
		}
		for s.MusicWrite(ctx, 1, b) == nil {
		}
	}()
	t.Cleanup(func() { cancel(); <-done })
	sample := func() int16 {
		p.mu.Lock()
		defer p.mu.Unlock()
		if len(p.written) == 0 {
			return -1
		}
		return int16(binary.LittleEndian.Uint16(p.written[len(p.written)-4:]))
	}
	buttonWait(t, "initial volume was not 5/120", func() bool { return sample() == 500 })
	return s, sample
}

func TestButtonsVolumeTapHoldReleaseAndLimits(t *testing.T) {
	s, sample := buttonMusic(t)
	e := session.New(s)
	key := buttonEvents(t, echo.NewPrivacy(func() (bool, error) { return false, nil }), e, s)
	key(115, 1)
	buttonWait(t, "plus did not raise speaker gain", func() bool { return sample() == 600 })
	// This keypad reports only down/up; software must repeat while held.
	buttonWait(t, "held plus did not repeat", func() bool { return sample() >= 800 })
	key(115, 0)
	time.Sleep(30 * time.Millisecond)
	stopped := sample()
	time.Sleep(250 * time.Millisecond)
	if sample() != stopped {
		t.Fatal("volume kept increasing after release")
	}
	key(114, 1)
	buttonWait(t, "minus did not lower speaker gain", func() bool { return sample() == stopped-100 })
	buttonWait(t, "held minus did not repeat", func() bool { return sample() <= stopped-300 })
	key(114, 0)
	for i := 0; i < 40; i++ {
		key(115, 1)
		key(115, 0)
	}
	buttonWait(t, "plus exceeded or missed conservative cap", func() bool { return sample() == 3000 })
	for i := 0; i < 40; i++ {
		key(114, 1)
		key(114, 0)
	}
	buttonWait(t, "minus did not reach silence", func() bool { return sample() == 0 })
}

type buttonLink struct{ commands chan string }

func (*buttonLink) Backend() session.Backend { return session.Incubator }
func (*buttonLink) Close() error             { return nil }
func (l *buttonLink) Send(c session.Command) error {
	select {
	case l.commands <- c.Kind:
		return nil
	default:
		return errors.New("test peer queue full")
	}
}

func TestButtonsActionStopsAlertBeforeStartingVoice(t *testing.T) {
	s, sample := buttonMusic(t)
	e := session.New(s)
	e.SetActivityHandler(s.SetVoiceActive)
	l := &buttonLink{commands: make(chan string, 16)}
	g, err := e.Attach(l)
	if err != nil {
		t.Fatal(err)
	}
	key := buttonEvents(t, echo.NewPrivacy(func() (bool, error) { return false, nil }), e, s)
	if err := e.Handle(g, session.Event{Kind: "timer_ringing"}); err != nil {
		t.Fatal(err)
	}
	key(138, 1)
	select {
	case command := <-l.commands:
		if command != "stop_alert" {
			t.Fatalf("ringing timer Action sent %q, want stop_alert", command)
		}
	case <-time.After(time.Second):
		t.Fatal("Action did not stop ringing timer")
	}
	key(138, 0)
	buttonWait(t, "timer/Action did not silence music", func() bool { return !e.Snapshot().Alert && sample() == 0 })
	s.PlayMusic()
	key(138, 1)
	buttonWait(t, "idle Action did not start voice", func() bool { return e.Snapshot().Phase == "listening" })
	if command := <-l.commands; command != "start" {
		t.Fatalf("idle Action sent %q", command)
	}
	key(138, 2) // Kernel autorepeat and duplicate downs must not retrigger Action.
	key(138, 1)
	time.Sleep(30 * time.Millisecond)
	if e.Snapshot().Phase != "listening" {
		t.Fatal("held Action retriggered voice cancellation")
	}
	buttonWait(t, "Action voice did not duck music", func() bool { return sample() == 100 })
	key(138, 0)
	key(138, 1)
	buttonWait(t, "second Action did not stop voice/music", func() bool { return e.Snapshot().Phase == "idle" && sample() == 0 })
	if command := <-l.commands; command != "end" {
		t.Fatalf("active Action sent %q", command)
	}
}

func TestButtonsMuteHonorsDriverLagAndBlocksAction(t *testing.T) {
	s, sample := buttonMusic(t)
	e := session.New(s)
	l := &buttonLink{commands: make(chan string, 16)}
	if _, err := e.Attach(l); err != nil {
		t.Fatal(err)
	}
	var driver atomic.Bool
	gate := echo.NewPrivacy(func() (bool, error) { return driver.Load(), nil })
	key := buttonEvents(t, gate, e, s)
	key(113, 1) // Driver state still reports old unmuted value during its lag.
	buttonWait(t, "mute did not immediately silence speaker/voice", func() bool { return e.Snapshot().Muted && sample() == 0 })
	key(113, 0)
	key(138, 1)
	key(138, 0)
	if muted, _ := gate.Check(func() { t.Error("Action escaped mute transition") }); !muted {
		t.Fatal("mute did not inhibit delayed hardware report")
	}
	driver.Store(true)
	time.Sleep(550 * time.Millisecond)
	if muted, _ := gate.Check(nil); !muted || !driver.Load() {
		t.Fatal("software toggled the driver's mute a second time")
	}
	// The next physical press releases the hardware latch. Capture observes the
	// authoritative state after the delay; no software write toggles it back.
	driver.Store(false)
	key(113, 1)
	key(113, 0)
	time.Sleep(550 * time.Millisecond)
	if muted, err := gate.Check(nil); muted || err != nil {
		t.Fatal("unmute did not follow authoritative hardware state")
	}
	if sample() != 0 {
		t.Fatal("unmuting restarted stopped music")
	}
	select {
	case command := <-l.commands:
		t.Fatalf("muted Action emitted %q", command)
	default:
	}
}
