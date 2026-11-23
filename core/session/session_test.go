package session_test

import (
	"context"
	"errors"
	"slices"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/core/presentation"
	"github.com/farooqu/encore-satellite/core/session"
	"github.com/farooqu/encore-satellite/core/wake"
)

type link struct {
	commands []session.Command
	closed   bool
	fail     bool
}

func (l *link) Close() error { l.closed = true; return nil }
func (l *link) Send(c session.Command) error {
	if l.fail {
		return errors.New("queue full")
	}
	c.Samples = slices.Clone(c.Samples)
	l.commands = append(l.commands, c)
	return nil
}

type player struct {
	writes                     int
	flushed                    int
	entered, release           chan struct{}
	holdCancelled              bool
	writeEntered, writeRelease chan struct{}
	writeErr                   error
}

func (p *player) Write(ctx context.Context, samples []int16) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if p.writeEntered != nil {
		close(p.writeEntered)
		<-p.writeRelease
	}
	if p.writeErr != nil {
		return p.writeErr
	}
	p.writes += len(samples)
	return nil
}
func (p *player) Flush() { p.flushed++ }
func (p *player) Drain(ctx context.Context) error {
	if p.entered != nil {
		close(p.entered)
		select {
		case <-ctx.Done():
			if p.holdCancelled {
				<-p.release // Model cleanup finishing after a new turn starts.
			}
			return ctx.Err()
		case <-p.release:
		}
	}
	return nil
}

func fixture(t *testing.T) (*session.Engine, *link, *player, uint64) {
	t.Helper()
	p, l := &player{}, &link{}
	e := session.New(p)
	g, err := e.Attach(l)
	if err != nil {
		t.Fatal(err)
	}
	return e, l, p, g
}

func TestRemoteCaptureFollowsExactIndicator(t *testing.T) {
	e, peer, _, generation := fixture(t)
	old, err := e.SetMonitor(generation, true, false, 1)
	if err != nil || old == 0 {
		t.Fatalf("first monitor: id=%d err=%v", old, err)
	}
	report := wake.Report{Detected: []string{"model"}}
	if err := e.ReportWake(generation, old, report); !errors.Is(err, session.ErrUnavailable) {
		t.Fatal("unconfirmed monitor accepted telemetry", err)
	}
	e.ConfirmMonitor(old)
	if err := e.ReportWake(generation, old, report); err != nil {
		t.Fatal("confirmed monitor rejected telemetry", err)
	}
	id, err := e.SetMonitor(generation, true, true, 2)
	if err != nil || id == 0 || id == old {
		t.Fatalf("replacement monitor: id=%d err=%v", id, err)
	}
	var seq uint64
	feed := func(sample int16) {
		t.Helper()
		seq++
		if err := e.Capture(audio.Frame{Sequence: seq, CapturedAt: time.Duration(seq) * time.Millisecond, Samples: []int16{sample}}); err != nil {
			t.Fatal(err)
		}
	}
	e.ConfirmMonitor(old)
	feed(37)
	e.ConfirmMonitor(id)
	if err := e.ReportWake(generation, old, report); !errors.Is(err, session.ErrStale) {
		t.Fatal("replaced monitor accepted a late inference", err)
	}
	if err := e.ReportWake(generation, id, report); !errors.Is(err, session.ErrUnavailable) {
		t.Fatal("intercom accepted telemetry", err)
	}
	e.HideMonitor(old)
	feed(61)
	e.HideMonitor(id)
	e.ConfirmMonitor(id)
	feed(89)
	var sent []int16
	for _, cmd := range peer.commands {
		if cmd.Kind == "monitor_audio" {
			sent = append(sent, cmd.Samples...)
		}
	}
	if !slices.Equal(sent, []int16{61}) || e.Snapshot().MonitorID != 0 {
		t.Fatalf("remote audio sent %v, monitor=%d; want only the visible request's live sample", sent, e.Snapshot().MonitorID)
	}
}

