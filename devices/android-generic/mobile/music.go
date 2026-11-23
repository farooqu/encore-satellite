package mobile

import (
	"errors"

	"github.com/farooqu/encore-satellite/backends/esphome/controls"
	"github.com/farooqu/encore-satellite/core/music"
	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"
)

// SendspinHost requests a change to the native player's saved setting.
// The host applies it on its own thread and reports it with SetSendspinEnabled.
type SendspinHost interface {
	RequestSendspinEnabled(enabled bool)
}

// ConfigureSendspin installs the native music control before Start.
func (r *Runtime) ConfigureSendspin(host SendspinHost, enabled bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		return errors.New("Sendspin must be configured before startup")
	}
	r.sendspinHost, r.sendspinEnabled = host, enabled
	r.sendspinChanges = make(chan struct{}, 1)
	return nil
}

// SetSendspinEnabled reflects the persisted preference, not connection readiness.
func (r *Runtime) SetSendspinEnabled(enabled bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sendspinEnabled != enabled {
		r.sendspinEnabled = enabled
		if !enabled {
			r.sendspinPairing = esphome.SendspinPairing{}
		}
		select {
		case r.sendspinChanges <- struct{}{}:
		default:
		}
	}
}

func (r *Runtime) sendMusicState(send func(proto.Message) error) error {
	r.sendspinStateMu.Lock()
	defer r.sendspinStateMu.Unlock()
	r.mu.Lock()
	host, enabled := r.sendspinHost, r.sendspinEnabled
	r.mu.Unlock()
	if host == nil {
		return nil
	}
	return send(&api.SwitchStateResponse{Key: controls.Sendspin.Key(), State: enabled})
}

// SetSendspinPairing publishes the running player's credential for the paired
// ESPHome connection only. The Android host clears it before stopping music.
func (r *Runtime) SetSendspinPairing(clientID, token string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.control != nil && r.sendspinEnabled {
		r.sendspinPairing = esphome.SendspinPairing{ClientID: clientID, Token: token}
	}
}

func (r *Runtime) musicPairing() esphome.SendspinPairing {
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.sendspinEnabled {
		return esphome.SendspinPairing{Status: api.SendspinPairingTokenStatus_SENDSPIN_PAIRING_TOKEN_STATUS_DISABLED}
	}
	return r.sendspinPairing
}

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
