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
	MonitorID    uint64
	RequestID    uint64
}

// Link.Send must copy/enqueue without blocking on I/O, using a bounded queue.
// discard_audio and monitor_off synchronously revoke their queued capture.
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
	Kind    string
	Text    string
	Reason  string
	Listen  bool
	Active  bool
	Speaker presentation.Speaker
	Night   *bool
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
	turn                            uint64
	monitorSerial                   uint64
	monitorVisible                  bool
	intercom                        bool
	invocation                      string
	responsePending                 bool
	outputOwner                     string
	outputAccepted, outputDraining  bool
	outputFailed                    bool
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
	cancelOutput                    context.CancelCauseFunc
	localOutput                     context.Context
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
	s := e.state
	s.Generation, s.Turn = e.generation, e.turn
	if s.Night == "" {
		s.Night = "unknown"
	}
	s.Inhibited = e.link == nil || s.Muted || e.cancelling || e.discard
	s.InputOpen = e.inputOpen && !s.Inhibited
	s.Speaker = presentation.Speaker{}
	if s.Inhibited && e.localOutput == nil {
		return s
	}
	s.Active = e.active || e.outputOpen || e.announcing || s.Alert || s.MonitorID != 0
	s.Announcing = e.announcing
	s.TalkAllowed = !s.Active
	// This is engine permission only. Incubator additionally checks whether
	// its ordinary model worker has armed and the host reports healthy capture.
	s.WakeAllowed = !s.Active
	if e.active || (e.outputOpen && e.outputOwner == "voice") {
		s.Invocation = e.invocation
		s.Speaker = e.state.Speaker
	}
	if e.active {
		s.Accepted = e.runStarted
		if e.inputOpen && (s.Phase == "tentative" || s.Phase == "listening") {
			s.Input = s.Phase
		}
	}
	if e.outputOpen {
		s.Output = presentation.Output{State: "queued", Owner: e.outputOwner, Received: e.outputReceived}
		if e.outputFailed {
			s.Output.State = "failed"
		} else if e.outputAccepted {
			s.Output.State = "playing"
			if e.outputDraining {
				s.Output.State = "draining"
			}
		}
	} else if e.active && e.responsePending {
		s.Output = presentation.Output{State: "queued", Owner: "voice"}
	}
	return s
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
	active := e.active || e.outputOpen || e.announcing || e.state.Alert || e.intercom
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
	if e.localOutput == nil {
		e.resetOutput()
	}
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
	e.cancelOutput(outputFence("session_disconnected"))
	e.localOutput = nil
	e.player.Flush()
	e.pre.Clear()
	e.active, e.discard, e.haveFrame = false, false, false
	e.outputTimer = false
	e.inputOpen, e.outputOpen, e.runEnded = false, false, false
	e.cancelling, e.streamExpected, e.streamEnded = false, false, false
	e.runStarted = false
	e.outputReceived = false
	e.announcing = false
	e.monitorVisible, e.intercom = false, false
	e.invocation, e.outputOwner = "", ""
	e.responsePending, e.outputAccepted, e.outputDraining, e.outputFailed = false, false, false, false
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
		if e.state.MonitorID != 0 {
			// Hard fence for off-call recording, including blocked writes.
			e.disconnect("monitor_privacy")
			return
		}
		active, alert := e.active || (e.outputOpen && e.localOutput == nil) || e.announcing, e.state.Alert
		wasCancelling := e.cancelling
		e.cancelling = e.cancelling || active || e.discard
		e.cancelOutput(outputFence("mute_fence"))
		e.localOutput = nil
		e.player.Flush()
		e.pre.Clear()
		e.haveFrame = false
		e.active, e.inputOpen, e.outputOpen, e.announcing = false, false, false, false
		e.state.Heard, e.state.Reply, e.state.Alert = "", "", false
		e.state.Speaker = presentation.Speaker{}
		if alert {
			_ = e.send(Command{Kind: "stop_alert"})
		}
		if e.cancelling && e.state.Backend == string(Incubator) {
			// Also retire interrupted output whose Stop/drain already cleared
			// active ownership. Never wait for a terminal that cannot arrive.
			if !wasCancelling {
				_ = e.send(Command{Kind: "cancel", Reason: "cancelled"})
			}
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
	_, err := e.CaptureForModels(frame)
	return err
}