func TestVoiceActivityCoversListeningThroughPhysicalDrain(t *testing.T) {
	p := &player{entered: make(chan struct{}), release: make(chan struct{})}
	e := session.New(p)
	g, err := e.Attach(&link{})
	if err != nil {
		t.Fatal(err)
	}
	var active atomic.Bool
	e.SetActivityHandler(active.Store)
	if active.Load() {
		t.Fatal("idle voice claims output")
	}
	if err := e.Wake(wake.Detection{Word: "lab-wake"}); err != nil {
		t.Fatal(err)
	}
	if !active.Load() {
		t.Fatal("microphone listening did not claim ducking")
	}
	_ = e.Handle(g, session.Event{Kind: "listening"})
	_ = e.Handle(g, session.Event{Kind: "processing"})
	if !active.Load() {
		t.Fatal("processing released ducking")
	}
	_ = e.Output(g, []int16{307, -891})
	_ = e.Handle(g, session.Event{Kind: "hangup"})
	done := make(chan error, 1)
	go func() { done <- e.EndOutput(g) }()
	<-p.entered
	if !active.Load() {
		t.Fatal("wire end released ducking before physical drain")
	}
	if s := e.Snapshot(); !s.Active || s.Output.State != "draining" || s.Output.Owner != "voice" || !s.Output.Received || s.TalkAllowed {
		t.Fatal("draining output was hidden by wire completion", s)
	}
	close(p.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if active.Load() {
		t.Fatal("finished turn retained ducking")
	}
	if s := e.Snapshot(); s.Active || s.Output.State != "" {
		t.Fatal("drained output retained presentation ownership", s)
	}
	_ = e.StartManual()
	_ = e.Stop()
	if active.Load() {
		t.Fatal("cancelled turn retained ducking")
	}
}

func TestVoiceActivityKeepsOtherOwnersAndFencesOldConnection(t *testing.T) {
	e, _, _, g := fixture(t)
	var active atomic.Bool
	e.SetActivityHandler(active.Store)
	_ = e.StartManual()
	_ = e.Handle(g, session.Event{Kind: "timer_ringing"})
	_ = e.Handle(g, session.Event{Kind: "hangup", Reason: "wake_rejected"})
	if !active.Load() {
		t.Fatal("rejection released an active timer")
	}
	_ = e.Handle(g, session.Event{Kind: "timer_stopped"})
	if active.Load() {
		t.Fatal("last owner did not release ducking")
	}
	e.Disconnect(g, "connection_closed")
	ng, err := e.Attach(&link{})
	if err != nil {
		t.Fatal(err)
	}
	_ = e.StartManual()
	e.Disconnect(g, "old reader")
	if !active.Load() {
		t.Fatal("old connection released current turn")
	}
	e.Disconnect(ng, "current reader")
	if active.Load() {
		t.Fatal("disconnect retained activity")
	}
}

func TestTimerStopRecoversWithoutStreamEndAndKeepsCallFence(t *testing.T) {
	for _, tt := range []struct {
		name                                        string
		callActive, announcementActive, streamEnded bool
	}{
		{name: "timer only"},
		{name: "timer overlaps call", callActive: true},
		{name: "timer overlaps announcement", announcementActive: true},
		{name: "timer ended before acknowledgement and next call cancelled", streamEnded: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			e, l, p, g := fixture(t)
			if tt.callActive {
				if err := e.StartManual(); err != nil {
					t.Fatal(err)
				}
				_ = e.Handle(g, session.Event{Kind: "listening"})
			}
			if tt.announcementActive {
				if err := e.Handle(g, session.Event{Kind: "announcement", Active: true}); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.Handle(g, session.Event{Kind: "timer_ringing"}); err != nil {
				t.Fatal(err)
			}
			if err := e.Output(g, []int16{123, -789}); err != nil {
				t.Fatal(err)
			}
			if err := e.Stop(); err != nil {
				t.Fatal(err)
			}
			// Repeated Stop must not forget the outstanding cancellation.
			if err := e.Stop(); err != nil {
				t.Fatal(err)
			}
			if err := e.StartManual(); !errors.Is(err, session.ErrUnavailable) {
				t.Fatal("new capture accepted before upstream cancellation", err)
			}
			if err := e.Output(g, []int16{999}); err != nil {
				t.Fatal(err)
			}
			if p.writes != 2 {
				t.Fatal("late timer audio was played")
			}
			if tt.streamEnded {
				if err := e.EndOutput(g); err != nil {
					t.Fatal(err)
				}
				if err := e.StartManual(); err != nil {
					t.Fatal(err)
				}
				if err := e.Stop(); err != nil {
					t.Fatal(err)
				}
			}
			// Real upstream cancellation can send only this, no AUDIO_OUT_END.
			if err := e.Handle(g, session.Event{Kind: "timer_stopped"}); err != nil {
				t.Fatal(err)
			}
			if tt.callActive || tt.announcementActive || tt.streamEnded {
				if err := e.StartManual(); !errors.Is(err, session.ErrUnavailable) {
					t.Fatal("timer completion released unrelated cancelled output", err)
				}
				if err := e.EndOutput(g); err != nil {
					t.Fatal(err)
				}
			}
			if err := e.Output(g, []int16{777}); err != nil {
				t.Fatal(err)
			}
			if p.writes != 2 {
				t.Fatal("idle late audio revived the timer")
			}
			if tt.callActive || tt.announcementActive || tt.streamEnded {
				if err := e.StartManual(); err != session.ErrUnavailable {
					t.Fatal("stream end released retiring native owner", err)
				}
				e.Disconnect(g, "connection_closed")
				var err error
				g, err = e.Attach(&link{})
				if err != nil {
					t.Fatal(err)
				}
			}
			if err := e.StartManual(); err != nil {
				t.Fatal("timer stop did not recover", err)
			}
			_ = e.Handle(g, session.Event{Kind: "listening"})
			if err := e.Output(g, []int16{-41, 903, 7}); err != nil {
				t.Fatal("next turn retained cancelled output context", err)
			}
			if p.writes != 5 || !e.Snapshot().Connected {
				t.Fatal("next turn did not play on the same connection")
			}
			for _, c := range l.commands {
				if c.Kind == "drained" {
					t.Fatal("cancelled timer falsely acknowledged as played")
				}
			}
		})
	}
}

func TestWakeRetainsEvidenceAndBoundedPreRoll(t *testing.T) {
	e, l, _, _ := fixture(t)
	for i := range 34 {
		block := make([]int16, 1000)
		for j := range block {
			block[j] = int16(i*1000 + j)
		}
		if err := e.Capture(audio.Frame{Samples: block, Sequence: uint64(i), CapturedAt: time.Duration(i) * time.Second / 16}); err != nil {
			t.Fatal(err)
		}
		clear(block) // Host is allowed to reuse its capture memory.
	}
	d := wake.Detection{Word: "model-id", Probability: 178, MaxProbability: 219, Cutoff: 163}
	if err := e.Wake(d); err != nil {
		t.Fatal(err)
	}
	if l.commands[0].Detection != d || l.commands[0].PreRollBytes != 48000 {
		t.Fatalf("lost evidence: %+v", l.commands[0])
	}
	var pcm []int16
	for _, c := range l.commands[1:] {
		if len(c.Samples) > audio.MaxSamples {
			t.Fatal("oversize frame")
		}
		pcm = append(pcm, c.Samples...)
	}
	if len(pcm) != 24000 || pcm[0] != 10000 || pcm[len(pcm)-1] != -31537 {
		t.Fatal("wrong ring order or unbounded pre-roll")
	}
	if e.Snapshot().Phase != "tentative" {
		t.Fatal("local candidate shown as accepted")
	}
}

func TestPrivacyAndReconnectFenceStaleReader(t *testing.T) {
	e, l, p, g := fixture(t)
	var active atomic.Bool
	e.SetActivityHandler(active.Store)
	if err := e.StartManual(); err != nil {
		t.Fatal(err)
	}
	if !active.Load() {
		t.Fatal("voice did not claim ducking")
	}
	e.SetMuted(true)
	if active.Load() {
		t.Fatal("mute retained voice ducking")
	}
	if !e.Snapshot().Muted || p.flushed == 0 || l.commands[len(l.commands)-1].Kind != "cancel" {
		t.Fatal("mute failed to silence locally and cancel the voice call")
	}
	if err := e.Output(g, []int16{9}); err != nil || p.writes != 0 {
		t.Fatalf("late audio: %v", err)
	}
	if err := e.StartManual(); !errors.Is(err, session.ErrUnavailable) {
		t.Fatal("started while muted")
	}
	_ = e.Capture(audio.Frame{Samples: []int16{77}})
	e.SetMuted(false)
	if _, err := e.Attach(&link{}); !errors.Is(err, session.ErrUnavailable) {
		t.Fatal("replacement attached before cancellation retired the old connection")
	}
	e.Disconnect(g, "connection_closed")
	newLink := &link{}
	ng, err := e.Attach(newLink)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Handle(g, session.Event{Kind: "timer_ringing"}); !errors.Is(err, session.ErrStale) {
		t.Fatal("stale alert accepted")
	}
	if listen, stop, monitor := e.WakeState(g); listen || stop || monitor != 0 {
		t.Fatal("stale model execution armed")
	}
	if err := e.WakeFor(g, wake.Detection{Word: "old-model"}); !errors.Is(err, session.ErrStale) {
		t.Fatal("old model restarted capture", err)
	}
	if err := e.WakeFor(ng, wake.Detection{Word: "stop"}); !errors.Is(err, session.ErrUnavailable) {
		t.Fatal("stop model armed while idle", err)
	}
	if err := e.Wake(wake.Detection{Word: "another-model"}); err != nil {
		t.Fatal(err)
	}
	if retry := e.UpdateWakeModelsWhenIdle(g, func() { t.Error("stale model update ran") }); retry != nil {
		t.Fatal("stale model update received an idle notification")
	}
	if newLink.commands[0].PreRollBytes != 0 || e.Snapshot().Alert || ng == g {
		t.Fatal("state/audio survived reconnect")
	}
}

func TestMutedStartupAndReconnectFenceOldReaders(t *testing.T) {
	e := session.New(&player{})
	e.SetMuted(true)
	for range 2 {
		l := &link{}
		g, err := e.Attach(l)
		if err != nil || !e.Snapshot().Muted || !e.Snapshot().Connected || e.Snapshot().Phase != "muted" {
			t.Fatal("muted connection rejected or microphone re-enabled", e.Snapshot(), err)
		}
		_ = e.Capture(audio.Frame{Samples: []int16{127}})
		_ = e.Handle(g, session.Event{Kind: "hangup", Listen: true})
		_ = e.Handle(g, session.Event{Kind: "responding", Text: "stale reply"})
		_ = e.EndOutput(g)
		if e.StartManual() != session.ErrUnavailable || len(l.commands) != 0 || e.Snapshot().Reply != "" {
			t.Fatal("muted connection could start or publish voice events")
		}
		e.SetMuted(false)
		if err := e.StartManual(); err != nil {
			t.Fatal(err)
		}
		e.SetMuted(true) // Reconnect before this cancellation completes.
		e.Disconnect(g, "network_lost")
		if !e.Snapshot().Muted || e.Snapshot().Connected {
			t.Fatal(e.Snapshot())
		}
		if err := e.Output(g, []int16{99}); err != session.ErrStale {
			t.Fatal("disconnected reader retained ownership", err)
		}
	}
	g, err := e.Attach(&link{})
	if err != nil {
		t.Fatal(err)
	}
	e.Disconnect(g-1, "old_reader_cleanup")
	_ = e.Handle(g-1, session.Event{Kind: "hangup", Listen: true})
	e.SetMuted(false)
	if err := e.StartManual(); err != nil {
		t.Fatal("old cancellation survived reconnect", err)
	}
}

func TestMuteDuringDrainCannotCompleteANewerTurn(t *testing.T) {
	p, l := &player{entered: make(chan struct{}), release: make(chan struct{}), holdCancelled: true}, &link{}
	e := session.New(p)
	g, _ := e.Attach(l)
	_ = e.StartManual()
	_ = e.Handle(g, session.Event{Kind: "listening"})
	_ = e.Output(g, []int16{5, -19, 83})
	done := make(chan error, 1)
	go func() { done <- e.EndOutput(g) }()
	<-p.entered
	e.SetMuted(true)
	e.SetMuted(false)
	if e.StartManual() != session.ErrUnavailable {
		t.Fatal("old stream end mistaken for cancellation acknowledgement")
	}
	_ = e.EndOutput(g) // Even the ACK cannot release native ownership.
	e.Disconnect(g, "connection_closed")
	l = &link{}
	g, _ = e.Attach(l)
	if err := e.StartManual(); err != nil {
		t.Fatal(err)
	}
	_ = e.Handle(g, session.Event{Kind: "listening"})
	close(p.release)
	select {
	case err := <-done:
		if err != session.ErrStale {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("mute blocked behind drain")
	}
	_ = e.Output(g, []int16{-31, 67})
	if l.closed || p.writes != 5 || l.commands[len(l.commands)-1].Kind != "start" {
		t.Fatal("cancelled drain interfered with new stream", e.Snapshot())
	}
}

func TestStopInterruptsDrainAndRejectsLateAudio(t *testing.T) {
	e, l, p, g := fixture(t)
	_ = e.StartManual()
	_ = e.Handle(g, session.Event{Kind: "listening"})
	_ = e.Output(g, []int16{1, -2, 3})
	p.entered, p.release = make(chan struct{}), make(chan struct{})
	done := make(chan error, 1)
	go func() { done <- e.EndOutput(g) }()
	<-p.entered
	if len(l.commands) != 1 {
		t.Fatal("acknowledged before actual drain")
	}
	if err := e.Stop(); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("stop blocked behind drain")
	}
	for _, c := range l.commands {
		if c.Kind == "drained" {
			t.Fatal("cancelled audio falsely acknowledged")
		}
	}
	// Neither drain cancellation nor another stream end releases this native
	// generation. Only the adapter's disconnect admits a fresh manual call.
	if err := e.StartManual(); err != session.ErrUnavailable {
		t.Fatal("cancelled drain released native generation", err)
	}
	e.Disconnect(g, "connection_closed")
	g, _ = e.Attach(&link{})
	if err := e.StartManual(); err != nil {
		t.Fatal(err)
	}
	_ = e.Stop()
	_ = e.Output(g, []int16{4, 5})
	_ = e.Handle(g, session.Event{Kind: "responding", Text: "late"})
	if p.writes != 3 || e.Snapshot().Reply != "" {
		t.Fatal("late output revived cancelled turn")
	}
	_ = e.EndOutput(g)
	if err := e.StartManual(); err != session.ErrUnavailable {
		t.Fatal("stream end released cancelled native input", err)
	}
}

func TestMicMutePreservesRingingAndManualDismissal(t *testing.T) {
	for _, name := range []string{"mute_during_playback", "ring_while_muted"} {
		t.Run(name, func(t *testing.T) {
			e, l, p, g := fixture(t)
			if name == "ring_while_muted" {
				e.SetMuted(true)
				_ = e.Output(g, []int16{97}) // Idle PCM cannot ring without the event.
			}
			_ = e.Handle(g, session.Event{Kind: "timer_ringing"})
			_ = e.Output(g, []int16{-7, 13})
			flushed := p.flushed
			e.SetMuted(true)
			if s := e.Snapshot(); !s.Muted || !s.Alert || !s.Active || s.Output.Owner != "alert" || s.Output.State != "playing" ||
				s.InputOpen || s.TalkAllowed || s.WakeAllowed || p.writes != 2 || p.flushed != flushed || len(l.commands) != 0 {
				t.Fatal("mic mute interrupted the alert or allowed capture", s, l.commands)
			}
			_ = e.Capture(audio.Frame{Samples: []int16{127}})
			if listen, stop, _ := e.WakeState(g); listen || stop || e.StartManual() != session.ErrUnavailable {
				t.Fatal("alert enabled the muted microphone")
			}
			_ = e.EndOutput(g)
			_ = e.Output(g, []int16{29}) // Ring audio can span several streams.
			_ = e.Stop()
			_ = e.Output(g, []int16{17})
			_ = e.Handle(g, session.Event{Kind: "timer_stopped"}) // No audio end on a cancelled ring.
			_ = e.Handle(g, session.Event{Kind: "timer_ringing"})
			_ = e.Output(g, []int16{37, 41})
			_ = e.Handle(g, session.Event{Kind: "timer_stopped"}) // Dismissed on another satellite.
			_ = e.EndOutput(g)                                    // No false drain after remote dismissal.
			if s := e.Snapshot(); !s.Muted || s.Alert || s.Active || p.writes != 5 || l.closed ||
				len(l.commands) != 2 || l.commands[0].Kind != "drained" || l.commands[1].Kind != "stop_alert" {
				t.Fatal("muted ring did not drain, dismiss and rearm independently of capture", s, l.commands, p.writes)
			}
		})
	}
}

func TestMutedRingDoesNotPlayOverlappingNativeAnnouncement(t *testing.T) {
	for _, ringFirst := range []bool{false, true} {
		e, l, p, g := fixture(t)
		e.SetMuted(true)
		ring := session.Event{Kind: "timer_ringing"}
		if ringFirst {
			_ = e.Handle(g, ring)
			_ = e.Output(g, []int16{12, -31})
		}
		writes, flushed := p.writes, p.flushed
		_ = e.Handle(g, session.Event{Kind: "announcement", Active: true})
		if !ringFirst {
			_ = e.Handle(g, ring)
		}
		_ = e.Output(g, []int16{73})
		_ = e.EndOutput(g)
		_ = e.Output(g, []int16{29, -87}) // An announcement can have multiple segments.
		_ = e.EndOutput(g)
		if s := e.Snapshot(); !s.Alert || !s.Muted || s.Announcing || s.Output.State != "" ||
			p.writes != writes || (ringFirst && p.flushed != flushed+1) || len(l.commands) != 0 {
			t.Fatal("ambiguous PCM played or displaced the muted ring", s, p.writes, l.commands)
		}
		_ = e.Handle(g, session.Event{Kind: "announcement", Active: false})
		_ = e.Output(g, []int16{-7, 13, 41})
		_ = e.EndOutput(g)
		if p.writes != writes+3 || len(l.commands) != 1 || l.commands[0].Kind != "drained" {
			t.Fatal("unambiguous ring did not resume", p.writes, l.commands)
		}
	}
}

func TestPlaybackInterruptionDoesNotDismissRing(t *testing.T) {
	e, l, p, g := fixture(t)
	_ = e.Handle(g, session.Event{Kind: "timer_ringing"})
	sequence := e.Snapshot().Ring.Sequence // The ring need not have reached the display yet.
	e.SetPlaybackInterrupted(true)         // Denied before any PCM arrives.
	_ = e.Output(g, []int16{97, 89})
	e.SetPlaybackInterrupted(false)
	_ = e.Output(g, []int16{47}) // Do not play or acknowledge the tail of a skipped segment.
	_ = e.EndOutput(g)
	if !e.Snapshot().Alert || p.writes != 0 || len(l.commands) != 0 {
		t.Fatal("denied focus dismissed the ring or allowed partial playback", e.Snapshot(), p.writes, l.commands)
	}
	_ = e.Output(g, []int16{-5, 11, -23})
	flushed := p.flushed
	e.SetPlaybackInterrupted(true) // Focus lost during playback.
	_ = e.Output(g, []int16{101})
	_ = e.EndOutput(g)
	if s := e.Snapshot(); !s.Alert || s.Ring.Sequence != sequence || s.Output.State != "" || p.writes != 3 ||
		p.flushed != flushed+1 || l.closed || len(l.commands) != 0 {
		t.Fatal("lost focus did not silence just this satellite", s, p.writes, l.commands)
	}
	e.StopAlert(sequence)
	if e.Snapshot().Alert || len(l.commands) != 1 || l.commands[0].Kind != "stop_alert" {
		t.Fatal("explicit Stop no longer dismisses the interrupted alert", l.commands)
	}
}

func TestPlaybackInterruptionDuringDrainDoesNotSkipNextSegment(t *testing.T) {
	e, l, p, g := fixture(t)
	p.entered, p.release = make(chan struct{}), make(chan struct{})
	_ = e.Handle(g, session.Event{Kind: "timer_ringing"})
	_ = e.Output(g, []int16{17, -83})
	done := make(chan error, 1)
	go func() { done <- e.EndOutput(g) }()
	<-p.entered
	e.SetPlaybackInterrupted(true)
	e.SetPlaybackInterrupted(false)
	close(p.release)
	if err := <-done; err != nil || len(l.commands) != 0 {
		t.Fatal("interrupted drain acknowledged playback", err, l.commands)
	}
	p.entered = nil
	_ = e.Output(g, []int16{-31, 67, 123})
	_ = e.EndOutput(g)
	if p.writes != 5 || len(l.commands) != 1 || l.commands[0].Kind != "drained" {
		t.Fatal("the next segment did not play after focus returned", p.writes, l.commands)
	}
}

func TestPlaybackInterruptionStillCancelsVoiceCall(t *testing.T) {
	e, l, _, g := fixture(t)
	_ = e.StartManual()
	_ = e.Handle(g, session.Event{Kind: "listening"})
	e.SetPlaybackInterrupted(true)
	if e.Snapshot().InputOpen || len(l.commands) != 2 || l.commands[1].Kind != "cancel" {
		t.Fatal("focus loss did not cancel active conversation", e.Snapshot(), l.commands)
	}
}

func TestStoppedTimerRestoresReadiness(t *testing.T) {
	for _, end := range []string{"timer_stopped", "output_end"} {
		t.Run(end, func(t *testing.T) {
			e, l, p, g := fixture(t)
			_ = e.Handle(g, session.Event{Kind: "timer_ringing"})
			_ = e.Output(g, []int16{-7, 13})
			if err := e.Stop(); err != nil {
				t.Fatal(err)
			}
			_ = e.Stop() // Repeated Stop must retain the original cancellation boundary.
			if len(l.commands) != 1 || l.commands[0].Kind != "stop_alert" || p.flushed == 0 {
				t.Fatal("timer was not silenced and stopped", l.commands)
			}
			_ = e.Output(g, []int16{79})
			_ = e.Handle(g, session.Event{Kind: "timer_ringing"})
			_ = e.Handle(g, session.Event{Kind: "responding", Text: "late"})
			if err := e.StartManual(); !errors.Is(err, session.ErrUnavailable) {
				t.Fatal("started before cancelled timer ended", err)
			}
			if end == "output_end" {
				_ = e.EndOutput(g)
			}
			// A cancelled paced ring need not send AUDIO_OUT_END at all.
			_ = e.Handle(g, session.Event{Kind: "timer_stopped"})
			_ = e.Output(g, []int16{83})
			_ = e.Handle(g, session.Event{Kind: "responding", Text: "late"})
			if p.writes != 2 || e.Snapshot().Alert || e.Snapshot().Reply != "" || l.closed || len(l.commands) != 1 {
				t.Fatal("late timer output played, claimed drain or revived presentation", e.Snapshot(), l.commands)
			}
			if err := e.StartManual(); err != nil {
				t.Fatal("next invocation requires reconnect", err)
			}
			_ = e.Handle(g, session.Event{Kind: "listening"})
			_ = e.Output(g, []int16{-17, 29, 43})
			_ = e.EndOutput(g)
			if p.writes != 5 || len(l.commands) != 3 || l.commands[1].Kind != "start" || l.commands[2].Kind != "drained" {
				t.Fatal("next invocation did not play and complete", l.commands)
			}
		})
	}
}

func TestTimerStoppedDoesNotReleaseCancelledCall(t *testing.T) {
	for _, name := range []string{"call", "call_with_timer", "hung_up_call_with_timer"} {
		t.Run(name, func(t *testing.T) {
			e, _, p, g := fixture(t)
			_ = e.StartManual()
			_ = e.Handle(g, session.Event{Kind: "listening"})
			if name == "call_with_timer" {
				_ = e.Handle(g, session.Event{Kind: "timer_ringing"})
			}
			_ = e.Output(g, []int16{19})
			if name == "hung_up_call_with_timer" {
				_ = e.Handle(g, session.Event{Kind: "hangup"})
				_ = e.Handle(g, session.Event{Kind: "timer_ringing"})
			}
			// More PCM in the same stream must retain its original owner,
			// even if the call ended and a timer was signaled in the meantime.
			_ = e.Output(g, []int16{-23})
			_ = e.Stop()
			_ = e.Handle(g, session.Event{Kind: "timer_stopped"})
			_ = e.Output(g, []int16{97})
			if err := e.StartManual(); !errors.Is(err, session.ErrUnavailable) || p.writes != 2 {
				t.Fatal("timer event released a cancelled voice call", err)
			}
			_ = e.EndOutput(g)
			if err := e.StartManual(); err != session.ErrUnavailable {
				t.Fatal("stream end released cancelled native call", err)
			}
		})
	}
}

func TestDiscontinuityAndTransportFailureFailClosed(t *testing.T) {
	for _, cause := range []string{"gap", "queue"} {
		t.Run(cause, func(t *testing.T) {
			e, l, _, _ := fixture(t)
			_ = e.StartManual()
			_ = e.Capture(audio.Frame{Samples: []int16{1}, Sequence: 3})
			if cause == "queue" {
				l.fail = true
			}
			seq := uint64(4)
			if cause == "gap" {
				seq = 5
			}
			err := e.Capture(audio.Frame{Samples: []int16{2}, Sequence: seq, CapturedAt: time.Millisecond})
			if err == nil || !l.closed || e.Snapshot().Connected {
				t.Fatal("failure did not revoke voice ownership")
			}
		})
	}
}

func TestIncubatorElectionAndFollowUp(t *testing.T) {
	e, l, _, g := fixture(t)
	_ = e.StartManual()
	_ = e.Handle(g, session.Event{Kind: "listening"})
	_ = e.Handle(g, session.Event{Kind: "responding"}) // Reply can play on an external speaker.
	if err := e.Handle(g, session.Event{Kind: "hangup", Listen: true}); err != nil {
		t.Fatal(err)
	}
	last := l.commands[len(l.commands)-1]
	if last.Detection.Word != "follow_up" || last.PreRollBytes != 0 {
		t.Fatal("follow-up replays wake audio")
	}
	before := e.Snapshot()
	if err := e.Handle(g, session.Event{Kind: "hangup", Listen: true}); err != nil || e.Snapshot() != before || len(l.commands) != 2 {
		t.Fatal("duplicate HANGUP changed the listening follow-up", err, e.Snapshot(), l.commands)
	}
	_ = e.Handle(g, session.Event{Kind: "responding", Text: ""})
	if e.Snapshot().Phase != "speaking" || e.Snapshot().Reply != "" {
		t.Fatal("invented missing caption")
	}
	_ = e.Handle(g, session.Event{Kind: "hangup", Reason: "answered_elsewhere", Listen: true})
	if l.closed || !e.Snapshot().Connected || e.Snapshot().Reason != "answered_elsewhere" {
		t.Fatal("upstream election ignored")
	}
}

func TestFollowUpRequiresCurrentResponse(t *testing.T) {
	for _, phase := range []string{"idle", "ended", "unaccepted", "listening", "working", "playing"} {
		t.Run(phase, func(t *testing.T) {
			e, l, _, g := fixture(t)
			if phase != "idle" {
				_ = e.Wake(wake.Detection{Word: "nabu"})
			}
			switch phase {
			case "ended":
				_ = e.Handle(g, session.Event{Kind: "hangup", Reason: "done"})
			case "unaccepted":
				_ = e.Handle(g, session.Event{Kind: "responding"})
			case "listening", "working", "playing":
				_ = e.Handle(g, session.Event{Kind: "listening"})
				if phase == "working" {
					_ = e.Handle(g, session.Event{Kind: "processing"})
				}
				if phase == "playing" {
					_ = e.Handle(g, session.Event{Kind: "responding"})
					_ = e.Output(g, []int16{12})
				}
			}
			before, count := e.Snapshot(), len(l.commands)
			if err := e.Handle(g, session.Event{Kind: "hangup", Reason: "done", Listen: true}); err != nil || e.Snapshot() != before {
				t.Fatal("unowned HANGUP changed the session", err, e.Snapshot())
			}
			if !before.InputOpen {
				if err := e.Capture(audio.Frame{Samples: []int16{513}}); err != nil {
					t.Fatal(err)
				}
			}
			if len(l.commands) != count {
				t.Fatal("unowned HANGUP sent a call start or PCM", l.commands[count:])
			}
		})
	}
}

func TestCallWatchdogGivesFollowUpItsOwnDeadline(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		e, l, _, g := fixture(t)
		if err := e.StartManual(); err != nil {
			t.Fatal(err)
		}
		time.Sleep(25 * time.Second)
		for _, event := range []session.Event{{Kind: "listening"}, {Kind: "responding"}, {Kind: "hangup", Listen: true}} {
			if err := e.Handle(g, event); err != nil {
				t.Fatal(err)
			}
		}
		time.Sleep(30*time.Second - time.Nanosecond) // Past the old deadline, just before the new one.
		synctest.Wait()
		if s := e.Snapshot(); s.Turn != 2 || s.Invocation != "follow_up" || !s.InputOpen {
			t.Fatal("follow-up did not keep its full window", s)
		}
		time.Sleep(time.Nanosecond)
		synctest.Wait()
		if s := e.Snapshot(); s.InputOpen || !s.Inhibited || s.Reason != "timeout" || len(l.commands) != 3 || l.commands[2].Kind != "cancel" || l.commands[2].Reason != "timeout" {
			t.Fatal("follow-up did not expire at its own deadline", s, l.commands)
		}
	})
}

