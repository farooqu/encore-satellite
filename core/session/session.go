// Package session owns one voice session and its privacy boundary. Backend and
// platform adapters depend on this package; it imports neither of them.
package session

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/core/presentation"
	"github.com/farooqu/encore-satellite/core/wake"
)

type Backend string

const (
	Assist    Backend = "assist"
	Incubator Backend = "incubator"
)

// Command is a satellite-to-backend action, independent of wire encodings.
type Command struct {
	Kind         string
	Detection    wake.Detection
	PreRollBytes int
	Samples      []int16
	Reason       string
}

// Link.Send must copy/enqueue without blocking on I/O, using a bounded queue.
// discard_audio synchronously purges unsent capture without a wire command.
// Native cancel sends cancellation then closes its voice connection; the reader
// reports Disconnect before a replacement may attach. Assist cancel stays open.
// Overflow fails the connection, not silently drops audio. Close invalidates
// queued and in-flight network audio and promptly unblocks all transport I/O.
type Link interface {
	Backend() Backend
	Send(Command) error
	Close() error
}

// Event contains only actually supplied upstream presentation data.
type Event struct {
	Kind   string
	Text   string
	Reason string
	Listen bool
	Active bool
}

var (
	ErrUnavailable = errors.New("session: disconnected, muted, busy or awaiting cancelled-stream end")
	ErrStale       = errors.New("session: stale backend connection")
)

type Engine struct {
	mu                              sync.Mutex
	player                          audio.Playback
	state                           presentation.State
	link                            Link
	generation                      uint64
	active, discard                 bool
	outputTimer                     bool
	inputOpen, outputOpen, runEnded bool
	outputReceived                  bool
	announcing                      bool
	cancelling                      bool
	runStarted                      bool
	streamExpected, streamEnded     bool
	pre                             audio.PreRoll
	lastSequence                    uint64
	lastTime                        time.Duration
	haveFrame                       bool
	output                          context.Context
	cancelOutput                    context.CancelFunc
	activityHandler                 func(bool)
	voiceActive                     bool
}

func New(player audio.Playback) *Engine {
	e := &Engine{player: player, state: presentation.State{Backend: string(Incubator), Phase: "disconnected"}}
	e.resetOutput()
	return e
}

func (e *Engine) Snapshot() presentation.State {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.state
}

// SetActivityHandler connects speaker policy to the entire local voice activity,
// including listening, processing, response drain, announcements and alerts.
// The handler runs under the session lock: it must return promptly and must not
// call the engine. It receives current state immediately. Music gain, user mute
// and privacy/stop gates remain the output host's responsibility.
func (e *Engine) SetActivityHandler(handler func(bool)) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.activityHandler = handler
	if handler != nil {
		handler(e.voiceActive)
	}
}

func (e *Engine) publishActivity() {
	active := e.active || e.outputOpen || e.announcing || e.state.Alert
	if active != e.voiceActive {
		e.voiceActive = active
		if e.activityHandler != nil {
			e.activityHandler(active)
		}
	}
}

