package biscuit

import (
	"context"
	"encoding/binary"
	"io/fs"
	"testing"
	"testing/synctest"
	"time"

	"github.com/farooqu/encore-satellite/internal/esphomepairing"
)

func TestSetupClipsAreBoundedStandalonePCM(t *testing.T) {
	for _, cue := range []setupCue{improvReady, wifiConnected, haReady, pairingOpen, pairingComplete, wifiFailed, appFailed, pairingTimeout} {
		t.Run(string(cue), func(t *testing.T) {
			wav, err := fs.ReadFile(onboardingClips, "onboarding/"+string(cue)+".wav")
			if err != nil {
				t.Fatal(err)
			}
			if len(wav) < 44 || string(wav[8:16]) != "WAVEfmt " ||
				binary.LittleEndian.Uint32(wav[16:]) != 16 || binary.LittleEndian.Uint32(wav[20:]) != 0x00010001 ||
				binary.LittleEndian.Uint32(wav[24:]) != 16000 || binary.LittleEndian.Uint32(wav[32:]) != 0x00100002 ||
				string(wav[36:40]) != "data" || int(binary.LittleEndian.Uint32(wav[40:])) != len(wav)-44 || len(wav)%2 != 0 {
				t.Fatal("not canonical 16 kHz mono PCM16")
			}
			total, audible := 0, false
			err = cue.stream(t.Context(), func(block []int16) error {
				total += len(block)
				for _, sample := range block {
					audible = audible || sample > 300 || sample < -300
				}
				return nil
			})
			if err != nil || !audible || total != (len(wav)-44)/2 || total >= int(setupSpeechLimit/time.Second)*16000 {
				t.Fatalf("clip not playable within setup speech budget: samples=%d audible=%t err=%v", total, audible, err)
			}
		})
	}
}

func TestSetupSpeechDoesNotDelayNextTransition(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		f := &setupFeedback{wake: make(chan struct{}, 1)}
		started := make(chan setupCue, 2)
		go f.speak(ctx, func(clipCtx context.Context, cue setupCue) error {
			started <- cue
			<-clipCtx.Done()
			return clipCtx.Err()
		})
		f.emit(haReady)
		if got := <-started; got != haReady {
			t.Fatalf("first cue = %q", got)
		}
		before := time.Now()
		f.emit(pairingOpen)
		if got := <-started; got != pairingOpen || time.Since(before) != 0 {
			t.Fatalf("Action waited for stale speech: %q after %s", got, time.Since(before))
		}
	})
}

func TestSetupRingShowsWaitingCountdownAndPrivacy(t *testing.T) {
	start := time.Now()
	options := RingOptions{Brightness: 50}
	count := func(frame [36]byte, channel int) int {
		n := 0
		for i := range 12 {
			if frame[3*i+channel] != 0 {
				n++
			}
		}
		return n
	}
	frame := func(cue setupCue, age time.Duration, muted bool) [36]byte {
		return (setupEvent{cue, start}).frame(start.Add(age), muted, options)
	}
	if amber, white := frame(improvReady, 0, false), frame(haReady, 0, false); count(amber, 2) != 0 || count(amber, 1) != 12 || count(white, 2) != 12 {
		t.Fatal("Improv and HA waiting are not visually distinct")
	}
	if got := count(frame(pairingOpen, esphomepairing.Window*3/4, false), 2); got != 3 {
		t.Fatalf("pairing countdown at 3/4 elapsed = %d, want 3", got)
	}
	if green := frame(wifiConnected, setupFlash/4, false); count(green, 1) != 7 || count(green, 0) != 0 || count(green, 2) != 0 {
		t.Fatal("Wi-Fi success did not display a green sweep")
	}
	for _, cue := range []setupCue{wifiFailed, pairingTimeout, appFailed} {
		fault := frame(cue, 0, false)
		if count(fault, 0) != 6 || count(fault, 1) != 0 || count(fault, 2) != 0 {
			t.Fatalf("%q should show a red half-ring", cue)
		}
	}
	if got := frame(pairingTimeout, setupFlash, false); got != frame(haReady, 0, false) {
		t.Fatal("pairing timeout did not restore the HA waiting ring")
	}
	muted := frame(improvReady, 0, true)
	if count(muted, 0) != 12 || count(muted, 1) != 0 || count(muted, 2) != 0 {
		t.Fatal("mute is not steady full red")
	}
	for _, cue := range []setupCue{wifiConnected, pairingOpen, pairingComplete, wifiFailed, pairingTimeout, appFailed} {
		if frame(cue, 0, true) != muted || frame(cue, setupFlash, true) != muted {
			t.Fatalf("%q obscured mic mute", cue)
		}
	}
}