func TestRejectedNativeCandidateKeepsConnectionAndFencesOldOutput(t *testing.T) {
	for _, reason := range []string{"answered_elsewhere", "wake_rejected"} {
		t.Run(reason, func(t *testing.T) {
			e, l, p, g := fixture(t)
			_ = e.StartManual()
			_ = e.Handle(g, session.Event{Kind: "responding", Text: "old reply"})
			_ = e.Handle(g, session.Event{Kind: "hangup", Reason: reason, Listen: true})
			_ = e.Handle(g, session.Event{Kind: "responding", Text: "late reply"})
			_ = e.Output(g, []int16{91})
			_ = e.EndOutput(g)
			if l.closed || !e.Snapshot().Connected || e.Snapshot().Phase != "idle" || e.Snapshot().Reply != "" || p.writes != 0 || len(l.commands) != 1 {
				t.Fatal("rejection closed connection, followed up or revived old output", e.Snapshot())
			}
			if err := e.StartManual(); err != nil {
				t.Fatal("next turn requires reconnect", err)
			}
			_ = e.Handle(g, session.Event{Kind: "listening"})
			_ = e.Output(g, []int16{-17, 29})
			_ = e.EndOutput(g)
			if p.writes != 2 || l.commands[len(l.commands)-1].Kind != "drained" {
				t.Fatal("next turn did not play and complete")
			}
			_ = e.Handle(g, session.Event{Kind: "hangup", Reason: reason})
			_ = e.Handle(g, session.Event{Kind: "timer_ringing"})
			_ = e.Output(g, []int16{43})
			_ = e.EndOutput(g)
			if p.writes != 3 {
				t.Fatal("rejection suppressed a subsequent upstream timer")
			}
		})
	}
}