// Select explicitly changes ownership, even when the requested adapter is not
// installed. It never falls back silently. Settings/pairing belong to adapters.
func (e *Engine) Select(backend Backend) error {
	if backend != Assist && backend != Incubator {
		return errors.New("session: unknown backend")
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if string(backend) != e.state.Backend {
		e.disconnect("backend_changed")
		e.state.Backend = string(backend)
	}
	return nil
}

// Attach returns a connection generation required on every incoming operation.
// A stale reader cannot update presentation or restart audio after replacement.
func (e *Engine) Attach(link Link) (uint64, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if string(link.Backend()) != e.state.Backend || e.link != nil {
		return 0, ErrUnavailable
	}
	e.generation++
	e.link = link
	e.state.Connected = true
	e.state.Phase = "idle"
	if e.state.Muted {
		e.state.Phase = "muted"
	}
	e.state.Reason = ""
	e.resetOutput()
	return e.generation, nil
}

func (e *Engine) Disconnect(generation uint64, reason string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if generation == e.generation {
		e.disconnect(reason)
	}
}

func (e *Engine) disconnect(reason string) {
	e.generation++
	if e.link != nil {
		_ = e.link.Close()
		e.link = nil
	}
	e.cancelOutput()
	e.player.Flush()
	e.pre.Clear()
	e.active, e.discard, e.haveFrame = false, false, false
	e.outputTimer = false
	e.inputOpen, e.outputOpen, e.runEnded = false, false, false
	e.cancelling, e.streamExpected, e.streamEnded = false, false, false
	e.runStarted = false
	e.outputReceived = false
	e.announcing = false
	e.state = presentation.State{Backend: e.state.Backend, Muted: e.state.Muted, Phase: "disconnected", Reason: reason}
	if e.state.Muted {
		e.state.Phase = "muted"
	}
	e.publishActivity()
}

// SetMuted silences locally and cancels the current pipeline without revoking
// the shared Assist/BLE transport. Native cancellation retires its voice-only
// connection: ordinary and cancellation stream ends are indistinguishable.
// A new turn waits for Assist completion or a fresh native connection.
// The host must ALSO gate/stop capture immediately at its OS boundary.
func (e *Engine) SetMuted(muted bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	defer e.publishActivity()
	if e.state.Muted == muted {
		return
	}
	e.state.Muted = muted
	if muted {
		active, alert := e.active || e.outputOpen || e.announcing, e.state.Alert
		e.cancelling = e.cancelling || active || e.discard
		e.cancelOutput()
		e.player.Flush()
		e.pre.Clear()
		e.haveFrame = false
		e.active, e.inputOpen, e.outputOpen, e.announcing = false, false, false, false
		e.state.Heard, e.state.Reply, e.state.Alert = "", "", false
		if alert {
			_ = e.send(Command{Kind: "stop_alert"})
		}
		if e.cancelling && e.state.Backend == string(Incubator) {
			// Also retire interrupted output whose Stop/drain already cleared
			// active ownership. Never wait for a terminal that cannot arrive.
			_ = e.send(Command{Kind: "cancel", Reason: "cancelled"})
		} else if active && e.runStarted {
			_ = e.send(Command{Kind: "cancel", Reason: "cancelled"})
		} else if active {
			// Purge capture now; HA must establish RUN_START before wire abort.
			_ = e.send(Command{Kind: "discard_audio"})
		}
	} else if !e.cancelling {
		// Idle mute also cancelled playback; timers/announcements have no start.
		e.resetOutput()
	}
	e.finishCancellation()
}

// Assist RUN_END can precede streamed TTS. No native stream end can prove that
// an old HANGUP is not still buffered; its adapter closes after sending cancel.
// Only the ensuing disconnect may release native cancellation ownership.
func (e *Engine) finishCancellation() {
	finished := false
	if e.state.Backend == string(Assist) {
		finished = e.runEnded && (!e.streamExpected || e.streamEnded)
	}
	if e.cancelling && finished {
		e.cancelling, e.discard = false, false
		e.resetOutput()
	}
	if e.state.Muted {
		e.state.Phase = "muted"
	} else if e.link == nil {
		e.state.Phase = "disconnected"
	} else if e.cancelling {
		e.state.Phase = "recovering"
	} else {
		e.state.Phase = "idle"
	}
}

func (e *Engine) Capture(frame audio.Frame) error {
	if err := frame.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state.Muted || e.cancelling || e.link == nil {
		return nil
	}
	if frame.Discontinuity || (e.haveFrame && (frame.Sequence != e.lastSequence+1 || frame.CapturedAt <= e.lastTime)) {
		e.disconnect("capture_discontinuity")
		return errors.New("session: capture discontinuity; reconnect required")
	}
	e.lastSequence, e.lastTime, e.haveFrame = frame.Sequence, frame.CapturedAt, true
	if e.inputOpen {
		return e.send(Command{Kind: "audio", Samples: frame.Samples})
	}
	if !e.active && !e.discard {
		e.pre.Add(frame.Samples)
	}
	return nil
}

// FinishInput is an explicit host endpoint, not local speech recognition.
// Incubator owns endpointing upstream and has no equivalent half-close message.
func (e *Engine) FinishInput() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state.Backend != string(Assist) || !e.inputOpen {
		return ErrUnavailable
	}
	e.inputOpen = false
	return e.send(Command{Kind: "input_end"})
}

