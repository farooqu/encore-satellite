// Package sensors maps device-provided capabilities to the ESPHome entity API.
package sensors

import (
	"strings"

	"github.com/farooqu/encore-satellite/core/illuminance"
	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"
)

// Sources are supplied by the device composition root, not inferred from its
// model or form factor. A missing capability is omitted from discovery and
// state; a failed read of a supported capability is not absence.
type Sources struct {
	MicrophoneMuted  func() bool
	Illuminance      *illuminance.Monitor
	WiFi             func() (ssid, changeStatus string, err error)
	SSH              func() (running bool, fingerprints []string, err error)
	RunningVoiceMode func() string
}

func key(id string) uint32 { return (&esphome.Base{ObjectID: id}).Key() }

const (
	microphoneMutedID = "microphone_muted"
	illuminanceID     = "illuminance"
	wifiSSIDID        = "wifi_ssid"
	wifiStatusID      = "wifi_change_status"
	sshRunningID      = "ssh_running"
	sshFingerprintsID = "ssh_fingerprints"
	runningVoiceID    = "running_voice_mode"
)

// List never emits ListEntitiesDone; the composing endpoint owns that boundary.
func (s Sources) List(send func(proto.Message) error) error {
	for _, entity := range s.catalog() {
		if err := send(entity); err != nil {
			return err
		}
	}
	return nil
}

func (s Sources) catalog() []proto.Message {
	var entities []proto.Message
	if s.MicrophoneMuted != nil {
		entities = append(entities, &api.ListEntitiesBinarySensorResponse{Key: key(microphoneMutedID), ObjectId: microphoneMutedID, Name: "Microphone muted", Icon: "mdi:microphone-off"})
	}
	if s.Illuminance.Supported() {
		entities = append(entities, &api.ListEntitiesSensorResponse{Key: key(illuminanceID), ObjectId: illuminanceID, Name: "Illuminance", UnitOfMeasurement: "lx", DeviceClass: "illuminance", StateClass: api.SensorStateClass_STATE_CLASS_MEASUREMENT})
	}
	if s.WiFi != nil {
		entities = append(entities,
			&api.ListEntitiesTextSensorResponse{Key: key(wifiSSIDID), ObjectId: wifiSSIDID, Name: "Wi-Fi SSID", EntityCategory: esphome.CategoryDiagnostic},
			&api.ListEntitiesTextSensorResponse{Key: key(wifiStatusID), ObjectId: wifiStatusID, Name: "Wi-Fi change status", EntityCategory: esphome.CategoryDiagnostic},
		)
	}
	if s.SSH != nil {
		entities = append(entities,
			&api.ListEntitiesBinarySensorResponse{Key: key(sshRunningID), ObjectId: sshRunningID, Name: "SSH running", EntityCategory: esphome.CategoryDiagnostic},
			&api.ListEntitiesTextSensorResponse{Key: key(sshFingerprintsID), ObjectId: sshFingerprintsID, Name: "SSH public-key fingerprints", EntityCategory: esphome.CategoryDiagnostic},
		)
	}
	if s.RunningVoiceMode != nil {
		entities = append(entities, &api.ListEntitiesTextSensorResponse{Key: key(runningVoiceID), ObjectId: runningVoiceID, Name: "Running voice mode", EntityCategory: esphome.CategoryDiagnostic})
	}
	return entities
}

func (s Sources) MicrophoneState() *api.BinarySensorStateResponse {
	if s.MicrophoneMuted == nil {
		return nil
	}
	return &api.BinarySensorStateResponse{Key: key(microphoneMutedID), State: s.MicrophoneMuted()}
}

func (s Sources) IlluminanceState() *api.SensorStateResponse {
	if !s.Illuminance.Supported() {
		return nil
	}
	r, err := s.Illuminance.Snapshot()
	return &api.SensorStateResponse{Key: key(illuminanceID), State: float32(r.Lux), MissingState: err != nil}
}

func (s Sources) States() []proto.Message {
	var states []proto.Message
	if state := s.MicrophoneState(); state != nil {
		states = append(states, state)
	}
	if state := s.IlluminanceState(); state != nil {
		states = append(states, state)
	}
	if s.WiFi != nil {
		ssid, status, err := s.WiFi()
		if err != nil {
			status = "unavailable"
		}
		states = append(states,
			&api.TextSensorStateResponse{Key: key(wifiSSIDID), State: ssid, MissingState: err != nil},
			&api.TextSensorStateResponse{Key: key(wifiStatusID), State: status},
		)
	}
	if s.SSH != nil {
		running, fingerprints, err := s.SSH()
		states = append(states,
			&api.BinarySensorStateResponse{Key: key(sshRunningID), State: running, MissingState: err != nil},
			&api.TextSensorStateResponse{Key: key(sshFingerprintsID), State: strings.Join(fingerprints, ", "), MissingState: err != nil},
		)
	}
	if s.RunningVoiceMode != nil {
		states = append(states, &api.TextSensorStateResponse{Key: key(runningVoiceID), State: s.RunningVoiceMode()})
	}
	return states
}