func TestNativeAnnouncementAllowsMultipleStreamsButNotIdleOrStoppedOutput(t *testing.T) {
	e, l, p, g := fixture(t)
	e.SetMuted(true)
	e.SetMuted(false) // A fresh out-of-call stream must not inherit idle mute.
	_ = e.Handle(g, session.Event{Kind: "announcement", Active: true})
	for _, pcm := range [][]int16{{-7}, {13, -23}} {
		if err := e.Output(g, pcm); err != nil {
			t.Fatal(err)
		}
		_ = e.EndOutput(g)
	}
	if p.writes != 3 || len(l.commands) != 2 || l.commands[0].Kind != "drained" || l.commands[1].Kind != "drained" {
		t.Fatal("announcement chime and speech did not each play and complete")
	}
	_ = e.Handle(g, session.Event{Kind: "announcement", Active: false})
	_ = e.Output(g, []int16{79})
	_ = e.EndOutput(g)
	_ = e.Handle(g, session.Event{Kind: "announcement", Active: true})
	_ = e.Stop()
	_ = e.Output(g, []int16{83})
	_ = e.EndOutput(g)
	if p.writes != 3 || len(l.commands) != 3 || l.commands[2].Kind != "cancel" {
		t.Fatal("idle/stopped announcement audio played or claimed completion")
	}
	if err := e.StartManual(); err != session.ErrUnavailable {
		t.Fatal("stopped announcement reused its native connection", err)
	}
}

