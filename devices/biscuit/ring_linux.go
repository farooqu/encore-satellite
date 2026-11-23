package biscuit

import (
	"encoding/hex"
	"math"
	"strings"
	"time"

	"github.com/farooqu/encore-satellite/core/presentation"
)

// The 6.1 driver latches PWM and current together. Every write must carry the
// quarter-current setting: its legacy frame interface resets current to full.
func scaledRingFrame(frame []byte) []byte {
	out := append([]byte(nil), frame[:len(frame)-1]...)
	out = append(out, strings.Repeat("03", 36)...)
	return append(out, '\n')
}

// RingOptions selects the stock daytime current/PWM brightness curve, not
// calibrated perceived brightness. Unknown night caps brightness at 50%.
type RingOptions struct {
	Brightness    int  `json:"brightness"`
	ReducedMotion bool `json:"reduced_motion"`
}

type volumeFeedback struct {
	steps int32
	muted bool
	at    time.Time
}

// Stock Biscuit Fire OS NS6574/7623 rgbTable.default indices 1..100, expressed
// in thousandths. HAL scaleDutyCycle truncates RGB*pwm; current selectors
// 3/2/1/0 mean quarter/third/half/full resistor-limited current. Index 101
// (PWM 1.0) is outside the stock user range and is deliberately not exposed.
var ringStockPWM = [...]int{
	137, 149, 161, 173, 184, 196, 204, 216, 227, 239,
	251, 263, 275, 282, 294, 306, 318, 329, 341, 353,
	361, 373, 384, 396, 408,
	325, 345, 361, 380, 396, 416, 431, 451, 467, 486,
	502, 522, 537, 557, 576, 592, 612, 627, 647, 663,
	682, 698, 718, 733, 753,
	514, 533, 553, 569, 588, 608, 624, 643, 663, 678,
	698, 718, 733, 753, 773, 788, 808, 827, 843, 863,
	882, 898, 918, 937, 957,
	494, 510, 525, 541, 557, 573, 588, 604, 620, 635,
	651, 667, 682, 698, 714, 729, 745, 761, 776, 792,
	808, 824, 839, 855, 875,
}

func ringDrive(s presentation.State, options RingOptions) (current byte, cap int) {
	brightness := min(100, max(1, options.Brightness))
	if s.Night == "on" {
		// Keep both current and PWM bounded: full current with PWM 8 would
		// quadruple the previous confirmed-night output.
		return 3, min(8, max(1, 32*brightness/100))
	}
	if s.Night != "off" {
		brightness = min(brightness, 50)
	}
	return byte((100 - brightness) / 25), 255 * ringStockPWM[brightness-1] / 1000
}

// The arc shows actual master software gain: full ring is 120/120 (unity),
// independently of the 50% startup level. A dim track makes zero visible.
func volumeRingFrame(s presentation.State, v volumeFeedback, options RingOptions) [36]byte {
	var frame [36]byte
	filled := float64(min(speakerMaxSteps, max(0, v.steps))) * 12 / speakerMaxSteps
	if v.muted {
		filled = 0
	}
	_, cap := ringDrive(s, options)
	for i := range 12 {
		level := byte(math.Round(float64(cap) / 2 * min(1, max(0, filled-float64(i)))))
		frame[i*3], frame[i*3+1], frame[i*3+2] = level, level, max(1, level)
	}
	return frame
}

func ringMode(s presentation.State) string {
	switch {
	case s.Connected && !s.Muted && s.MonitorID != 0:
		return "monitor"
	case s.Connected && (!s.Inhibited || s.Muted) &&
		(s.Alert || (s.Output.Owner == "alert" && (s.Output.State == "playing" || s.Output.State == "draining"))):
		return "alert"
	case s.Muted:
		return "privacy"
	case !s.Connected || !s.CaptureReady:
		return "unavailable"
	case s.Inhibited:
		return "idle" // Stop/cancellation never resumes a saved animation.
	case s.Output.State == "failed":
		return "unavailable"
	case s.Output.State == "playing" || s.Output.State == "draining":
		return "output"
	case s.Output.State == "queued" || s.Announcing:
		return "queued"
	case s.Input == "tentative":
		return "tentative"
	case s.Input == "listening":
		return "input"
	case s.Active && s.Phase == "working":
		return "processing"
	case s.Phase == "error" && (s.Reason == "wake_rejected" || s.Reason == "answered_elsewhere"):
		return "idle"
	case s.Phase == "error" || !s.WakeReady:
		return "unavailable"
	default:
		return "idle"
	}
}

