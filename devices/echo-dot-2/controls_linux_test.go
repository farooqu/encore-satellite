package dot

import (
	"context"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/core/presentation"
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

func TestRingFramesDistinguishInputWorkQueuedPlaybackAndPriority(t *testing.T) {
	ready := presentation.State{Connected: true, CaptureReady: true, WakeReady: true, Night: "off", Phase: "idle"}
	options := RingOptions{Brightness: 100}
	at := func(s presentation.State, level float64, milliseconds int) [36]byte {
		return RingFrame(s, level, time.Duration(milliseconds)*time.Millisecond, options)
	}
	if at(ready, 1, 0) != [36]byte{} {
		t.Fatal("healthy idle is not quiet")
	}
	input := ready
	input.Active, input.Input = true, "listening"
	quiet, loud := at(input, 0, 0), at(input, 1, 0)
	if quiet == loud || loud[1] <= quiet[1] || loud[1] <= loud[2] || loud[2] <= loud[0] {
		t.Fatal("actual mic level did not drive mint input", quiet, loud)
	}
	input.Input = "tentative"
	if tentative := at(input, 1, 0); tentative[1] >= quiet[1] {
		t.Fatal("tentative input was not dimmer", tentative)
	}
	work := ready
	work.Active, work.Phase = true, "working"
	a, b := at(work, 0, 0), at(work, 0, 600)
	if a == b || a[2] <= a[11] || b[11] <= b[2] {
		t.Fatal("processing crest did not move by a quarter ring", a, b)
	}
	for _, rgb := range [][3]byte{{255, 0, 0}, {255, 150, 0}, {0, 0, 255}, {255, 255, 255}} {
		work.Speaker = presentation.Speaker{Name: "Example", RGB: rgb, ColorKnown: true}
		frame := at(work, 0, 0)
		for i := range 12 {
			if frame[i*3+2] < frame[i*3] || frame[i*3+2] < frame[i*3+1] {
				t.Fatal("speaker color replaced blue processing semantics", rgb, frame)
			}
		}
	}
	work.Speaker = presentation.Speaker{RGB: [3]byte{}, ColorKnown: true}
	if at(work, 0, 0) != a {
		t.Fatal("valid black erased the processing family")
	}
	work.Speaker = presentation.Speaker{RGB: [3]byte{255, 0, 0}}
	if at(work, 0, 0) != a {
		t.Fatal("unknown color retained a profile accent")
	}
	output := ready
	output.Active, output.Phase = true, "speaking"
	output.Output = presentation.Output{State: "queued", Owner: "voice"}
	if at(output, 0, 0) != at(output, 1, 900) {
		t.Fatal("queued text animated like playback or microphone input")
	}
	output.Output.State = "playing"
	playing := at(output, 0, 0)
	if playing == at(output, 0, 900) || playing[2] <= playing[0] || playing[0] <= playing[1] {
		t.Fatal("actual output did not pulse lilac")
	}
	for i := 1; i < 12; i++ {
		if [3]byte(playing[i*3:i*3+3]) != [3]byte(playing[:3]) {
			t.Fatal("output segments did not pulse together", playing)
		}
	}
	output.Output.State, output.Phase = "draining", "idle"
	if at(output, 0, 0) != playing {
		t.Fatal("HANGUP/idle hid draining playback")
	}
	output.Alert = true
	alert := at(output, 0, 0)
	if alert == playing || alert == at(output, 0, 1600) || alert[0] <= alert[1] || alert[1] <= alert[2] {
		t.Fatal("alert did not take priority with its slower amber pulse")
	}
	output.Alert, output.Output.Owner = false, "alert"
	if at(output, 0, 0) != alert {
		t.Fatal("timer_stopped recolored an alert that the host is still draining")
	}
	output.Inhibited = true
	if at(output, 1, 900) != [36]byte{} {
		t.Fatal("Stop fence retained a previous animation")
	}
	output.Muted, output.Connected, output.CaptureReady = true, false, false
	options.Brightness = 1
	privacy := at(output, 1, 0)
	if privacy != at(output, 0, 7777) || privacy[0] < 8 || privacy[1] != 0 || privacy[2] != 0 {
		t.Fatal("low brightness or overlapping status hid steady privacy", privacy)
	}
	for _, mode := range []presentation.State{work, {Connected: true, CaptureReady: true, Output: presentation.Output{State: "playing"}, Night: "off"}, {Connected: true, CaptureReady: true, Alert: true, Night: "off"}} {
		options.Brightness, options.ReducedMotion = 100, true
		if at(mode, 0, 0) != at(mode, 0, 1370) {
			t.Fatal("reduced motion retained decorative animation", mode)
		}
		options.ReducedMotion, mode.Night = false, "unknown"
		if at(mode, 0, 0) != at(mode, 0, 1370) {
			t.Fatal("unknown night policy used daytime motion")
		}
	}
}

func TestRingFramesBoundEverySegmentAndDoNotInventReadiness(t *testing.T) {
	for _, night := range []string{"off", "on", "unknown"} {
		for _, brightness := range []int{1, 19, 100} {
			for _, output := range []string{"", "queued", "playing", "draining", "failed"} {
				for milliseconds := 0; milliseconds < 3200; milliseconds += 50 {
					s := presentation.State{Connected: true, CaptureReady: true, Active: true, Phase: "working", Night: night, Output: presentation.Output{State: output}, Speaker: presentation.Speaker{RGB: [3]byte{255, 255, 255}, ColorKnown: true}}
					frame := RingFrame(s, 1, time.Duration(milliseconds)*time.Millisecond, RingOptions{Brightness: brightness})
					for i := range 12 {
						sum := 0
						for _, channel := range frame[i*3 : i*3+3] {
							if channel > 32 || (night != "off" && channel > 8) {
								t.Fatal("channel exceeded software/night cap", frame)
							}
							sum += int(channel)
						}
						if sum == 0 || sum > 48 {
							t.Fatal("active segment dark or over budget", frame)
						}
					}
				}
			}
		}
	}
	options := RingOptions{Brightness: 100}
	ready := presentation.State{Connected: true, CaptureReady: true, WakeReady: true, Night: "off"}
	for _, change := range []func(*presentation.State){
		func(s *presentation.State) { s.WakeReady = false; s.TalkReady = true },
		func(s *presentation.State) { s.CaptureReady = false; s.Input = "listening" },
		func(s *presentation.State) { s.Connected = false; s.Output.State = "playing" },
	} {
		s := ready
		change(&s)
		f := RingFrame(s, 1, time.Second, options)
		if f == [36]byte{} || f[0] <= f[1] || f[1] < f[2] || f != RingFrame(s, 0, 2*time.Second, options) {
			t.Fatal("unavailable state looked ready or retained live motion", s, f)
		}
	}
}

func TestRingWriterCoalescesAndDropsEnvelopeAcrossTurns(t *testing.T) {
	var frames [][36]byte
	r := &ringRenderer{write: func(b []byte) error {
		if len(b) != 73 || b[72] != '\n' {
			t.Fatalf("invalid driver payload %q", b)
		}
		var frame [36]byte
		if _, err := hex.Decode(frame[:], b[:72]); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, frame)
		return nil
	}}
	start := time.Now()
	s := presentation.State{Connected: true, CaptureReady: true, WakeReady: true, Night: "off", Generation: 1, Turn: 1}
	options := RingOptions{Brightness: 100}
	draw := func(milliseconds int, mic captureReading) {
		t.Helper()
		if err := r.update(s, mic, start.Add(time.Duration(milliseconds)*time.Millisecond), options); err != nil {
			t.Fatal(err)
		}
	}
	draw(0, captureReading{})
	draw(50, captureReading{})
	if len(frames) != 1 || frames[0] != [36]byte{} {
		t.Fatal("unchanged idle frames were rewritten")
	}
	s.Input = "listening"
	draw(100, captureReading{level: 1, at: start, ready: true})
	baseline := frames[len(frames)-1]
	draw(150, captureReading{level: 1, at: start.Add(140 * time.Millisecond), ready: true})
	if frames[len(frames)-1][1] <= baseline[1] {
		t.Fatal("fresh actual capture did not animate the input")
	}
	s.Inhibited = true
	draw(200, captureReading{})
	if frames[len(frames)-1] != [36]byte{} {
		t.Fatal("Stop retained a live frame")
	}
	s.Inhibited, s.Turn = false, 2
	draw(250, captureReading{level: 1, at: start.Add(140 * time.Millisecond), ready: true})
	if frames[len(frames)-1] != baseline {
		t.Fatal("new turn reused the previous turn's capture envelope")
	}
	s.CaptureReady = false
	draw(300, captureReading{})
	s.CaptureReady = true
	draw(350, captureReading{level: 1, at: start.Add(340 * time.Millisecond), ready: true})
	if frames[len(frames)-1] != baseline {
		t.Fatal("capture restart reused an earlier observation")
	}
}