func TestMuteNativeAnnouncementRevokesOnlyItsVoiceLink(t *testing.T) {
	e, l, p, g := fixture(t)
	_ = e.Handle(g, session.Event{Kind: "announcement", Active: true})
	_ = e.Output(g, []int16{-71})
	e.SetMuted(true)
	if len(l.commands) != 1 || l.commands[0].Kind != "cancel" || !e.Snapshot().Muted {
		t.Fatal("announcement mute did not request native cancellation", e.Snapshot(), l.commands)
	}
	_ = e.EndOutput(g) // CALL_END's acknowledgement cannot end announcing_now.
	e.SetMuted(false)
	if err := e.StartManual(); !errors.Is(err, session.ErrUnavailable) {
		t.Fatal("started before native reconnect", err)
	}
	e.Disconnect(g, "connection_closed") // Adapter retires the link after sending cancel.
	newLink := &link{}
	ng, err := e.Attach(newLink)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.StartManual(); err != nil {
		t.Fatal(err)
	}
	if err := e.Output(g, []int16{99}); !errors.Is(err, session.ErrStale) || p.writes != 1 {
		t.Fatal("old announcement crossed into new turn", err, p.writes)
	}
	// The upstream task can survive socket loss and write on its new socket.
	if err := e.Output(ng, []int16{101}); err != nil || p.writes != 1 {
		t.Fatal("played before upstream accepted the new CALL_START", err, p.writes)
	}
	_ = e.Handle(ng, session.Event{Kind: "listening"})
	if err := e.Output(ng, []int16{13, -29}); err != nil || p.writes != 3 {
		t.Fatal("new voice connection cannot play", err, p.writes)
	}
}

