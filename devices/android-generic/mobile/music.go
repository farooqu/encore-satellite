package mobile

import "github.com/farooqu/encore-satellite/core/music"

// Use basic string constants so gomobile can expose the intents with the
// existing Android engine binding.
const (
	SendspinPlay     = string(music.Play)
	SendspinPause    = string(music.Pause)
	SendspinStop     = string(music.Stop)
	SendspinNext     = string(music.Next)
	SendspinPrevious = string(music.Previous)
)

// SendspinCommandAllowed checks an intent against the Android player's live
// connection and the intersection of its implemented and advertised commands.
func SendspinCommandAllowed(command string, ready, supported bool) bool {
	return music.Command(command).CanDispatch(ready, supported)
}
