package biscuit

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/backends/incubator"
	"github.com/farooqu/encore-satellite/core/session"
	"github.com/farooqu/encore-satellite/platforms/linux"
	"github.com/farooqu/encore-satellite/platforms/linux/alsa"
)

type silentPlayer struct{}

func (silentPlayer) Write(context.Context, []int16) error { return nil }
func (silentPlayer) Drain(context.Context) error          { return nil }
func (silentPlayer) Flush()                               {}

type recordingLink struct {
	commands []session.Command
	closed   bool
}

func (*recordingLink) Backend() session.Backend { return session.Incubator }
func (l *recordingLink) Close() error           { l.closed = true; return nil }
func (l *recordingLink) Send(c session.Command) error {
	c.Samples = append([]int16(nil), c.Samples...)
	l.commands = append(l.commands, c)
	// Native Incubator now retires its voice-only connection after flushing
	// cancel; the shared engine no longer closes a possibly shared transport.
	if c.Kind == "cancel" {
		return l.Close()
	}
	return nil
}

type captureStub struct {
	starts, reads, rewinds int
	duringRead             func()
	readErr                error
	buffer                 []byte
}

func (s *captureStub) Start() error  { s.starts++; return nil }
func (s *captureStub) Rewind() error { s.rewinds++; return nil }
func (s *captureStub) Read(b []byte) (int, error) {
	s.reads++
	s.buffer = b
	for i := 0; i < 2; i++ {
		for ch := range 9 {
			// The seven microphones average to 400, then -800 PCM16;
			// center-only capture and either reference give other values.
			x := int32(100*(ch+1)*(1-3*i)) << 8
			for j := range 3 {
				b[i*27+ch*3+j] = byte(x >> uint(j*8))
			}
		}
	}
	if s.duringRead != nil {
		s.duringRead()
	}
	return 54, s.readErr
}

func TestCapturePrivacyAtOSBoundaryAndAfterRead(t *testing.T) {
	for _, scenario := range []string{"open", "initially_muted", "mute_during_read", "driver_error", "driver_error_during_read"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			e := session.New(silentPlayer{})
			l := &recordingLink{}
			if _, err := e.Attach(l); err != nil {
				t.Fatal(err)
			}
			if err := e.StartManual(); err != nil {
				t.Fatal(err)
			}
			var muted atomic.Bool
			muted.Store(scenario == "initially_muted")
			unreadable := scenario == "driver_error"
			gate := linux.NewPrivacy(func() (bool, error) {
				if unreadable {
					return false, errors.New("state unavailable")
				}
				return muted.Load(), nil
			})
			pcm := &captureStub{}
			if scenario == "mute_during_read" {
				pcm.duringRead = func() { muted.Store(true) }
			} else if scenario == "driver_error_during_read" {
				pcm.duringRead = func() { unreadable = true }
			}
			ready := false
			meter := &captureMeter{origin: time.Now()}
			err := capture(ctx, pcm, func() (bool, error) { return false, nil }, gate, &incubator.Server{Engine: e}, meter, func() {
				reading := meter.snapshot(time.Now())
				if reading.ready != (scenario == "open") || (reading.ready && reading.level <= 0) {
					t.Fatal("visual observation escaped capture/privacy boundary", reading)
				}
				ready = true
				cancel()
			})
			if meter.snapshot(time.Now()).ready {
				t.Fatal("capture exit retained visual observation")
			}
			if err != nil || !ready {
				t.Fatalf("capture: %v app ready=%v", err, ready)
			}
			var audioCommands []session.Command
			for _, c := range l.commands {
				if c.Kind == "audio" {
					audioCommands = append(audioCommands, c)
				}
			}
			if scenario == "open" {
				if len(audioCommands) != 1 {
					t.Fatalf("audio commands: %d", len(audioCommands))
				}
				got := audioCommands[0].Samples
				if len(got) != 2 || got[0] != 400 || got[1] != -800 {
					t.Fatalf("capture PCM %v", got)
				}
			} else {
				if len(audioCommands) != 0 || !e.Snapshot().Muted || !l.closed {
					t.Fatal("muted capture escaped OS boundary")
				}
				wantReads := 0
				if scenario == "mute_during_read" || scenario == "driver_error_during_read" {
					wantReads = 1
				}
				if pcm.starts != wantReads || pcm.reads != wantReads {
					t.Fatalf("PCM starts=%d reads=%d, want %d each", pcm.starts, pcm.reads, wantReads)
				}
				for _, b := range pcm.buffer {
					if b != 0 {
						t.Fatal("capture retained discarded audio")
					}
				}
				if pcm.rewinds != wantReads {
					t.Fatal("pending kernel audio not discarded")
				}
			}
		})
	}
}