func TestNativeMuteCannotReuseBufferedStreamBoundary(t *testing.T) {
	e, _, p, g := fixture(t)
	_ = e.StartManual()
	_ = e.Handle(g, session.Event{Kind: "listening"})
	_ = e.Output(g, []int16{1})
	e.SetMuted(true)
	e.SetMuted(false)
	// An ordinary end and HANGUP may already be buffered before cancellation.
	_ = e.EndOutput(g)
	if err := e.StartManual(); err != session.ErrUnavailable {
		t.Fatal("old ordinary end admitted a new call before native disconnect", err)
	}
	_ = e.Handle(g, session.Event{Kind: "hangup", Reason: "done"})
	_ = e.EndOutput(g) // Even the identical cancellation ACK cannot prove ownership.
	if err := e.StartManual(); err != session.ErrUnavailable {
		t.Fatal("cancellation ACK admitted a call on the retiring native link", err)
	}
	e.Disconnect(g, "connection_closed")
	ng, err := e.Attach(&link{})
	if err != nil {
		t.Fatal(err)
	}
	_ = e.StartManual()
	_ = e.Handle(g, session.Event{Kind: "hangup", Reason: "done"})
	_ = e.Handle(ng, session.Event{Kind: "listening"})
	_ = e.Output(ng, []int16{99})
	if p.writes != 2 {
		t.Fatal("stale native tail silenced the replacement call", p.writes)
	}
}

