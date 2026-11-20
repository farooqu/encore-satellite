package music

import "testing"

func TestCommandDispatch(t *testing.T) {
	for _, command := range []Command{Play, Pause, Stop, Next, Previous, "", "resume", "switch"} {
		for _, ready := range []bool{false, true} {
			for _, supported := range []bool{false, true} {
				known := command != "" && command != "resume" && command != "switch"
				if got := command.CanDispatch(ready, supported); got != (known && ready && supported) {
					t.Errorf("%q ready=%t supported=%t: CanDispatch=%t", command, ready, supported, got)
				}
			}
		}
	}
	if Pause == Stop {
		t.Fatal("Pause and Stop must remain distinct intents")
	}
}