func TestCaptureRecoversFromUnreadablePrivacyWithoutRestart(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	engine := session.New(silentPlayer{})
	pcm := &captureStub{}
	meter := &captureMeter{origin: time.Now()}
	state := "unreadable"
	gate := linux.NewPrivacy(func() (bool, error) {
		if state == "unreadable" {
			return false, errors.New("state unavailable")
		}
		return state == "muted", nil
	})
	resumed := false
	err := capture(ctx, pcm, func() (bool, error) { return false, nil }, gate, &incubator.Server{Engine: engine}, meter, func() {
		if state != "unreadable" || !engine.Snapshot().Muted || pcm.starts != 0 || pcm.reads != 0 {
			t.Fatal("app must start with no PCM activity while privacy is unreadable")
		}
		state = "muted"
	}, func(muted bool) {
		if state == "muted" {
			if !muted || !engine.Snapshot().Muted || pcm.starts != 0 || pcm.reads != 0 {
				t.Fatal("a readable muted state must not resume capture")
			}
			state = "unmuted"
		} else if !muted && meter.snapshot(time.Now()).ready {
			resumed = !engine.Snapshot().Muted
			cancel()
		}
	})
	if err != nil || !resumed || pcm.starts != 1 {
		t.Fatalf("capture did not resume: %v resumed=%v starts=%d", err, resumed, pcm.starts)
	}
}

type lossControlsStub struct {
	pending       [2]uint32
	lookups, gets []string
	lookupErr     string
	getErr        string
	malformed     bool
	invalid       bool
}

func (m *lossControlsStub) Lookup(name string) (alsa.Control, error) {
	m.lookups = append(m.lookups, name)
	if name == m.lookupErr {
		return alsa.Control{}, errors.New("missing control")
	}
	id := uint32(0)
	if name == "Kernel Overruns" {
		id = 1
	}
	c := alsa.Control{Numid: id, Name: name, Type: alsa.TypeInteger, Count: 1}
	if m.invalid {
		c.Count = 2
	}
	return c, nil
}

func (m *lossControlsStub) Get(c alsa.Control) ([]uint32, error) {
	m.gets = append(m.gets, c.Name)
	if c.Name == m.getErr {
		return nil, errors.New("control read failed")
	}
	if m.malformed {
		return nil, nil
	}
	v := m.pending[c.Numid]
	m.pending[c.Numid] = 0 // the real controls are read-and-clear
	return []uint32{v}, nil
}

func TestCaptureLossMonitorReadAndClear(t *testing.T) {
	m := &lossControlsStub{}
	loss, err := captureLossMonitor(m)
	if err != nil {
		t.Fatal(err)
	}
	if len(m.lookups) != 2 || m.lookups[0] != "FPGA Overruns" || m.lookups[1] != "Kernel Overruns" {
		t.Fatalf("control lookups: %v", m.lookups)
	}
	for _, counter := range []int{0, 1} {
		for _, report := range []uint32{1, 1, 0, 2, 2, 0} {
			m.pending[counter] = report
			got, err := loss()
			if err != nil || got != (report != 0) {
				t.Fatalf("counter=%d report=%d lost=%v err=%v", counter, report, got, err)
			}
		}
	}
	if len(m.gets) != 24 {
		t.Fatalf("must consume both counters at every check: %v", m.gets)
	}
	for _, m := range []*lossControlsStub{{lookupErr: "FPGA Overruns"}, {lookupErr: "Kernel Overruns"}, {invalid: true}} {
		if _, err := captureLossMonitor(m); err == nil {
			t.Fatal("accepted unavailable or malformed control")
		}
	}
}