func TestSpokenStopCoversResponseBeforePCMAndAfterDrain(t *testing.T) {
	for _, playReply := range []bool{false, true} {
		name := "awaiting-pcm"
		if playReply {
			name = "drained-before-hangup"
		}
		t.Run(name, func(t *testing.T) {
			e, l, _, g := fixture(t)
			stop := wake.Detection{Word: "stop"}
			if err := e.StartManual(); err != nil {
				t.Fatal(err)
			}
			for _, kind := range []string{"listening", "processing"} {
				_ = e.Handle(g, session.Event{Kind: kind})
				if _, armed, _ := e.WakeState(g); armed {
					t.Fatalf("Stop armed during %s", kind)
				}
				if err := e.WakeFor(g, stop); err != session.ErrUnavailable {
					t.Fatalf("Stop accepted during %s: %v", kind, err)
				}
			}
			_ = e.Handle(g, session.Event{Kind: "responding"}) // External TTS can have no text.
			if playReply {
				if err := e.Output(g, []int16{17, -83}); err != nil {
					t.Fatal(err)
				}
				if err := e.EndOutput(g); err != nil || l.commands[len(l.commands)-1].Kind != "drained" {
					t.Fatal("response did not drain", err, l.commands)
				}
			}
			if _, armed, _ := e.WakeState(g); !armed {
				t.Fatal("Stop not armed during the current response")
			}
			if err := e.WakeFor(g, stop); err != nil || l.commands[len(l.commands)-1].Kind != "cancel" {
				t.Fatal("spoken Stop did not retire the voice connection", err, l.commands)
			}
			_ = e.Handle(g, session.Event{Kind: "hangup", Listen: true})
			if s := e.Snapshot(); s.Turn != 1 || s.InputOpen || !s.Inhibited {
				t.Fatal("cancelled response reopened capture", s)
			}
		})
	}
}

func TestNativeStopCannotReuseBufferedStreamBoundary(t *testing.T) {
	e, l, p, g := fixture(t)
	_ = e.StartManual()
	_ = e.Handle(g, session.Event{Kind: "listening"})
	_ = e.Output(g, []int16{17, -83})
	_ = e.Stop()
	_ = e.Stop()
	_ = e.EndOutput(g) // Ordinary response end buffered before Stop.
	if err := e.StartManual(); err != session.ErrUnavailable {
		t.Fatal("ordinary end released cancelled native call", err)
	}
	_ = e.Handle(g, session.Event{Kind: "hangup", Listen: true})
	if s := e.Snapshot(); s.Turn != 1 || s.InputOpen || !s.Inhibited {
		t.Fatal("old HANGUP reopened capture", s)
	}
	if len(l.commands) != 2 || l.commands[1].Kind != "cancel" {
		t.Fatal("Stop did not request exactly one native retirement", l.commands)
	}
	e.Disconnect(g, "connection_closed")
	ng, err := e.Attach(&link{})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.StartManual(); err != nil {
		t.Fatal(err)
	}
	_ = e.Handle(g, session.Event{Kind: "hangup", Listen: true})
	if s := e.Snapshot(); s.Turn != 2 || !s.InputOpen {
		t.Fatal("old HANGUP clobbered fresh manual turn", s)
	}
	_ = e.Handle(ng, session.Event{Kind: "listening"})
	if err := e.Output(ng, []int16{-31}); err != nil || p.writes != 3 {
		t.Fatal("fresh native playback failed", err, p.writes)
	}
}

func TestIdleStopPreservesUnsolicitedPlayback(t *testing.T) {
	for _, kind := range []string{"announcement", "timer_ringing"} {
		t.Run(kind, func(t *testing.T) {
			e, l, p, g := fixture(t)
			_ = e.Stop()
			_ = e.Stop()
			_ = e.Handle(g, session.Event{Kind: kind, Active: true})
			if err := e.Output(g, []int16{17, -83}); err != nil {
				t.Fatal("idle Stop poisoned next playback context", err)
			}
			if err := e.EndOutput(g); err != nil {
				t.Fatal(err)
			}
			if p.writes != 2 || len(l.commands) != 1 || l.commands[0].Kind != "drained" {
				t.Fatal("first idle output did not play and drain", l.commands, p.writes)
			}
		})
	}
}