// RingFrame composes the Dot 2's 12 RGB segments deterministically. Level is a
// bounded, already-smoothed actual microphone envelope (0..1), not direction.
// Every active segment stays lit. Channels are bounded by the stock brightness
// PWM (223 at 100%); segment sums stay within 1.5 times that bound. The existing
// color/envelope budget is retained, not a reproduction of Amazon animations
// or its thermal policy, nor a measured current/gamma or comfort guarantee.
func RingFrame(s presentation.State, level float64, elapsed time.Duration, options RingOptions) [36]byte {
	var frame [36]byte
	mode := ringMode(s)
	if mode == "idle" {
		return frame
	}
	_, cap := ringDrive(s, options)
	still := options.ReducedMotion || s.Night != "off"
	if mode == "privacy" {
		for i := range 12 {
			frame[i*3] = byte(max(8, cap)) // No blackout of software privacy.
		}
		return frame
	}
	phase := max(0, elapsed.Seconds())
	for i := range 12 {
		if mode == "alert" && s.Muted && (i == 3 || i == 9) {
			frame[i*3] = byte(max(8, cap)) // Keep the mic privacy cue visible while ringing.
			continue
		}
		color, intensity := [3]float64{1, .45, .02}, .55
		switch mode {
		case "monitor":
			color, intensity = [3]float64{1, .04, .27}, .85
			if i%2 != 0 {
				intensity = .42
			}
		case "input", "tentative":
			color = [3]float64{.13, 1, .68}
			intensity = .35
			if mode == "input" {
				if math.IsNaN(level) {
					level = 0
				}
				intensity = .45 + .55*min(1, max(0, level))
			}
		case "processing":
			color = [3]float64{.2, .38, 1}
			rotation := phase / 2.4
			if still {
				rotation = 0
			}
			crest := math.Pow((1+math.Cos(2*math.Pi*(float64(i)/12-rotation)))/2, 3)
			intensity = .45 + .55*crest
			if s.Speaker.ColorKnown {
				rgb := s.Speaker.RGB
				peak := max(rgb[0], rgb[1], rgb[2])
				if peak > 0 {
					// A bounded secondary accent cannot turn processing into
					// solid privacy-red/alert-amber or erase its blue family.
					weight := .35 * crest
					for c := range color {
						color[c] = color[c]*(1-weight) + float64(rgb[c])/float64(peak)*weight
					}
				}
			}
		case "output", "queued":
			color = [3]float64{.68, .3, 1}
			intensity = .6
			if mode == "output" && !still {
				intensity = .55 + .45*(1-math.Cos(2*math.Pi*phase/1.8))/2
			}
		case "alert":
			intensity = .7
			if !still {
				intensity = .5 + .5*(1-math.Cos(2*math.Pi*phase/3.2))/2
			}
		}
		// Square the scalar envelope, preserving the color ratios. This is
		// untuned software easing, not a measured LED gamma transform.
		scale := float64(cap) * intensity * intensity / max(1, (color[0]+color[1]+color[2])/1.5)
		dominant := 0
		for c := range color {
			frame[i*3+c] = byte(math.Floor(color[c] * scale))
			if color[c] > color[dominant] {
				dominant = c
			}
		}
		frame[i*3+dominant] = max(1, frame[i*3+dominant])
		if mode == "monitor" && i%2 == 0 {
			// Keep a magenta accent distinct from privacy red at minimum PWM.
			frame[i*3+2] = max(1, frame[i*3+2])
		}
	}
	return frame
}

// ringRenderer is the sole frame writer. Neither capture nor a session callback
// calls it. Runtime supplies monotonic time; unchanged payloads are coalesced.
type ringRenderer struct {
	write             func([]byte) error
	setCurrent        func(byte) error
	current           byte
	currentWritten    bool
	volume            func() volumeFeedback
	volumeDismissed   time.Time
	last              [36]byte
	written           bool
	mode              string
	generation, turn  uint64
	entered, previous time.Time
	level             float64
}

func (r *ringRenderer) update(s presentation.State, mic captureReading, now time.Time, options RingOptions) error {
	mode := ringMode(s)
	if mode != r.mode || s.Generation != r.generation || s.Turn != r.turn {
		r.mode, r.generation, r.turn = mode, s.Generation, s.Turn
		r.entered, r.previous, r.level = now, now, 0
	}
	if mode == "input" && mic.ready && !mic.at.Before(r.entered) {
		target := min(1, mic.level*8) // Visual gain only; does not alter PCM.
		tau := 180 * time.Millisecond
		if target > r.level {
			tau = 40 * time.Millisecond
		}
		r.level += (target - r.level) * (1 - math.Exp(-max(0, now.Sub(r.previous).Seconds())/tau.Seconds()))
	} else {
		r.level = 0
	}
	r.previous = now
	frame := RingFrame(s, r.level, now.Sub(r.entered), options)
	if r.volume != nil {
		v := r.volume()
		if mode == "privacy" || mode == "unavailable" || mode == "alert" || mode == "monitor" || s.Inhibited {
			// Never replay feedback that was hidden by privacy, a fault, Stop
			// or an alert after that higher-priority condition clears.
			r.volumeDismissed = v.at
		} else if v.at.After(r.volumeDismissed) && !now.Before(v.at) && now.Sub(v.at) < 1200*time.Millisecond {
			frame = volumeRingFrame(s, v, options)
		}
	}
	current, _ := ringDrive(s, options)
	if r.setCurrent != nil && (!r.currentWritten || current != r.current) {
		// Sysfs has no atomic current+PWM transaction. Blank before changing
		// current so an old high-PWM frame cannot flash at the new current.
		blank := make([]byte, 73)
		hex.Encode(blank[:72], make([]byte, 36))
		blank[72] = '\n'
		if err := r.write(blank); err != nil {
			return err
		}
		r.last, r.written = [36]byte{}, true
		r.currentWritten = false
		if err := r.setCurrent(current); err != nil {
			return err
		}
		r.current, r.currentWritten = current, true
	}
	if r.written && frame == r.last {
		return nil
	}
	payload := make([]byte, hex.EncodedLen(len(frame))+1)
	hex.Encode(payload, frame[:])
	payload[len(payload)-1] = '\n'
	if err := r.write(payload); err != nil {
		return err
	}
	r.last, r.written = frame, true
	return nil
}