func (e *Engine) StartManual() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.start(wake.Detection{Word: "manual"}, nil)
}

func (e *Engine) Wake(detection wake.Detection) error {
	if err := detection.Validate(); err != nil {
		return err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.start(detection, e.pre.Snapshot())
}

// WakeState scopes local model execution to its owning backend connection.
// Stop models are armed only for response output, announcements and alerts;
// ordinary models run only while genuinely idle. Inference runs outside this lock.
func (e *Engine) WakeState(generation uint64) (listen, stop bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if generation != e.generation || e.link == nil || e.state.Muted {
		return false, false
	}
	stop = !e.discard && (e.outputOpen || e.announcing || e.state.Alert)
	listen = !e.active && !e.outputOpen && !e.discard && !e.announcing && !e.state.Alert
	return
}

// WakeFor accepts a model result only for the connection that supplied its
// model. Both generation and arming are checked again after inference. Unlike
// host/manual Wake, the reserved stop identity cancels rather than starts a call.
func (e *Engine) WakeFor(generation uint64, detection wake.Detection) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if generation != e.generation || e.link == nil {
		return ErrStale
	}
	if e.state.Muted || e.discard {
		return ErrUnavailable
	}
	if detection.Word == "stop" {
		if !e.outputOpen && !e.announcing && !e.state.Alert {
			return ErrUnavailable
		}
		return e.stop()
	}
	if err := detection.Validate(); err != nil {
		return err
	}
	if e.announcing || e.state.Alert {
		return ErrUnavailable
	}
	return e.start(detection, e.pre.Snapshot())
}

func (e *Engine) start(detection wake.Detection, pre []int16) error {
	if e.state.Muted || e.cancelling || e.link == nil || e.active || e.outputOpen || e.discard {
		return ErrUnavailable
	}
	e.resetOutput()
	e.state.Heard, e.state.Reply, e.state.Reason = "", "", ""
	e.state.Phase = "tentative"
	if detection.Word == "manual" || detection.Word == "follow_up" {
		e.state.Phase = "listening"
	}
	e.active = true
	e.inputOpen, e.runEnded = true, false
	e.runStarted = false
	e.streamExpected, e.streamEnded = false, false
	e.outputReceived = false
	e.pre.Clear()
	e.publishActivity() // Duck before the backend can begin capture or respond.
	if err := e.send(Command{Kind: "start", Detection: detection, PreRollBytes: len(pre) * 2}); err != nil {
		return err
	}
	for len(pre) > 0 {
		n := min(len(pre), audio.MaxSamples)
		if err := e.send(Command{Kind: "audio", Samples: pre[:n]}); err != nil {
			return err
		}
		pre = pre[n:]
	}
	return nil
}

// Stop silences locally before notifying the peer; late output and presentation
// cannot revive the cancelled call. A new call waits for AUDIO_OUT_END (or a
// native timer's timer_stopped), or reconnect.
func (e *Engine) Stop() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stop()
}

// StopFor prevents a stale model-control reader from cancelling a newer peer.
func (e *Engine) StopFor(generation uint64) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	if generation != e.generation || e.link == nil {
		return ErrStale
	}
	return e.stop()
}

func (e *Engine) stop() error {
	e.cancelOutput()
	e.player.Flush()
	e.pre.Clear()
	e.discard = e.discard || e.active || e.outputOpen
	active, alert := e.active, e.state.Alert
	e.active = false
	e.announcing = false
	e.inputOpen, e.outputOpen = false, false
	e.state.Heard, e.state.Reply, e.state.Alert = "", "", false
	e.state.Reason = "cancelled"
	e.state.Phase = "idle"
	if e.link == nil {
		e.state.Phase = "disconnected"
	}
	if e.state.Muted {
		e.state.Phase = "muted"
	}
	if e.cancelling {
		e.finishCancellation()
	}
	e.publishActivity()
	// End discards unsent capture in the transport queue. Send it first so
	// it cannot discard the timer dismissal when both owners are active.
	if active {
		if err := e.send(Command{Kind: "end", Reason: "cancelled"}); err != nil {
			return err
		}
	}
	if alert {
		return e.send(Command{Kind: "stop_alert"})
	}
	return nil
}