// CaptureForModels reports whether the same frame may enter local inference.
// Monitor audio never becomes call pre-roll or a later wake-model input.
func (e *Engine) CaptureForModels(frame audio.Frame) (bool, error) {
	if err := frame.Validate(); err != nil {
		return false, err
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.state.Muted || e.cancelling || e.link == nil {
		// Preserve local-model delivery policy for non-monitoring hosts;
		// the model worker separately checks arming and generation.
		return true, nil
	}
	if frame.Discontinuity || (e.haveFrame && (frame.Sequence != e.lastSequence+1 || frame.CapturedAt <= e.lastTime)) {
		e.disconnect("capture_discontinuity")
		return false, errors.New("session: capture discontinuity; reconnect required")
	}
	e.lastSequence, e.lastTime, e.haveFrame = frame.Sequence, frame.CapturedAt, true
	if id := e.state.MonitorID; id != 0 {
		if e.monitorVisible {
			return false, e.send(Command{Kind: "monitor_audio", Samples: frame.Samples, MonitorID: id})
		}
		return false, nil
	}
	if e.inputOpen {
		err := e.send(Command{Kind: "audio", Samples: frame.Samples})
		return err == nil, err
	}
	if !e.active && !e.discard {
		e.pre.Add(frame.Samples)
	}
	return true, nil
}

// SetMonitor changes off-call recording for the requesting connection only.
// The host must confirm the indicator before any frame is sent.
func (e *Engine) SetMonitor(generation uint64, active, intercom bool, requestID uint64) (id uint64, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	defer e.publishActivity()
	if generation != e.generation || e.link == nil {
		return 0, ErrStale
	}
	if requestID != 0 && active {
		// Queue the result before capture can enqueue any monitored PCM.
		defer func() {
			if err == nil || errors.Is(err, ErrUnavailable) {
				if sendErr := e.send(Command{Kind: "monitor_result", MonitorID: id, RequestID: requestID}); sendErr != nil {
					err = sendErr
				}
			}
		}()
	}
	intercom = active && intercom
	leaving := e.intercom && !intercom
	if leaving {
		e.intercom = false
		e.fenceIntercomOutput()
	}
	if e.state.MonitorID != 0 && (!active || leaving || intercom != e.intercom) {
		if err := e.revokeMonitor(); err != nil {
			return 0, err
		}
	}
	if !active || (e.state.MonitorID != 0 && e.intercom == intercom) {
		return e.state.MonitorID, nil
	}
	if e.state.Muted || e.active || e.outputOpen || e.announcing || e.state.Alert || e.cancelling || (e.discard && !leaving) {
		return 0, ErrUnavailable
	}
	e.intercom = intercom
	e.monitorSerial++
	e.state.MonitorID = e.monitorSerial
	e.pre.Clear()
	return e.state.MonitorID, nil
}

func (e *Engine) revokeMonitor() error {
	id := e.state.MonitorID
	if id == 0 {
		return nil
	}
	e.state.MonitorID, e.monitorVisible = 0, false
	return e.send(Command{Kind: "monitor_off", MonitorID: id})
}

func (e *Engine) fenceIntercomOutput() {
	if e.discard {
		return
	}
	if e.outputOpen && e.outputOwner == "intercom" {
		e.resetOutput()
		e.player.Flush()
		e.outputOpen = false
	}
	// MONITOR off is not a playback completion. Discard until the relay's
	// AUDIO_OUT_END so neither late speech nor its terminator can claim Drain.
	e.discard = true
}

// ConfirmMonitor is called only after the host visibly indicates the exact request.
func (e *Engine) ConfirmMonitor(id uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if id != 0 && id == e.state.MonitorID {
		e.monitorVisible = true
	}
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
	listen = !e.active && !e.outputOpen && !e.discard && !e.announcing && !e.state.Alert && e.state.MonitorID == 0
	return
}

// IdleFor permits connection-local configuration storage even while muted.
// Unlike presentation phase, it includes outstanding output/cancellation owners.
// It grants no permission to capture or execute wake models.
func (e *Engine) IdleFor(generation uint64) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return generation == e.generation && e.link != nil && !e.active && !e.outputOpen &&
		!e.announcing && !e.state.Alert && !e.discard && !e.cancelling
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
	if e.state.Muted || e.cancelling || e.link == nil || e.active || e.outputOpen || e.discard || e.state.MonitorID != 0 {
		return ErrUnavailable
	}
	e.resetOutput()
	e.state.Heard, e.state.Reply, e.state.Reason = "", "", ""
	e.state.Speaker = presentation.Speaker{}
	e.turn++
	e.invocation = "wake"
	if detection.Word == "manual" || detection.Word == "follow_up" {
		e.invocation = detection.Word
	}
	e.responsePending = false
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
// cannot revive the cancelled call. Native calls/announcements retire their
// connection, just like mute; timer-only cancellation keeps its own end fence.
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
	if e.intercom {
		if e.state.MonitorID == 0 { // local Stop already notified the peer
			return nil
		}
		e.fenceIntercomOutput()
		e.publishActivity()
		if err := e.revokeMonitor(); err != nil {
			return err
		}
		return e.send(Command{Kind: "intercom_end"})
	}
	if e.state.MonitorID != 0 {
		e.disconnect("monitor_stopped")
		return nil
	}
	active, alert := e.active, e.state.Alert
	localOnly := e.localOutput != nil && !e.active && !e.announcing
	// A timer-only stream has an unambiguous dismissal. Calls and announcements
	// do not: an ordinary end/HANGUP may already be buffered before Stop.
	retire := e.state.Backend == string(Incubator) && !e.cancelling &&
		(e.active || e.announcing || (e.outputOpen && !e.outputTimer && !localOnly))
	e.cancelOutput(outputFence("session_stop"))
	e.localOutput = nil
	e.player.Flush()
	e.pre.Clear()
	e.discard = e.discard || e.active || (e.outputOpen && !localOnly)
	if e.state.Backend == string(Incubator) {
		e.discard = e.discard || e.announcing
	}
	e.cancelling = e.cancelling || retire
	e.active = false
	e.announcing = false
	e.inputOpen, e.outputOpen = false, false
	e.state.Heard, e.state.Reply, e.state.Alert = "", "", false
	e.state.Speaker = presentation.Speaker{}
	e.responsePending = false
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
	} else if !e.discard {
		// Idle Stop (including MODEL_CLEAR) has no cancelled owner to await.
		e.resetOutput()
	}
	e.publishActivity()
	if e.state.Backend == string(Incubator) {
		// Dismiss timers before cancellation closes this voice connection.
		if alert {
			if err := e.send(Command{Kind: "stop_alert"}); err != nil {
				return err
			}
		}
		if retire {
			return e.send(Command{Kind: "cancel", Reason: "cancelled"})
		}
		return nil
	}
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
	// Area visual policy is connection scoped, not turn scoped. It can update
	// during mute/cancellation but never from an old reader.
	if event.Kind == "night" {
		if event.Night != nil {
			e.state.Night = "off"
			if *event.Night {
				e.state.Night = "on"
			}
		}
		return nil
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
		e.state.Speaker = presentation.Speaker{}
		if !e.outputOpen && e.state.Phase != "speaking" && !e.discard {
			e.active = false
			e.state.Phase = "idle"
		}
		return nil
	}
	if event.Kind == "hangup" {
		e.active = false
		e.inputOpen = false
		e.responsePending = false
		e.pre.Clear()
		e.state.Phase, e.state.Reason = "idle", event.Reason
		// Incubator owns election. A losing candidate returns quietly; its
		// evidence was already sent without any local peer pre-election.
		if event.Reason == "answered_elsewhere" || event.Reason == "wake_rejected" {
			e.state.Speaker = presentation.Speaker{}
			// HANGUP ends the call, not the healthy native connection. Closing
			// it here makes ordinary wake rejection trigger upstream backoff.
			e.cancelOutput(outputFence("native_hangup"))
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
			e.state.Speaker = presentation.Speaker{}
		}
	case "processing":
		// Incubator repeats processing to replace speaker attribution. A late
		// metadata event must not turn an output/failed turn back into input work.
		if !e.active {
			break
		}
		if e.state.Backend == string(Incubator) && (e.outputOpen || e.state.Phase == "speaking" || e.state.Phase == "error") {
			break
		}
		e.state.Phase = "working"
		e.state.Speaker = event.Speaker
		if e.state.Backend == string(Assist) {
			e.inputOpen = false
		}
	case "output_start":
		if e.localOutput == nil && (e.active || e.state.Backend == string(Incubator)) {
			e.streamExpected, e.streamEnded = true, false
			e.openOutput()
			e.outputReceived = false
			e.outputAccepted = false
		}
	case "recognized":
		if e.active {
			e.state.Heard = event.Text
		}
	case "responding":
		if e.active {
			e.state.Phase, e.state.Reply = "speaking", event.Text
			e.responsePending = !e.outputOpen
		}
	case "not_recognized", "wake_rejected":
		if e.active {
			e.state.Phase, e.state.Reason = "error", event.Kind
			e.state.Speaker = presentation.Speaker{}
		}
	}
	return nil
}

