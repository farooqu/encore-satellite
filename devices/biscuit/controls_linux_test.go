package biscuit

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
	"github.com/farooqu/encore-satellite/platforms/linux"
	"github.com/farooqu/encore-satellite/platforms/linux/input"
	"github.com/farooqu/encore-satellite/platforms/linuxsendspin"
)

func TestButtonDiscoveryRequiresBoardHardwareGroups(t *testing.T) {
	for _, tc := range []struct {
		name, gpioName, gpioPhys, keypadName string
		action, wantOK                       bool
	}{
		{"legacy keypad", "keys", "gpio-keys/input0", "mtk-kpd", true, false},
		{"generic keys without identity", "keys", "", "mtk-kpd", false, false},
		{"unrelated keys", "keys", "usb/input0", "mtk-kpd", false, false},
		{"wrong physical input", "keys", "gpio-keys/input1", "mtk-kpd", false, false},
		{"missing keypad", "keys", "gpio-keys/input0", "", false, false},
		{"6.1 board groups", "keys", "", "mtk-pmic-keys", true, true},
		{"6.1 missing Action", "keys", "", "mtk-pmic-keys", false, false},
		{"6.1 missing PMIC", "keys", "", "", true, false},
		{"6.1 missing volume", "volume-keys", "", "mtk-pmic-keys", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			node := func(name, phys string) *input.Device {
				t.Helper()
				path := filepath.Join(t.TempDir(), "events")
				if err := os.WriteFile(path, nil, 0600); err != nil {
					t.Fatal(err)
				}
				d, err := input.Open(path)
				if err != nil {
					t.Fatal(err)
				}
				t.Cleanup(func() { d.Close() })
				d.Name, d.Phys = name, phys
				return d
			}
			unrelated := node("ACCDET", "")
			gpio := node(tc.gpioName, tc.gpioPhys)
			devices := []*input.Device{unrelated, gpio}
			if tc.keypadName != "" {
				devices = append(devices, node(tc.keypadName, ""))
			}
			if tc.action {
				devices = append(devices, node("action_key", ""))
			}
			got, err := buttonDevices(devices)
			if (err == nil) != tc.wantOK {
				t.Fatalf("discovery error = %v, want success %v", err, tc.wantOK)
			}
			if tc.wantOK {
				if len(got) != len(devices)-1 {
					t.Fatal("discovery did not retain exactly the board hardware groups")
				}
				for i, d := range got {
					if d != devices[i+1] {
						t.Fatal("discovery retained an unexpected input device")
					}
				}
			}
			for _, d := range devices {
				_, readErr := d.Read()
				wantClosed := !tc.wantOK || d == unrelated
				if errors.Is(readErr, os.ErrClosed) != wantClosed {
					t.Fatalf("%q closed = %v, want %v (read error %v)", d.Name, errors.Is(readErr, os.ErrClosed), wantClosed, readErr)
				}
			}
		})
	}
}

// Use real evdev records through the production input reader, not direct calls
// to a key mapping helper. Only the event node and audio hardware are virtual.
func buttonEvents(t *testing.T, gate *linux.Privacy, audio func() *buttonAudio, apiAction chan<- struct{}) func(uint16, int32) {
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
	d.Name = "action_key"
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- buttons(ctx, d, gate, audio, apiAction) }()
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