func TestCaptureMeterUsesActualPCMAndExpiresSilenceSeparately(t *testing.T) {
	now := time.Now()
	m := &captureMeter{origin: now}
	m.observe([]int16{32767, -32768, 0}, now)
	reading := m.snapshot(now)
	want := math.Sqrt((1073676289.0+1073741824.0)/3) / 32768
	if !reading.ready || math.Abs(reading.level-want) > 1.0/65535 {
		t.Fatal("meter did not reflect asymmetric signed PCM", reading)
	}
	m.observe([]int16{0, 0, 0}, now)
	if reading := m.snapshot(now.Add(250 * time.Millisecond)); !reading.ready || reading.level != 0 {
		t.Fatal("real silence was confused with capture loss", reading)
	}
	if m.snapshot(now.Add(251 * time.Millisecond)).ready {
		t.Fatal("stalled capture retained current-health claim")
	}
	m.observe([]int16{12345}, now)
	m.clear()
	if m.snapshot(now).ready {
		t.Fatal("cleared capture observation survived")
	}
}

func TestRingPolicyValidationAndSaveFailurePreserveLiveState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "ring.json")
	r, err := loadRingSettings(path)
	if err != nil || r.snapshot() != (RingOptions{Brightness: 100}) {
		t.Fatal("incorrect absent-policy default", err)
	}
	for _, invalid := range []float32{0, -1, 101, 1.5, float32(math.NaN()), float32(math.Inf(1))} {
		if err := r.configure(&invalid, nil); err == nil || r.snapshot().Brightness != 100 {
			t.Fatal("invalid control changed live policy", invalid, err)
		}
	}
	for _, bad := range []string{`null`, `[]`, `{`, `{"brightness":0}`, `{"brightness":101}`, `{"brightness":1.5}`, `{"brightness":50,"extra":true}`, `{} {}`} {
		if err := os.WriteFile(path, []byte(bad), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := loadRingSettings(path); err == nil {
			t.Fatal("accepted malformed policy", bad)
		}
	}
	brightness := float32(17)
	reduced := true
	r.path = filepath.Join(t.TempDir(), "missing", "ring.json")
	if err := r.configure(&brightness, &reduced); err == nil || r.snapshot() != (RingOptions{Brightness: 100}) {
		t.Fatal("failed save changed live policy", err)
	}
	r.path = path
	if err := r.configure(&brightness, &reduced); err != nil {
		t.Fatal(err)
	}
	if st, err := os.Stat(path); err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("ring policy is not private", err)
	}
	next, err := loadRingSettings(path)
	if err != nil || next.snapshot() != (RingOptions{Brightness: 17, ReducedMotion: true}) {
		t.Fatal("lost ring policy after reopen", err)
	}
}
