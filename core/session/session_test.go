package session_test

import (
	"context"
	"errors"
	"slices"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/core/session"
	"github.com/farooqu/encore-satellite/core/wake"
)

type link struct {
	backend  session.Backend
	commands []session.Command
	closed   bool
	fail     bool
}

func (l *link) Backend() session.Backend { return l.backend }
func (l *link) Close() error             { l.closed = true; return nil }
func (l *link) Send(c session.Command) error {
	if l.fail {
		return errors.New("queue full")
	}
	c.Samples = slices.Clone(c.Samples)
	l.commands = append(l.commands, c)
	return nil
}

type player struct {
	writes           int
	flushed          int
	entered, release chan struct{}
}

func (p *player) Write(ctx context.Context, samples []int16) error {
	if err := ctx.Err(); err != nil {
		return err
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
			return ctx.Err()
		case <-p.release:
		}
	}
	return nil
}

func fixture(t *testing.T) (*session.Engine, *link, *player, uint64) {
	t.Helper()
	p, l := &player{}, &link{backend: session.Incubator}
	e := session.New(p)
	g, err := e.Attach(l)
	if err != nil {
		t.Fatal(err)
	}
	return e, l, p, g
}

func TestWakeRetainsEvidenceAndBoundedPreRoll(t *testing.T) {
	e, l, _, _ := fixture(t)
	for i := range 18 {
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
	if l.commands[0].Detection != d || l.commands[0].PreRollBytes != 32000 {
		t.Fatalf("lost evidence: %+v", l.commands[0])
	}
	var pcm []int16
	for _, c := range l.commands[1:] {
		if len(c.Samples) > audio.MaxSamples {
			t.Fatal("oversize frame")
		}
		pcm = append(pcm, c.Samples...)
	}
	if len(pcm) != 16000 || pcm[0] != 2000 || pcm[len(pcm)-1] != 17999 {
		t.Fatal("wrong ring order or unbounded pre-roll")
	}
	if e.Snapshot().Phase != "tentative" {
		t.Fatal("local candidate shown as accepted")
	}
}

func TestPrivacyAndBackendSwitchFenceStaleReader(t *testing.T) {
	e, l, p, g := fixture(t)
	if err := e.StartManual(); err != nil {
		t.Fatal(err)
	}
	e.SetMuted(true)
	if !l.closed || !e.Snapshot().Muted || p.flushed == 0 {
		t.Fatal("mute did not close audio ownership")
	}
	if err := e.Output(g, []int16{9}); !errors.Is(err, session.ErrStale) {
		t.Fatalf("late audio: %v", err)
	}
	if err := e.StartManual(); !errors.Is(err, session.ErrUnavailable) {
		t.Fatal("started while muted")
	}
	_ = e.Capture(audio.Frame{Samples: []int16{77}})
	e.SetMuted(false)
	if err := e.Select(session.Assist); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Attach(&link{backend: session.Incubator}); !errors.Is(err, session.ErrUnavailable) {
		t.Fatal("wrong backend attached")
	}
	newLink := &link{backend: session.Assist}
	ng, err := e.Attach(newLink)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.Handle(g, session.Event{Kind: "timer_ringing"}); !errors.Is(err, session.ErrStale) {
		t.Fatal("stale alert accepted")
	}
	if err := e.Wake(wake.Detection{Word: "another-model"}); err != nil {
		t.Fatal(err)
	}
	if newLink.commands[0].PreRollBytes != 0 || e.Snapshot().Alert || ng == g {
		t.Fatal("state/audio survived reconnect")
	}
}

func TestStopInterruptsDrainAndRejectsLateAudio(t *testing.T) {
	e, l, p, g := fixture(t)
	_ = e.StartManual()
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
	// Cancel another active call before any output; late audio must be fenced
	// until its stream-end arrives. An idle Stop does not create a phantom call.
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
	if err := e.StartManual(); err != nil {
		t.Fatal(err)
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
	if err := e.Handle(g, session.Event{Kind: "hangup", Listen: true}); err != nil {
		t.Fatal(err)
	}
	last := l.commands[len(l.commands)-1]
	if last.Detection.Word != "follow_up" || last.PreRollBytes != 0 {
		t.Fatal("follow-up replays wake audio")
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

func TestAssistIdleStreamEndAfterReconnectDoesNotClaimPlayback(t *testing.T) {
	e, _, p, _ := fixture(t)
	_ = e.Select(session.Assist)
	l := &link{backend: session.Assist}
	g, _ := e.Attach(l)
	_ = e.Output(g, []int16{77})
	_ = e.EndOutput(g) // Observed on real HA: cancelled task finally uses new client.
	if p.writes != 0 || len(l.commands) != 0 {
		t.Fatal("unsolicited stream played or falsely acknowledged")
	}
	_ = e.Stop() // Idle stop must not wedge the next intentional session.
	if err := e.StartManual(); err != nil {
		t.Fatal(err)
	}
	_ = e.EndOutput(g)
	if len(l.commands) != 1 {
		t.Fatal("empty stream ended the listening turn")
	}
	_ = e.Handle(g, session.Event{Kind: "output_start"})
	_ = e.Output(g, []int16{3, -5})
	_ = e.EndOutput(g)
	if p.writes != 2 || l.commands[len(l.commands)-1].Kind != "drained" {
		t.Fatal("valid response no longer plays/completes")
	}
}

func TestNativeAnnouncementAllowsMultipleStreamsButNotIdleOrStoppedOutput(t *testing.T) {
	e, l, p, g := fixture(t)
	_ = e.Handle(g, session.Event{Kind: "announcement", Active: true})
	for _, pcm := range [][]int16{{-7}, {13, -23}} {
		_ = e.Output(g, pcm)
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
	if p.writes != 3 || len(l.commands) != 2 {
		t.Fatal("idle/stopped announcement audio played or claimed completion")
	}
	if err := e.StartManual(); err != nil {
		t.Fatal("stopping idle announcement wedged next turn", err)
	}
}