func TestCaptureLossDiscardsRewindsAndMarksRecovery(t *testing.T) {
	for _, scenario := range []string{"fpga_during_read", "kernel_during_read", "equal_nonzero_reports", "between_reads", "alsa_overrun", "loss_with_eagain", "loss_with_eintr"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			e := session.New(silentPlayer{})
			initial, recovered := &recordingLink{}, &recordingLink{}
			generation, err := e.Attach(initial)
			if err != nil {
				t.Fatal(err)
			}
			if err := e.StartManual(); err != nil {
				t.Fatal(err)
			}
			m := &lossControlsStub{}
			loss, err := captureLossMonitor(m)
			if err != nil {
				t.Fatal(err)
			}
			wantReads, wantRewinds := 3, 1
			if scenario == "equal_nonzero_reports" {
				wantReads, wantRewinds = 4, 2
			} else if scenario == "between_reads" {
				wantReads = 2
			}
			meter := &captureMeter{origin: time.Now()}
			pcm := &captureStub{}
			pcm.duringRead = func() {
				pcm.readErr = nil
				if pcm.reads == wantReads {
					if meter.snapshot(time.Now()).ready {
						t.Fatal("capture loss retained a healthy microphone observation")
					}
					if !initial.closed || pcm.rewinds != wantRewinds {
						t.Fatal("loss did not immediately close speech and rewind")
					}
					// A replacement connection must also see the recovery
					// Discontinuity, not receive the first post-gap block.
					// The native adapter reports disconnect after flushing cancel.
					e.Disconnect(generation, "cancelled")
					if _, err := e.Attach(recovered); err != nil {
						t.Fatal(err)
					}
					if err := e.StartManual(); err != nil {
						t.Fatal(err)
					}
					cancel()
				} else if pcm.reads > 1 {
					switch scenario {
					case "kernel_during_read":
						m.pending[1] = 1
					case "alsa_overrun":
						pcm.readErr = alsa.ErrOverrun
					default:
						m.pending[0] = 1
						if scenario == "loss_with_eagain" {
							pcm.readErr = syscall.EAGAIN
						} else if scenario == "loss_with_eintr" {
							pcm.readErr = syscall.EINTR
						}
					}
				}
			}
			ready := false
			err = capture(ctx, pcm, loss, linux.NewPrivacy(func() (bool, error) { return false, nil }), &incubator.Server{Engine: e}, meter, func() {
				ready = true
				if !meter.snapshot(time.Now()).ready {
					t.Fatal("successful capture did not publish a microphone observation")
				}
				if scenario == "between_reads" {
					m.pending[0] = 1
				}
			})
			if err != nil || !ready || pcm.reads != wantReads || pcm.rewinds != wantRewinds || pcm.starts != wantRewinds+1 {
				t.Fatalf("capture=%v ready=%v PCM=%+v", err, ready, pcm)
			}
			if !recovered.closed {
				t.Fatal("recovery frame did not carry Discontinuity")
			}
			count := 0
			for _, link := range []*recordingLink{initial, recovered} {
				for _, c := range link.commands {
					if c.Kind == "audio" {
						count++
					}
				}
			}
			if count != 1 {
				t.Fatalf("gap audio escaped: %d blocks, want only initial clean block", count)
			}
			for _, b := range pcm.buffer {
				if b != 0 {
					t.Fatal("capture retained discarded audio")
				}
			}
		})
	}
}