func (e *Engine) Handle(generation uint64, event Event) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	defer e.publishActivity()
	if generation != e.generation || e.link == nil {
		return ErrStale
	}
	if event.Kind == "run_start" && !e.runStarted {
		e.runStarted = true
		if e.cancelling {
			// HA can cancel an unscheduled task without emitting RUN_END.
			// Local mute is immediate; defer abort until the run can finish.
			return e.send(Command{Kind: "cancel", Reason: "cancelled"})
		}
	}
	if e.cancelling {
		switch event.Kind {
		case "run_end":
			e.runEnded = true
		case "output_expected":
			e.streamExpected = true
		case "output_start":
			e.streamExpected, e.streamEnded = true, false
		}
		e.finishCancellation()
		return nil
	}
	if e.state.Muted {
		return nil
	}
	if event.Kind == "error" {
		e.disconnect(event.Reason)
		return nil
	}
	if event.Kind == "run_end" {
		e.inputOpen, e.runEnded = false, true
		if !e.outputOpen && e.state.Phase != "speaking" && !e.discard {
			e.active = false
			e.state.Phase = "idle"
		}
		return nil
	}
	if event.Kind == "hangup" {
		e.active = false
		e.inputOpen = false
		e.pre.Clear()
		e.state.Phase, e.state.Reason = "idle", event.Reason
		// Incubator owns election. A losing candidate returns quietly; its
		// evidence was already sent without any local peer pre-election.
		if event.Reason == "answered_elsewhere" || event.Reason == "wake_rejected" {
			// HANGUP ends the call, not the healthy native connection. Closing
			// it here makes ordinary wake rejection trigger upstream backoff.
			e.cancelOutput()
			e.player.Flush()
			e.outputOpen, e.outputReceived = false, false
			if !e.discard {
				e.outputTimer = false
			}
			e.state.Heard, e.state.Reply = "", ""
			e.resetOutput()
			return nil
		}
		if event.Listen && !e.discard {
			return e.start(wake.Detection{Word: "follow_up"}, nil)
		}
		return nil
	}
	if event.Kind == "timer_stopped" && e.discard && e.outputTimer {
		// Cancelling a native paced ring can omit AUDIO_OUT_END. This event
		// ends only that ring's fence, never a cancelled voice call's fence.
		e.discard, e.outputTimer = false, false
		e.resetOutput()
	}
	if e.discard {
		return nil
	}
	switch event.Kind {
	case "output_expected":
		if e.active {
			e.streamExpected = true
		}
	case "announcement":
		e.announcing = event.Active
	case "timer_ringing":
		e.state.Alert = true
	case "timer_stopped":
		e.state.Alert = false
	case "listening":
		if e.active {
			e.runStarted = true
			e.state.Phase = "listening"
		}
	case "processing":
		if e.active {
			e.state.Phase = "working"
			if e.state.Backend == string(Assist) {
				e.inputOpen = false
			}
		}
	case "output_start":
		if e.active || e.state.Backend == string(Incubator) {
			e.streamExpected, e.streamEnded = true, false
			e.openOutput()
			e.outputReceived = false
		}
	case "recognized":
		if e.active {
			e.state.Heard = event.Text
		}
	case "responding":
		if e.active {
			e.state.Phase, e.state.Reply = "speaking", event.Text
		}
	case "not_recognized", "wake_rejected":
		if e.active {
			e.state.Phase, e.state.Reason = "error", event.Kind
		}
	}
	return nil
}

