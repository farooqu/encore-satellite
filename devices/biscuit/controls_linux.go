package biscuit

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/farooqu/encore-satellite/core/session"
	"github.com/farooqu/encore-satellite/platforms/linux"
	"github.com/farooqu/encore-satellite/platforms/linux/input"
)

const ringPath = "/sys/bus/i2c/devices/0-003f/"

// A 40 ms, 660 Hz cue with 5 ms linear attack/release, before the same capped
// master gain as voice. It is best-effort: busy playback/privacy/Stop suppress it.
var volumeCue = func() []int16 {
	samples := make([]int16, 640)
	for i := range samples {
		ramp := min(1, float64(i)/80, float64(len(samples)-1-i)/80)
		samples[i] = int16(math.Round(4000 * ramp * math.Sin(2*math.Pi*660*float64(i)/16000)))
	}
	return samples
}()

func (s *speaker) pressVolume(delta int32) {
	s.adjust(delta)
	s.TryCue(volumeCue)
}

func openButtons() ([]*input.Device, error) {
	devices, err := input.List()
	if err != nil {
		return nil, err
	}
	return buttonDevices(devices)
}

// buttonDevices takes ownership of the discovered nodes, closing unrelated
// devices and refusing a base without PMIC mute, volume and Action groups.
func buttonDevices(devices []*input.Device) ([]*input.Device, error) {
	var kept []*input.Device
	var found uint8
	for _, d := range devices {
		group := buttonGroup(d.Name)
		if group != 0 {
			kept = append(kept, d)
			found |= group
		} else {
			_ = d.Close()
		}
	}
	if found != 7 {
		for _, d := range kept {
			_ = d.Close()
		}
		return nil, errors.New("dot: required board input groups are missing")
	}
	return kept, nil
}

func buttonGroup(name string) uint8 {
	// gpio_keys uses the platform node name, not its compatible string.
	switch name {
	case "mtk-pmic-keys":
		return 1
	case "keys":
		return 2
	case "action_key":
		return 4
	}
	return 0
}

func buttons(ctx context.Context, d *input.Device, gate *linux.Privacy, engine *session.Engine, player *speaker, apiAction chan<- struct{}) error {
	// The Dot keypad emits down/up, not autorepeat. Keep reads interruptible
	// while this worker repeats held volume keys, as Techo5 does every 200 ms.
	events := make(chan input.Event)
	failed := make(chan error, 1)
	stop, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		for {
			e, err := d.Read()
			if err != nil {
				failed <- err
				return
			}
			select {
			case events <- e:
			case <-stop:
				return
			}
		}
	}()
	defer func() { close(stop); _ = d.Close(); <-done }()
	repeat := time.NewTicker(200 * time.Millisecond)
	repeat.Stop()
	defer repeat.Stop()
	var tick <-chan time.Time
	down := make(map[uint16]bool)
	for {
		var e input.Event
		select {
		case <-ctx.Done():
			return nil
		case err := <-failed:
			if ctx.Err() != nil {
				return nil
			}
			return err
		case <-tick:
			delta := int32(0)
			if down[114] {
				delta--
			}
			if down[115] {
				delta++
			}
			if delta != 0 {
				player.adjust(delta)
			}
			continue
		case e = <-events:
		}
		if e.Type == input.EvSyn && e.Code == 3 {
			return errors.New("dot: dropped button events")
		}
		if e.Type != input.EvKey {
			continue
		}
		switch e.Code {
		case 113, 114, 115, 138:
		default:
			continue
		}
		if e.Value == 0 {
			delete(down, e.Code)
			if !down[114] && !down[115] {
				repeat.Stop()
				tick = nil
			}
			continue
		}
		if e.Value != 1 || down[e.Code] {
			continue
		}
		if (e.Code == 114 || e.Code == 115) && !down[114] && !down[115] {
			repeat.Reset(200 * time.Millisecond)
			tick = repeat.C
		}
		down[e.Code] = true
		switch e.Code {
		case 113:
			// Driver acts BEFORE evdev, with up to 500ms state-report lag.
			// Inhibit now and observe state later; NEVER toggle the latch.
			gate.Inhibit(500 * time.Millisecond)
			player.SetMusicPrivacy(true)
			engine.SetMuted(true)
		case 114:
			player.pressVolume(-1)
		case 115:
			player.pressVolume(1)
		case 138:
			if buttonGroup(d.Name) == 4 {
				select {
				case apiAction <- struct{}{}:
				default: // No waiting first-pair window; never queue an authorization.
				}
			}
			_, _ = gate.Check(func() {
				s := engine.Snapshot()
				if s.Phase == "idle" && !s.Active && !s.Alert {
					_ = engine.StartManual()
				} else {
					player.StopMusic()
					_ = engine.Stop()
				}
			})
		}
	}
}
