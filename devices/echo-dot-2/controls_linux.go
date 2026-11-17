package dot

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/farooqu/encore-satellite/core/session"
	"github.com/farooqu/encore-satellite/platforms/echo"
	"github.com/farooqu/encore-satellite/platforms/echo/input"
)

const ringPath = "/sys/bus/i2c/devices/0-003f/"

func openButtons() ([]*input.Device, error) {
	devices, err := input.List()
	if err != nil {
		return nil, err
	}
	// The stock driver names are source evidence, not a discovered Encore
	// hardware matrix. Refuse a base that does not expose both key groups.
	var kept []*input.Device
	keypad, gpio := false, false
	for _, d := range devices {
		if strings.Contains(d.Name, "mtk-kpd") || strings.Contains(d.Name, "gpio-keys") {
			kept = append(kept, d)
			keypad = keypad || strings.Contains(d.Name, "mtk-kpd")
			gpio = gpio || strings.Contains(d.Name, "gpio-keys")
		} else {
			_ = d.Close()
		}
	}
	if !keypad || !gpio {
		for _, d := range kept {
			_ = d.Close()
		}
		return nil, errors.New("dot: expected mtk-kpd and gpio-keys input devices")
	}
	return kept, nil
}

func buttons(ctx context.Context, d *input.Device, gate *echo.Privacy, engine *session.Engine, player *speaker) error {
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
			player.adjust(-1)
		case 115:
			player.adjust(1)
		case 138:
			_, _ = gate.Check(func() {
				s := engine.Snapshot()
				if s.Phase == "idle" && !s.Alert {
					_ = engine.StartManual()
				} else {
					player.StopMusic()
					_ = engine.Stop()
				}
			})
		}
	}
}
