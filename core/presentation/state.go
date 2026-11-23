// Package presentation contains snapshots only, not dashboard or timer logic.
package presentation

// Speaker is server attribution, not authentication. Presence flags distinguish
// an absent score/channel set from valid zero values. Values contain no aliases
// into the decoder or engine and may safely be retained by a renderer.
type Speaker struct {
	Name       string  `json:"name"`
	Score      float64 `json:"score"`
	ScoreKnown bool    `json:"score_known"`
	RGB        [3]byte `json:"rgb"`
	ColorKnown bool    `json:"color_known"`
}

// Output describes local playback ownership, never acoustic audibility or level.
// State is empty, queued, playing, draining or failed. Received means PCM reached
// the engine; playing/draining additionally require a successful host Write.
type Output struct {
	State    string `json:"state"`
	Owner    string `json:"owner"` // voice, announcement, alert or intercom
	Received bool   `json:"received"`
}

type Timer struct {
	Seconds int64  `json:"seconds"`
	Name    string `json:"name,omitempty"`
}

// TimerSlots is the number of countdowns in the native display projection.
const TimerSlots = 6

type Ring struct {
	Sequence uint64   `json:"sequence"` // local alert occurrence, not an upstream timer ID
	Name     string   `json:"name,omitempty"`
	Count    int      `json:"count,omitempty"`
	Names    []string `json:"names,omitempty"`
}

type State struct {
	Backend   string            `json:"backend"`
	Connected bool              `json:"connected"`
	Muted     bool              `json:"muted"`
	Phase     string            `json:"phase"`
	Heard     string            `json:"heard,omitempty"`
	Reply     string            `json:"reply,omitempty"`
	Alert     bool              `json:"alert"`
	Reason    string            `json:"reason,omitempty"`
	Timers    [TimerSlots]Timer `json:"timers,omitzero"` // zero rows are unused
	Ring      Ring              `json:"ring,omitzero"`

	Generation  uint64  `json:"generation"`
	Turn        uint64  `json:"turn"`
	Active      bool    `json:"active"`
	Announcing  bool    `json:"announcing"`           // upstream marker, not proof of PCM
	MonitorID   uint64  `json:"monitor_id,omitempty"` // request identity for the recording indicator
	Inhibited   bool    `json:"inhibited"`
	Invocation  string  `json:"invocation,omitempty"` // wake, manual or follow_up
	Input       string  `json:"input,omitempty"`      // tentative or listening
	InputOpen   bool    `json:"input_open"`           // actual call input, including Incubator work/output
	Accepted    bool    `json:"accepted"`             // backend accepted this turn
	Output      Output  `json:"output"`
	Speaker     Speaker `json:"speaker,omitzero"`
	Night       string  `json:"night"`        // unknown, off or on; connection scoped
	TalkAllowed bool    `json:"talk_allowed"` // engine permission, not capture health
	WakeAllowed bool    `json:"wake_allowed"` // engine permission, not model readiness

	// The Incubator adapter fills these using its connection-local ordinary
	// models and an explicit host capture-health fact. Engine snapshots alone
	// do not claim that a healthy microphone or an ordinary model exists.
	WakeModels   int  `json:"wake_models"`
	WakeArmed    bool `json:"wake_armed"`
	CaptureReady bool `json:"capture_ready"`
	TalkReady    bool `json:"talk_ready"`
	WakeReady    bool `json:"wake_ready"`
}
