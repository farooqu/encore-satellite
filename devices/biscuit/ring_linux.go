package biscuit

import (
	"encoding/hex"
	"math"
	"time"

	"github.com/farooqu/encore-satellite/core/presentation"
)

// RingOptions is visual policy only. Brightness is 1..100 percent of a
// conservative software ceiling, not calibrated perceived/electrical brightness.
type RingOptions struct {
	Brightness    int  `json:"brightness"`
	ReducedMotion bool `json:"reduced_motion"`
}

type volumeFeedback struct {
	steps int32
	muted bool
	at    time.Time
}

func ringBrightness(s presentation.State, options RingOptions) int {
	cap := max(1, 32*min(100, max(1, options.Brightness))/100)
	if s.Night != "off" {
		cap = min(cap, 8) // Unknown is never silently treated as day.
	}
	return cap
}

// The arc shows 0..100% of the allowed range, not uncapped hardware gain:
// a full ring is 30/120 (25% actual gain). A dim track makes zero visible.
func volumeRingFrame(s presentation.State, v volumeFeedback, options RingOptions) [36]byte {
	var frame [36]byte
	filled := float64(min(30, max(0, v.steps))) * 12 / 30
	if v.muted {
		filled = 0
	}
	cap := ringBrightness(s, options)
	for i := range 12 {
		level := byte(math.Round(float64(cap) / 2 * min(1, max(0, filled-float64(i)))))
		frame[i*3], frame[i*3+1], frame[i*3+2] = level, level, max(1, level)
	}
	return frame
}

func ringMode(s presentation.State) string {
	switch {
	case s.Muted:
		return "privacy"
	case !s.Connected || !s.CaptureReady:
		return "unavailable"
	case s.Inhibited:
		return "idle" // Stop/cancellation never resumes a saved animation.
	case s.Alert || (s.Output.Owner == "alert" && (s.Output.State == "playing" || s.Output.State == "draining")):
		return "alert"
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
// Every active segment stays lit. No channel exceeds 32 and no segment sum
// exceeds 48 (576 for the whole ring), including arbitrary profile RGB.
// These are software bounds, NOT a measured current/gamma or comfort guarantee.
func RingFrame(s presentation.State, level float64, elapsed time.Duration, options RingOptions) [36]byte {
	var frame [36]byte
	mode := ringMode(s)
	if mode == "idle" {
		return frame
	}
	cap := ringBrightness(s, options)
	still := options.ReducedMotion || s.Night != "off"
	if mode == "privacy" {
		for i := range 12 {
			frame[i*3] = byte(max(8, cap)) // No blackout of software privacy.
		}
		return frame
	}
	phase := max(0, elapsed.Seconds())
	for i := range 12 {
		color, intensity := [3]float64{1, .45, .02}, .55
		switch mode {
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
	}
	return frame
}

// ringRenderer is the sole frame writer. Neither capture nor a session callback
// calls it. Runtime supplies monotonic time; unchanged payloads are coalesced.
type ringRenderer struct {
	write             func([]byte) error
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
		if mode == "privacy" || mode == "unavailable" || mode == "alert" || s.Inhibited {
			// Never replay feedback that was hidden by privacy, a fault, Stop
			// or an alert after that higher-priority condition clears.
			r.volumeDismissed = v.at
		} else if v.at.After(r.volumeDismissed) && !now.Before(v.at) && now.Sub(v.at) < 1200*time.Millisecond {
			frame = volumeRingFrame(s, v, options)
		}
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
