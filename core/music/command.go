// Package music defines Encore's Sendspin command contract. Connection,
// pairing, retry, and audio policy belong to the supplying player.
package music

// Command is a playback intent, not an SDK or process wire value. A player
// implements only the intents it can honor. Stop is distinct from Pause: the
// Biscuit player stops local output until an explicit Play.
type Command string

const (
	Play     Command = "play"
	Pause    Command = "pause"
	Stop     Command = "stop"
	Next     Command = "next"
	Previous Command = "previous"
)

// CanDispatch requires a known intent that the current player supports and a
// current command target. Each adapter supplies these conditions: for example,
// readiness may mean an authenticated controller or a live local receiver, not
// merely that the feature is enabled. A rejected intent must not be replayed
// when a later connection becomes available.
func (c Command) CanDispatch(ready, supported bool) bool {
	if !ready || !supported {
		return false
	}
	switch c {
	case Play, Pause, Stop, Next, Previous:
		return true
	default:
		return false
	}
}