func TestCaptureLossControlReadErrorsFailClosed(t *testing.T) {
	for _, scenario := range []string{"fpga_before", "kernel_before", "fpga_after", "kernel_after", "malformed_after", "muted_before"} {
		t.Run(scenario, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			e := session.New(silentPlayer{})
			link := &recordingLink{}
			if _, err := e.Attach(link); err != nil {
				t.Fatal(err)
			}
			if err := e.StartManual(); err != nil {
				t.Fatal(err)
			}
			m := &lossControlsStub{}
			fail := func() {
				m.getErr = "FPGA Overruns"
				if scenario == "kernel_before" || scenario == "kernel_after" {
					m.getErr = "Kernel Overruns"
				} else if scenario == "malformed_after" {
					m.getErr, m.malformed = "", true
				}
			}
			pcm := &captureStub{}
			wantReads := 0
			if scenario == "fpga_after" || scenario == "kernel_after" || scenario == "malformed_after" {
				pcm.duringRead = fail
				wantReads = 1
			} else {
				fail()
			}
			loss, err := captureLossMonitor(m)
			if err != nil {
				t.Fatal(err)
			}
			ready := false
			err = capture(ctx, pcm, loss, linux.NewPrivacy(func() (bool, error) { return scenario == "muted_before", nil }), &incubator.Server{Engine: e}, &captureMeter{origin: time.Now()}, func() { ready = true; cancel() })
			if err == nil || ready || !link.closed || !e.Snapshot().Muted || pcm.reads != wantReads {
				t.Fatalf("did not fail closed: err=%v ready=%v closed=%v muted=%v reads=%d", err, ready, link.closed, e.Snapshot().Muted, pcm.reads)
			}
			for _, c := range link.commands {
				if c.Kind == "audio" {
					t.Fatal("audio escaped an unreadable loss control")
				}
			}
		})
	}
}

type speakerPCM struct {
	mu      sync.Mutex
	written []byte
}

func (p *speakerPCM) Write(b []byte) (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.written = append(p.written, b...)
	return len(b), nil
}
func (*speakerPCM) Drop() error         { return nil }
func (*speakerPCM) Drain() error        { return nil }
func (*speakerPCM) Close() error        { return nil }
func (*speakerPCM) Delay() (int, error) { return 0, nil }

type recoveringSpeakerPCM struct {
	speakerPCM
	once             sync.Once
	entered, release chan struct{}
}

func (p *recoveringSpeakerPCM) Delay() (int, error) {
	var err error
	p.once.Do(func() { err = syscall.EPIPE })
	return 0, err
}

func (p *recoveringSpeakerPCM) Drop() error {
	close(p.entered)
	<-p.release
	return nil
}