func TestNativeStopThenMuteDuringAnnouncementDrainReconnects(t *testing.T) {
	p := &player{entered: make(chan struct{}), release: make(chan struct{}), holdCancelled: true}
	l := &link{}
	e := session.New(p)
	g, _ := e.Attach(l)
	_ = e.Handle(g, session.Event{Kind: "announcement", Active: true})
	_ = e.Output(g, []int16{-7})
	done := make(chan error, 1)
	go func() { done <- e.EndOutput(g) }()
	<-p.entered
	_ = e.Stop()
	e.SetMuted(true)
	e.SetMuted(false)
	close(p.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if len(l.commands) != 1 || l.commands[0].Kind != "cancel" {
		t.Fatal("Stop/mute left a terminal wait with no native cancellation", l.commands)
	}
	e.Disconnect(g, "connection_closed")
	ng, err := e.Attach(&link{})
	if err != nil {
		t.Fatal(err)
	}
	if err := e.StartManual(); err != nil {
		t.Fatal("Stop/mute wedged voice after native reconnect", err)
	}
	_ = e.Handle(g, session.Event{Kind: "announcement", Active: false})
	_ = e.Handle(ng, session.Event{Kind: "listening"})
	_ = e.Output(ng, []int16{13, -29})
	if p.writes != 3 {
		t.Fatal("new call after cancelled announcement did not play", p.writes)
	}
}

func TestPresentationTurnAttributionAndPolicyLifetimes(t *testing.T) {
	e, _, _, g := fixture(t)
	on := true
	_ = e.Handle(g, session.Event{Kind: "night", Night: &on})
	_ = e.Wake(wake.Detection{Word: "nabu"})
	first := e.Snapshot()
	if first.Generation != g || first.Turn == 0 || first.Invocation != "wake" || first.Input != "tentative" || first.Accepted || first.Night != "on" {
		t.Fatal(first)
	}
	_ = e.Handle(g, session.Event{Kind: "listening"})
	if s := e.Snapshot(); s.Input != "listening" || !s.InputOpen || !s.Accepted || !s.Active {
		t.Fatal(s)
	}
	person := presentation.Speaker{Name: "Person", Score: 0, ScoreKnown: true, RGB: [3]byte{0, 37, 255}, ColorKnown: true}
	for range 2 {
		_ = e.Handle(g, session.Event{Kind: "processing", Speaker: person})
		if s := e.Snapshot(); s.Turn != first.Turn || s.Input != "" || !s.InputOpen || s.Speaker != person || s.Output.State != "" {
			t.Fatal("processing metadata changed the turn or ownership", s)
		}
	}
	copy := e.Snapshot()
	copy.Speaker.RGB[1] = 99
	if e.Snapshot().Speaker != person {
		t.Fatal("snapshot aliases retained attribution")
	}
	_ = e.Handle(g, session.Event{Kind: "responding", Text: "Actual reply"})
	_ = e.Handle(g, session.Event{Kind: "processing", Speaker: presentation.Speaker{Name: "Late speaker"}})
	if s := e.Snapshot(); s.Phase != "speaking" || !s.InputOpen || s.Output.State != "queued" || s.Output.Received || s.Speaker != person || s.Reply != "Actual reply" || s.Turn != first.Turn {
		t.Fatal("text/late attribution claimed playback or restarted work", s)
	}
	_ = e.Output(g, []int16{321, -765})
	if s := e.Snapshot(); s.Output.State != "playing" || !s.InputOpen || s.Speaker != person {
		t.Fatal("output lost independent input or current-turn attribution", s)
	}
	_ = e.EndOutput(g)
	if s := e.Snapshot(); s.Output.State != "" {
		t.Fatal(s)
	}
	_ = e.Handle(g, session.Event{Kind: "hangup", Listen: true})
	if s := e.Snapshot(); s.Turn != first.Turn+1 || s.Invocation != "follow_up" || s.Input != "listening" || !s.InputOpen || s.Speaker.Name != "" || s.Accepted || s.Reply != "" || s.Night != "on" {
		t.Fatal("follow-up did not establish its own clean turn", s)
	}
	_ = e.Stop()
	_ = e.Handle(g, session.Event{Kind: "processing", Speaker: person})
	if s := e.Snapshot(); !s.Inhibited || s.InputOpen || s.Active || s.Output.State != "" || s.Speaker.Name != "" || s.Heard != "" || s.Reply != "" || s.Night != "on" || s.TalkAllowed {
		t.Fatal("stop retained or revived turn presentation", s)
	}
	e.SetMuted(true)
	on = false
	_ = e.Handle(g, session.Event{Kind: "night", Night: &on})
	if s := e.Snapshot(); !s.Inhibited || s.Night != "off" {
		t.Fatal("privacy suppressed connection-scoped policy", s)
	}
	e.Disconnect(g, "test")
	ng, _ := e.Attach(&link{})
	_ = e.Handle(g, session.Event{Kind: "night", Night: &on})
	_ = e.Handle(g, session.Event{Kind: "processing", Speaker: person})
	if s := e.Snapshot(); s.Generation != ng || s.Night != "unknown" || s.Speaker.Name != "" || s.Output.State != "" {
		t.Fatal("old reader revived replacement presentation", s)
	}
}

func TestPresentationOutputRequiresSuccessfulCurrentHostWrite(t *testing.T) {
	for _, finish := range []string{"accepted", "failed", "stop", "mute", "replace"} {
		t.Run(finish, func(t *testing.T) {
			p := &player{writeEntered: make(chan struct{}), writeRelease: make(chan struct{})}
			if finish == "failed" {
				p.writeErr = errors.New("host write failed")
			}
			e := session.New(p)
			g, _ := e.Attach(&link{})
			_ = e.StartManual()
			_ = e.Handle(g, session.Event{Kind: "listening"})
			_ = e.Handle(g, session.Event{Kind: "responding", Text: "Reply"})
			done := make(chan error, 1)
			go func() { done <- e.Output(g, []int16{19, -43}) }()
			<-p.writeEntered
			if s := e.Snapshot(); s.Output.State != "queued" || !s.Output.Received || s.Output.Owner != "voice" {
				t.Fatal("pending write claimed accepted playback", s)
			}
			switch finish {
			case "stop":
				_ = e.Stop()
			case "mute":
				e.SetMuted(true)
			case "replace":
				e.Disconnect(g, "test")
				_, _ = e.Attach(&link{})
			}
			close(p.writeRelease) // Host may return success despite cancellation.
			err := <-done
			if (err != nil) != (finish == "failed") {
				t.Fatal(err)
			}
			want := ""
			if finish == "accepted" {
				want = "playing"
			} else if finish == "failed" {
				want = "failed"
			}
			if s := e.Snapshot(); s.Output.State != want {
				t.Fatal("completed write misrepresented current ownership", s)
			}
		})
	}
}

func TestPresentationAnnouncementOwnershipSurvivesDrainAndAlertOverlap(t *testing.T) {
	p := &player{entered: make(chan struct{}), release: make(chan struct{})}
	e := session.New(p)
	g, _ := e.Attach(&link{})
	_ = e.Handle(g, session.Event{Kind: "announcement", Active: true})
	if s := e.Snapshot(); !s.Active || !s.Announcing || s.Output.State != "" {
		t.Fatal("announcement marker was lost or claimed PCM playback", s)
	}
	_ = e.Output(g, []int16{7})
	_ = e.Handle(g, session.Event{Kind: "announcement", Active: false})
	_ = e.Handle(g, session.Event{Kind: "timer_ringing"})
	done := make(chan error, 1)
	go func() { done <- e.EndOutput(g) }()
	<-p.entered
	if s := e.Snapshot(); !s.Alert || s.Announcing || s.Output.Owner != "announcement" || s.Output.State != "draining" || !s.Active {
		t.Fatal("alert or announcement-end relabeled owned output", s)
	}
	close(p.release)
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if s := e.Snapshot(); !s.Active || s.Output.State != "" || !s.Alert {
		t.Fatal("drain cleared the independent alert", s)
	}
	_ = e.Output(g, []int16{13})
	if s := e.Snapshot(); s.Output.Owner != "alert" || s.Output.State != "playing" {
		t.Fatal("alert PCM not attributed to the alert", s)
	}
}