// Output and EndOutput are called serially by a backend reader. They release the
// state lock during blocking playback so Stop/mute never queue behind a drain.
func (e *Engine) Output(generation uint64, samples []int16) error {
	if len(samples) == 0 || len(samples) > 2048 {
		return errors.New("session: invalid output block")
	}
	e.mu.Lock()
	if generation != e.generation || e.link == nil {
		e.mu.Unlock()
		return ErrStale
	}
	if e.state.Muted || e.cancelling {
		e.mu.Unlock()
		return nil
	}
	// Assist announcements are not advertised. A stream cannot appear before
	// a local turn and its TTS_STREAM_START. HA can send a cancelled task's
	// final STREAM_END on its freshly reconnected client.
	if e.state.Backend == string(Assist) && (!e.active || !e.outputOpen) {
		e.mu.Unlock()
		return nil
	}
	// Native call output ends at HANGUP. Only explicitly signaled upstream
	// alerts/announcements may play outside a call; idle frames cannot revive it.
	if e.state.Backend == string(Incubator) {
		// Upstream can retain an announcement task across socket reconnect.
		// CALL_START cancels it before listening: local start alone is not
		// authority to play PCM that arrived before upstream accepted the call.
		if (e.active && !e.runStarted) || (!e.active && !e.state.Alert && !e.announcing) {
			e.mu.Unlock()
			return nil
		}
	}
	ctx, discard := e.output, e.discard
	if !discard {
		e.openOutput()
		e.outputReceived = true
	}
	e.publishActivity()
	e.mu.Unlock()
	if discard {
		return nil
	}
	return e.player.Write(ctx, samples)
}

func (e *Engine) EndOutput(generation uint64) error {
	e.mu.Lock()
	if generation != e.generation || e.link == nil {
		e.mu.Unlock()
		return ErrStale
	}
	if e.streamExpected || e.state.Backend == string(Incubator) {
		e.streamEnded = true
	}
	if e.cancelling || e.state.Muted {
		e.finishCancellation()
		e.mu.Unlock()
		return nil
	}
	if !e.outputOpen && !e.discard {
		e.mu.Unlock()
		return nil
	}
	if e.state.Backend == string(Assist) && !e.outputReceived && !e.discard {
		// This HA version can end an empty stream after TTS fails without
		// sending an ERROR event. No received audio means no playback claim.
		e.outputOpen, e.outputTimer = false, false
		if e.runEnded {
			e.active = false
			e.state.Phase = "idle"
		} else {
			e.state.Phase = "working"
		}
		e.publishActivity()
		e.mu.Unlock()
		return nil
	}
	ctx, discarded := e.output, e.discard
	e.mu.Unlock()
	var err error
	if !discarded {
		err = e.player.Drain(ctx)
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	defer e.publishActivity()
	if generation != e.generation || e.link == nil {
		return ErrStale
	}
	if ctx != e.output || e.cancelling || e.state.Muted {
		return nil
	}
	e.outputOpen, e.outputTimer = false, false
	if ctx.Err() != nil || discarded || e.discard {
		e.discard = false
		e.resetOutput()
		return nil // Cancelled samples were not played: never claim they drained.
	}
	if err != nil {
		e.disconnect("playback_failed")
		return err
	}
	if e.runEnded {
		e.active = false
		e.state.Phase = "idle"
	} else if e.state.Backend == string(Assist) {
		e.state.Phase = "working"
	}
	return e.send(Command{Kind: "drained"})
}

func (e *Engine) openOutput() {
	if !e.outputOpen {
		// Ownership lasts through cancellation until the stream's boundary;
		// HANGUP or a later timer event cannot relabel an open voice stream.
		e.outputTimer = e.state.Backend == string(Incubator) && e.state.Alert && !e.active && !e.announcing
		e.outputOpen = true
	}
}

func (e *Engine) resetOutput() {
	if e.cancelOutput != nil {
		e.cancelOutput()
	}
	e.output, e.cancelOutput = context.WithCancel(context.Background())
}

func (e *Engine) send(command Command) error {
	if e.link == nil {
		return ErrUnavailable
	}
	if err := e.link.Send(command); err != nil {
		e.disconnect("transport_failed")
		return err
	}
	return nil
}