func TestUnmutedCaptureContinuesDuringPlaybackRecovery(t *testing.T) {
	pcm := &recoveringSpeakerPCM{entered: make(chan struct{}), release: make(chan struct{})}
	// No gain write here: the real callback must run while recovery owns the lock.
	player := linux.NewMixer(pcm, speakerLeadFrames)
	defer player.Close()
	defer close(pcm.release)
	select {
	case <-pcm.entered:
	case <-time.After(time.Second):
		t.Fatal("playback did not enter recovery")
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	mic := &captureStub{}
	mic.duringRead = func() {
		if mic.reads == 2 {
			cancel()
		}
	}
	gate := linux.NewPrivacy(func() (bool, error) { return false, nil })
	voice := &incubator.Server{Engine: session.New(player)}
	done := make(chan error, 1)
	go func() {
		// Exercise a real privacy transition, not only the no-op fast path.
		player.SetMusicPrivacy(true)
		voice.Engine.SetMuted(true)
		done <- capture(ctx, mic, func() (bool, error) { return false, nil }, gate, voice, &captureMeter{origin: time.Now()}, func() {}, player.SetMusicPrivacy)
	}()
	select {
	case err := <-done:
		if err != nil || mic.reads != 2 {
			t.Fatalf("capture did not reach the second read: reads=%d, err=%v", mic.reads, err)
		}
	case <-time.After(time.Second):
		t.Fatal("unmuted capture waited on playback recovery")
	}
}

func TestNativeVolumeAndMuteApplyToVoicePCM(t *testing.T) {
	for _, tc := range []struct {
		name    string
		percent float32
		muted   bool
		want    int16
	}{{"quarter", 25, false, 1000}, {"startup-level", 50, false, 2000}, {"output-muted", 100, true, 0}, {"unity", 100, false, 4000}} {
		t.Run(tc.name, func(t *testing.T) {
			pcm := &speakerPCM{}
			s := newSpeaker(pcm)
			defer s.Close()
			if err := s.setOutputVolume(tc.percent); err != nil {
				t.Fatal(err)
			}
			s.setOutputMute(tc.muted)
			ctx, cancel := context.WithTimeout(t.Context(), time.Second)
			defer cancel()
			samples := make([]int16, 320)
			for i := range samples {
				samples[i] = 4000
			}
			if err := s.Write(ctx, samples); err != nil {
				t.Fatal(err)
			}
			if err := s.Drain(ctx); err != nil {
				t.Fatal(err)
			}
			pcm.mu.Lock()
			defer pcm.mu.Unlock()
			// The pump can emit idle silence before Write queues voice. Find
			// the signal, then ignore resampler edges and check its steady
			// middle, not a scheduling-dependent absolute recording offset.
			start := 0
			if !tc.muted {
				for start < len(pcm.written) && binary.LittleEndian.Uint16(pcm.written[start:]) == 0 {
					start += 4
				}
			}
			if len(pcm.written) < start+800*4 {
				t.Fatal("missing rendered voice PCM")
			}
			for i := start + 300*4; i < start+800*4; i += 2 {
				if got := int16(binary.LittleEndian.Uint16(pcm.written[i:])); got != tc.want {
					t.Fatalf("voice PCM got %d want %d", got, tc.want)
				}
			}
		})
	}
}

func TestSpeakerFormatAndIndependentStartupMaximum(t *testing.T) {
	p := &speakerPCM{}
	s := newSpeaker(p)
	defer s.Close()
	if volume, muted := s.outputState(); volume != 50 || muted {
		t.Fatal("startup must select 50% independently of the maximum", volume, muted)
	}
	s.MusicStart(1, nil)
	if err := s.MusicWrite(context.Background(), 1, []byte{0xff, 0x7f, 0, 0x80}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	p.mu.Lock()
	initial := false
	for i := 0; i < len(p.written); i += 4 {
		if int16(binary.LittleEndian.Uint16(p.written[i:])) == 16384 && int16(binary.LittleEndian.Uint16(p.written[i+2:])) == -16384 {
			initial = true
		}
	}
	p.written = nil
	p.mu.Unlock()
	if !initial {
		t.Fatal("missing initial 50% stereo PCM16 gain")
	}
	s.adjust(100)
	if volume, _ := s.outputState(); volume != 100 {
		t.Fatal("keys must reach unity, independently of startup", volume)
	}
	if err := s.MusicWrite(context.Background(), 1, []byte{0xff, 0x7f, 0, 0x80}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	p.mu.Lock()
	found := false
	for i := 0; i < len(p.written); i += 4 {
		if int16(binary.LittleEndian.Uint16(p.written[i:])) == 32767 && int16(binary.LittleEndian.Uint16(p.written[i+2:])) == -32768 {
			found = true
		}
	}
	p.mu.Unlock()
	if !found {
		t.Fatal("unity must preserve both signed full-scale PCM16 polarities without wrap")
	}
	s.adjust(-200)
	if volume, _ := s.outputState(); volume != 0 {
		t.Fatal("volume below zero")
	}
	p.mu.Lock()
	p.written = nil
	p.mu.Unlock()
	if err := s.MusicWrite(context.Background(), 1, []byte{0xff, 0x7f, 0, 0x80}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, v := range p.written {
		if v != 0 {
			t.Fatal("software mute not silent")
		}
	}
}