// Output and EndOutput are called serially by a backend reader. They release the
// state lock during blocking playback so Stop/mute never queue behind a drain.
func (e *Engine) Output(generation uint64, samples []int16) error {
	return e.outputSamples(generation, 0, samples)
}

// OutputIntercom admits queued relay PCM only for its original monitor request.
func (e *Engine) OutputIntercom(generation, monitorID uint64, samples []int16) error {
	return e.outputSamples(generation, monitorID, samples)
}

func (e *Engine) outputSamples(generation, monitorID uint64, samples []int16) error {
	if len(samples) == 0 || len(samples) > 2048 {
		return errors.New("session: invalid output block")
	}
	e.mu.Lock()
	if generation != e.generation || e.link == nil {
		e.mu.Unlock()
		return ErrStale
	}
	if monitorID != 0 && (!e.intercom || e.state.MonitorID != monitorID) {
		e.mu.Unlock()
		return nil
	}
	if e.localOutput != nil {
		e.mu.Unlock()
		return nil
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
	// Outside an accepted call, only explicitly owned native streams may play.
	if e.state.Backend == string(Incubator) {
		// Upstream can retain an announcement task across socket reconnect.
		// CALL_START cancels it before listening: local start alone is not
		// authority to play PCM that arrived before upstream accepted the call.
		if (e.active && !e.runStarted) || (!e.active && !e.state.Alert && !e.announcing && !(e.intercom && e.state.MonitorID != 0)) {
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
	err := e.player.Write(ctx, samples)
	e.mu.Lock()
	defer e.mu.Unlock()
	if monitorID != 0 && (ctx != e.output || e.state.MonitorID != monitorID) {
		return nil // A retired relay write cannot fail the new stream or link.
	}
	// A blocked Write can finish after Stop, mute or connection replacement.
	// It must not reassert playback in the newer presentation.
	if generation == e.generation && ctx == e.output && e.outputOpen && !e.discard && !e.cancelling && !e.state.Muted {
		if err == nil {
			e.outputAccepted = true
		} else {
			e.outputFailed = true
		}
	}
	return err
}

func (e *Engine) EndOutput(generation uint64) error {
	e.mu.Lock()
	if generation != e.generation || e.link == nil {
		e.mu.Unlock()
		return ErrStale
	}
	if e.localOutput != nil {
		e.mu.Unlock()
		return nil
	}
	if e.streamExpected || e.state.Backend == string(Incubator) {
		e.streamEnded = true
	}
	if e.cancelling || e.state.Muted {
		e.finishCancellation()
		e.mu.Unlock()
		return nil
	}
	e.responsePending = false
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
	e.outputDraining = !discarded
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
		e.outputOwner = "voice"
		if e.outputTimer {
			e.outputOwner = "alert"
		} else if e.announcing && !e.active {
			e.outputOwner = "announcement"
		} else if e.intercom && !e.active {
			e.outputOwner = "intercom"
		}
		e.outputAccepted, e.outputDraining, e.outputFailed = false, false, false
		e.outputReceived = false
		e.responsePending = false
		if e.outputOwner != "voice" {
			e.state.Speaker = presentation.Speaker{}
		}
		e.outputOpen = true
	}
}

func (e *Engine) resetOutput() {
	if e.cancelOutput != nil {
		e.cancelOutput(nil)
	}
	e.localOutput = nil
	e.output, e.cancelOutput = context.WithCancelCause(context.Background())
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