func pairedButtonEvents(t *testing.T, gate *linux.Privacy, engine *session.Engine, player *speaker, music *musicService) func(uint16, int32) {
	a := &buttonAudio{engine: engine, player: player, music: music}
	return buttonEvents(t, gate, func() *buttonAudio { return a }, nil)
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

type buttonPCM struct{ speakerPCM }

func (p *buttonPCM) Write(b []byte) (int, error) {
	// This fake reports zero delay each tick. Accept only one tick's worth
	// of 48 kHz stereo PCM, not an entire playback ring every 2 ms.
	return p.speakerPCM.Write(b[:min(len(b), 96*4)])
}

func buttonMusic(t *testing.T) (*speaker, func() int16) {
	t.Helper()
	p := &buttonPCM{}
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
	buttonWait(t, "initial volume was not 50%", func() bool { return sample() == 6000 })
	return s, sample
}

func TestButtonsVolumeTapHoldReleaseAndLimits(t *testing.T) {
	s, sample := buttonMusic(t)
	e := session.New(s)
	key := pairedButtonEvents(t, linux.NewPrivacy(func() (bool, error) { return false, nil }), e, s, nil)
	key(114, 1)
	buttonWait(t, "minus did not lower speaker gain", func() bool { return sample() == 5900 })
	// This keypad reports only down/up; software must repeat while held.
	buttonWait(t, "held minus did not repeat", func() bool { return sample() <= 5700 })
	key(114, 0)
	time.Sleep(30 * time.Millisecond)
	stopped := sample()
	time.Sleep(250 * time.Millisecond)
	if sample() != stopped {
		t.Fatal("volume kept decreasing after release")
	}
	key(115, 1)
	buttonWait(t, "plus did not raise speaker gain", func() bool { return sample() == stopped+100 })
	buttonWait(t, "held plus did not repeat", func() bool { return sample() >= stopped+300 })
	key(115, 0)
	for i := 0; i < 140; i++ {
		key(115, 1)
		key(115, 0)
	}
	buttonWait(t, "plus exceeded or missed software unity", func() bool { return sample() == 12000 })
	for i := 0; i < 140; i++ {
		key(114, 1)
		key(114, 0)
	}
	buttonWait(t, "minus did not reach silence", func() bool { return sample() == 0 })
}

type buttonLink struct{ commands chan string }

func (*buttonLink) Close() error { return nil }
func (l *buttonLink) Send(c session.Command) error {
	select {
	case l.commands <- c.Kind:
		return nil
	default:
		return errors.New("test peer queue full")
	}
}

func TestButtonsActionSendspinPlayPause(t *testing.T) {
	s, sample := buttonMusic(t)
	e := session.New(s)
	l := &buttonLink{commands: make(chan string, 16)}
	if _, err := e.Attach(l); err != nil {
		t.Fatal(err)
	}
	m := newMusic(Config{Sendspin: true}, nil, s)
	w := &musicWorker{commands: make(chan linuxsendspin.CommandRequest, 1)}
	m.worker = w
	update := func(generation, group uint64, playing, stopped bool, commands uint32) {
		m.mu.Lock()
		defer m.mu.Unlock()
		w.updateReadiness(linuxsendspin.Readiness{Generation: generation, Group: group, Ready: true,
			Commands: commands, Playing: playing, Stopped: stopped})
	}
	const playPause = (1 << (linuxsendspin.Play - 1)) | (1 << (linuxsendspin.Pause - 1))
	key := pairedButtonEvents(t, linux.NewPrivacy(func() (bool, error) { return false, nil }), e, s, m)
	expectMusic := func(command linuxsendspin.Command, generation uint64) {
		t.Helper()
		select {
		case got := <-w.commands:
			if want := (linuxsendspin.CommandRequest{Command: command, Generation: generation}); got != want {
				t.Fatalf("got %+v, want %+v", got, want)
			}
		case voice := <-l.commands:
			t.Fatalf("Action sent voice command %q instead of music command %v", voice, command)
		case <-time.After(time.Second):
			t.Fatal("Action did not send music command", command)
		}
	}
	update(1, 1, true, false, playPause)
	key(138, 1)
	expectMusic(linuxsendspin.Pause, 1)
	key(138, 2)
	key(138, 1)
	// A volume key after the repeated events lets the actual evdev loop drain
	// them before we deliver a Stopped state, with no sleep-based assertion.
	key(114, 1)
	key(114, 0)
	buttonWait(t, "evdev reader did not process queued presses", func() bool {
		steps, _ := s.outputState()
		return steps < 50
	})
	if len(w.commands) != 0 {
		t.Fatal("held Action repeated Pause")
	}
	key(138, 0)
	update(2, 1, false, true, 0)
	key(138, 1) // Unsupported Play is a no-op and does not lose a resumable pause.
	key(138, 0)
	key(115, 1)
	key(115, 0)
	buttonWait(t, "evdev reader did not process unavailable Play", func() bool {
		steps, _ := s.outputState()
		return steps == 50
	})
	if len(w.commands) != 0 || len(l.commands) != 0 {
		t.Fatal("unavailable Play emitted a music or voice command")
	}
	update(3, 1, false, true, playPause)
	key(138, 1)
	expectMusic(linuxsendspin.Play, 3)
	key(138, 0)
	update(4, 1, true, false, 0) // No controller commands, but local Stop can resume.
	s.StopMusic()
	buttonWait(t, "local Stop did not silence music", func() bool { return sample() == 0 })
	key(138, 1)
	buttonWait(t, "Action did not resume local Stop without Play support", func() bool { return sample() == 6000 })
	if len(w.commands) != 0 || len(l.commands) != 0 {
		t.Fatal("local resume sent a group or voice command")
	}
	key(138, 0)
	key(138, 1) // Now audible, but still no Pause support.
	key(138, 0)
	key(114, 1)
	key(114, 0)
	buttonWait(t, "evdev reader did not process unavailable Pause", func() bool {
		steps, _ := s.outputState()
		return steps < 50
	})
	if len(w.commands) != 0 || len(l.commands) != 0 {
		t.Fatal("unavailable Pause emitted a music or voice command")
	}
	update(5, 1, true, false, playPause)
	key(138, 1)
	expectMusic(linuxsendspin.Pause, 5)
	key(138, 0)
	update(6, 1, false, true, playPause)
	update(7, 2, false, true, playPause) // Joining a cold idle group cannot retain Action pause.
	key(138, 1)
	select {
	case voice := <-l.commands:
		if voice != "start" {
			t.Fatalf("idle Action sent %q", voice)
		}
	case <-time.After(time.Second):
		t.Fatal("idle group advertised Play and hijacked Action")
	}
	if len(w.commands) != 0 {
		t.Fatal("Action resumed a different group")
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
	m := newMusic(Config{Sendspin: true}, nil, s)
	w := &musicWorker{commands: make(chan linuxsendspin.CommandRequest, 1), ready: linuxsendspin.Readiness{
		Generation: 1, Group: 1, Ready: true, Playing: true, Commands: 1 << (linuxsendspin.Pause - 1),
	}}
	m.worker = w
	key := pairedButtonEvents(t, linux.NewPrivacy(func() (bool, error) { return false, nil }), e, s, m)
	announced := make(chan error, 1)
	go func() {
		_, err := e.Announce(t.Context(), func(ctx context.Context, _ func([]int16) error) error {
			<-ctx.Done()
			return ctx.Err()
		})
		announced <- err
	}()
	buttonWait(t, "announcement did not acquire foreground", func() bool { return e.Snapshot().Output.Owner == "announcement" })
	key(138, 1)
	key(138, 0)
	select {
	case err := <-announced:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("Action did not cancel announcement: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("Action did not stop announcement ahead of music")
	}
	s.PlayMusic()
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
	m.mu.Lock()
	w.updateReadiness(linuxsendspin.Readiness{Generation: 2, Group: 1, Ready: true, Stopped: true})
	m.mu.Unlock()
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
	buttonWait(t, "Action voice did not duck music", func() bool { return sample() == 1200 })
	m.mu.Lock()
	w.updateReadiness(linuxsendspin.Readiness{Generation: 3, Group: 1, Ready: true, Playing: true, Commands: 1 << (linuxsendspin.Pause - 1)})
	m.mu.Unlock()
	key(138, 0)
	key(138, 1)
	buttonWait(t, "second Action did not stop voice/music", func() bool { return e.Snapshot().Phase == "recovering" && sample() == 0 })
	if command := <-l.commands; command != "cancel" {
		t.Fatalf("active Action sent %q", command)
	}
	if len(w.commands) != 0 {
		t.Fatal("foreground Action sent a music command")
	}
}

func TestButtonsMuteHonorsDriverLagAndBlocksAction(t *testing.T) {
	s, sample := buttonMusic(t)
	e := session.New(s)
	l := &buttonLink{commands: make(chan string, 16)}
	g, err := e.Attach(l)
	if err != nil {
		t.Fatal(err)
	}
	var driver atomic.Bool
	gate := linux.NewPrivacy(func() (bool, error) { return driver.Load(), nil })
	m := newMusic(Config{Sendspin: true}, nil, s)
	w := &musicWorker{commands: make(chan linuxsendspin.CommandRequest, 1), ready: linuxsendspin.Readiness{
		Generation: 1, Group: 1, Ready: true, Playing: true, Commands: 1 << (linuxsendspin.Pause - 1),
	}}
	m.worker = w
	key := pairedButtonEvents(t, gate, e, s, m)
	key(113, 1) // Driver state still reports old unmuted value during its lag.
	buttonWait(t, "mute did not immediately silence speaker/voice", func() bool { return e.Snapshot().Muted && sample() == 0 })
	key(113, 0)
	if err := e.Handle(g, session.Event{Kind: "timer_ringing"}); err != nil {
		t.Fatal(err)
	}
	key(138, 1)
	select {
	case command := <-l.commands:
		if command != "stop_alert" || !e.Snapshot().Muted || e.Snapshot().Alert {
			t.Fatalf("muted Action did not exclusively dismiss the alert: %s %+v", command, e.Snapshot())
		}
	case <-time.After(time.Second):
		t.Fatal("privacy gate blocked alert dismissal")
	}
	key(138, 0)
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
	if len(w.commands) != 0 {
		t.Fatal("muted Action sent a group command")
	}
	select {
	case command := <-l.commands:
		t.Fatalf("muted Action emitted %q", command)
	default:
	}
}

func TestButtonsSwitchFromPairingToVoiceOnFreshAction(t *testing.T) {
	s := newSpeaker(&speakerPCM{})
	t.Cleanup(func() { s.Close() })
	gate := linux.NewPrivacy(func() (bool, error) { return false, nil })
	var audio atomic.Pointer[buttonAudio]
	action := make(chan struct{}, 2)
	key := buttonEvents(t, gate, audio.Load, action)
	key(114, 1) // No pre-pair volume or cue.
	key(138, 1)
	select {
	case <-action:
	case <-time.After(time.Second):
		t.Fatal("unpaired Action did not authorize setup")
	}
	e := session.New(s)
	l := &buttonLink{commands: make(chan string, 2)}
	if _, err := e.Attach(l); err != nil {
		t.Fatal(err)
	}
	audio.Store(&buttonAudio{engine: e, player: s})
	key(138, 1) // A held pairing press must not toggle the new voice session.
	key(138, 0)
	key(138, 1)
	buttonWait(t, "fresh post-pair Action did not start voice", func() bool { return e.Snapshot().Phase == "listening" })
	select {
	case command := <-l.commands:
		if command != "start" {
			t.Fatalf("post-pair Action sent %q, want start", command)
		}
	case <-time.After(time.Second):
		t.Fatal("post-pair Action did not send start")
	}
	if len(action) != 0 {
		t.Fatal("paired Action also opened pairing")
	}
	if volume, _ := s.outputState(); volume != 50 {
		t.Fatalf("pre-pair volume leaked into normal operation: %v", volume)
	}
}

func TestVolumeCuePhysicalPressNotHeldRepeatOrHA(t *testing.T) {
	pcm := &speakerPCM{}
	s := newSpeaker(pcm)
	t.Cleanup(func() { s.Close() })
	if !s.volumeFeedback().at.IsZero() {
		t.Fatal("startup flashed volume feedback")
	}
	if err := s.setOutputVolume(100); err != nil {
		t.Fatal(err)
	}
	e := session.New(s)
	e.SetActivityHandler(s.SetVoiceActive)
	key := pairedButtonEvents(t, linux.NewPrivacy(func() (bool, error) { return false, nil }), e, s, nil)
	nonzero := func() int {
		pcm.mu.Lock()
		defer pcm.mu.Unlock()
		count := 0
		for i := 0; i < len(pcm.written); i += 2 {
			v := int16(binary.LittleEndian.Uint16(pcm.written[i:]))
			if v != 0 {
				count++
			}
			if v < -4400 || v > 4400 {
				t.Fatal("cue exceeded bounded level at software unity", v)
			}
		}
		return count
	}
	// TryCue may legitimately lose admission to the pump mutex. A fresh tap
	// retries the user action; no rejected cue may be deferred until a hold.
	for attempts := 0; nonzero() == 0; attempts++ {
		if attempts == 10 {
			t.Fatal("physical presses never produced an idle cue")
		}
		key(114, 0)
		key(114, 1)
		time.Sleep(70 * time.Millisecond)
	}
	key(114, 2) // Kernel autorepeat is not a new press either.
	key(114, 1) // A duplicate down is not a new press.
	time.Sleep(100 * time.Millisecond)
	audible := nonzero()
	volume, _ := s.outputState()
	time.Sleep(450 * time.Millisecond)
	if now, _ := s.outputState(); now >= volume {
		t.Fatal("held key did not keep adjusting volume")
	}
	if nonzero() != audible {
		t.Fatal("held/repeated key replayed cue")
	}
	key(114, 0)
	if err := s.setOutputVolume(20); err != nil {
		t.Fatal(err)
	}
	time.Sleep(70 * time.Millisecond)
	if nonzero() != audible {
		t.Fatal("HA-style absolute volume change produced a cue")
	}
	if feedback := s.volumeFeedback(); feedback.steps != 24 || feedback.at.IsZero() {
		t.Fatal("HA-style volume change omitted ring feedback", feedback)
	}
	for attempts := 0; nonzero() == audible; attempts++ {
		if attempts == 10 {
			t.Fatal("release/new press did not permit another cue")
		}
		key(115, 0)
		key(115, 1)
		time.Sleep(70 * time.Millisecond)
	}
	key(115, 0)
}

func TestVolumeRingFeedbackRangeExpiryAndPriority(t *testing.T) {
	now := time.Now()
	ready := presentation.State{Connected: true, CaptureReady: true, WakeReady: true, Night: "off", Phase: "idle"}
	options := RingOptions{Brightness: 100}
	feedback := volumeFeedback{steps: 60, at: now}
	var frame [36]byte
	r := &ringRenderer{volume: func() volumeFeedback { return feedback }, write: func(b []byte) error {
		_, err := hex.Decode(frame[:], b[:72])
		return err
	}}
	draw := func(s presentation.State, at time.Time) {
		t.Helper()
		if err := r.update(s, captureReading{}, at, options); err != nil {
			t.Fatal(err)
		}
	}
	draw(ready, now)
	for i := range 12 {
		want := [3]byte{0, 0, 1}
		if i < 6 {
			want = [3]byte{112, 112, 112}
		}
		if got := [3]byte(frame[i*3 : i*3+3]); got != want {
			t.Fatalf("half range segment %d got %v want %v", i, got, want)
		}
	}
	draw(ready, now.Add(1199*time.Millisecond))
	if frame == [36]byte{} {
		t.Fatal("volume feedback expired early")
	}
	draw(ready, now.Add(1200*time.Millisecond))
	if frame != [36]byte{} {
		t.Fatal("volume feedback did not expire")
	}
	for _, block := range []func(*presentation.State){
		func(s *presentation.State) { s.Muted = true },
		func(s *presentation.State) { s.Connected = false },
		func(s *presentation.State) { s.Alert = true },
		func(s *presentation.State) { s.MonitorID = 1 },
		func(s *presentation.State) { s.Inhibited = true },
	} {
		now = now.Add(2 * time.Second)
		feedback.at = now
		s := ready
		block(&s)
		draw(s, now)
		if frame != RingFrame(s, 0, 0, options) {
			t.Fatal("volume replaced priority status", s, frame)
		}
		draw(ready, now.Add(50*time.Millisecond))
		if frame != [36]byte{} {
			t.Fatal("hidden volume replayed after priority status")
		}
	}
	for _, night := range []string{"off", "on", "unknown"} {
		for _, brightness := range []int{1, 19, 25, 26, 50, 51, 75, 76, 100} {
			for _, steps := range []int32{0, 1, 7, 60, 119, 120} {
				for _, muted := range []bool{false, true} {
					now = now.Add(2 * time.Second)
					feedback = volumeFeedback{steps: steps, muted: muted, at: now}
					ready.Night, options.Brightness = night, brightness
					draw(ready, now)
					for i := range 12 {
						a, b, c := frame[i*3], frame[i*3+1], frame[i*3+2]
						if a > 244 || b > 244 || c > 244 || int(a)+int(b)+int(c) > 366 || (night == "on" && max(a, b, c) > 8) || (night == "unknown" && max(a, b, c) > 192) {
							t.Fatal("volume frame exceeded LED caps", frame)
						}
						if (steps == 0 || muted) && [3]byte{a, b, c} != [3]byte{0, 0, 1} {
							t.Fatal("zero/mute did not show empty track")
						}
						if steps == 120 && !muted && brightness == 100 && a == 0 {
							t.Fatal("maximum did not fill the whole ring")
						}
					}
				}
			}
		}
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
	monitoring := ready
	monitoring.MonitorID = 1
	if frame := at(monitoring, 0, 0); frame[0] <= frame[3] || frame[3] == 0 || frame[2] == 0 {
		t.Fatal("recording indicator is not distinct from idle or mute", frame)
	}
	monitoring.Night = "on"
	if frame := RingFrame(monitoring, 0, 0, RingOptions{Brightness: 1}); frame[2] != 1 || frame[5] != 0 {
		t.Fatal("recording indicator lost its distinction at minimum brightness", frame)
	}
	monitoring.Muted = true
	if frame := at(monitoring, 0, 0); frame[0] == 0 || frame[0] != frame[3] || frame[2] != 0 {
		t.Fatal("recording indicator replaced privacy red", frame)
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
		for _, brightness := range []int{1, 19, 25, 26, 50, 51, 75, 76, 100} {
			for _, output := range []string{"", "queued", "playing", "draining", "failed"} {
				for milliseconds := 0; milliseconds < 3200; milliseconds += 50 {
					s := presentation.State{Connected: true, CaptureReady: true, Active: true, Phase: "working", Night: night, Output: presentation.Output{State: output}, Speaker: presentation.Speaker{RGB: [3]byte{255, 255, 255}, ColorKnown: true}}
					frame := RingFrame(s, 1, time.Duration(milliseconds)*time.Millisecond, RingOptions{Brightness: brightness})
					for i := range 12 {
						sum := 0
						for _, channel := range frame[i*3 : i*3+3] {
							if channel > 244 || (night == "on" && channel > 8) || (night == "unknown" && channel > 192) {
								t.Fatal("channel exceeded software/night cap", frame)
							}
							sum += int(channel)
						}
						if sum == 0 || sum > 366 {
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

func TestRingStockBrightnessAndCurrentTransitions(t *testing.T) {
	var frame [36]byte
	var current byte
	writes, changes := 0, 0
	r := &ringRenderer{
		write: func(b []byte) error {
			writes++
			_, err := hex.Decode(frame[:], b[:72])
			return err
		},
		setCurrent: func(value byte) error {
			if frame != [36]byte{} {
				t.Fatal("changed current with an old PWM frame still lit", frame)
			}
			changes++
			current = value
			return nil
		},
	}
	// Privacy red is an unattenuated source color, independent of animation
	// envelopes. Expectations come from the OEM table, including both sides
	// of each current-band transition, not from the implementation's table.
	s := presentation.State{Muted: true, Night: "off"}
	for _, tc := range []struct {
		brightness int
		current    byte
		pwm        byte
	}{
		{1, 3, 34}, {10, 3, 60}, {25, 3, 104}, {26, 2, 82},
		{50, 2, 192}, {51, 1, 131}, {75, 1, 244}, {76, 0, 125}, {100, 0, 223},
	} {
		options := RingOptions{Brightness: tc.brightness}
		if err := r.update(s, captureReading{}, time.Now(), options); err != nil {
			t.Fatal(err)
		}
		for i := range 12 {
			if current != tc.current || [3]byte(frame[i*3:i*3+3]) != [3]byte{tc.pwm, 0, 0} {
				t.Fatalf("brightness %d: current %d frame %v", tc.brightness, current, frame)
			}
		}
		beforeWrites, beforeChanges := writes, changes
		if err := r.update(s, captureReading{}, time.Now(), options); err != nil || writes != beforeWrites || changes != beforeChanges {
			t.Fatal("unchanged current/frame was not coalesced", err)
		}
	}
	for _, tc := range []struct {
		night      string
		brightness int
		current    byte
		pwm        byte
	}{
		{"on", 100, 3, 8},
		{"unknown", 100, 2, 192},
		{"unknown", 25, 3, 104},
		{"unknown", 50, 2, 192},
		{"unknown", 51, 2, 192},
		{"", 100, 2, 192},
		{"off", 100, 0, 223},
	} {
		s.Night = tc.night
		if err := r.update(s, captureReading{}, time.Now(), RingOptions{Brightness: tc.brightness}); err != nil || current != tc.current || frame[0] != tc.pwm {
			t.Fatal("night transition did not apply brightness policy", tc, current, frame, err)
		}
	}
}

func TestRingDriveWriteFailureDoesNotAdvanceOutput(t *testing.T) {
	for _, failure := range []string{"blank", "current", "frame"} {
		t.Run(failure, func(t *testing.T) {
			var frame [36]byte
			current := byte(3)
			fail := false
			boom := errors.New("driver write failed")
			r := &ringRenderer{
				write: func(b []byte) error {
					var next [36]byte
					if _, err := hex.Decode(next[:], b[:72]); err != nil {
						return err
					}
					if fail && ((failure == "blank" && next == [36]byte{}) || (failure == "frame" && next != [36]byte{})) {
						return boom
					}
					frame = next
					return nil
				},
				setCurrent: func(value byte) error {
					if frame != [36]byte{} {
						t.Fatal("current change while lit")
					}
					if fail && failure == "current" {
						return boom
					}
					current = value
					return nil
				},
			}
			s := presentation.State{Muted: true, Night: "on"}
			options := RingOptions{Brightness: 100}
			if err := r.update(s, captureReading{}, time.Now(), options); err != nil {
				t.Fatal(err)
			}
			s.Night, fail = "off", true
			if err := r.update(s, captureReading{}, time.Now(), options); !errors.Is(err, boom) {
				t.Fatal("lost driver error", err)
			}
			if failure == "blank" && (current != 3 || frame[0] != 8) {
				t.Fatal("failed blank still changed drive", current, frame)
			}
			if failure != "blank" && frame != [36]byte{} {
				t.Fatal("failed transition left LEDs lit", current, frame)
			}
			fail = false
			if err := r.update(s, captureReading{}, time.Now(), options); err != nil || current != 0 || frame[0] != 223 {
				t.Fatal("retry did not apply complete stock output", current, frame, err)
			}
		})
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
