// Package illuminance maps optional light hardware to the ESPHome sensor API.
package illuminance

import (
	"github.com/farooqu/encore-satellite/core/illuminance"
	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
)

var key = (&esphome.Base{ObjectID: "illuminance"}).Key()

// List never emits ListEntitiesDone; the composing endpoint owns that boundary.
func List(c *esphome.Conn, m *illuminance.Monitor) error {
	if !m.Supported() {
		return nil
	}
	return c.Send(&api.ListEntitiesSensorResponse{Key: key, ObjectId: "illuminance", Name: "Illuminance", UnitOfMeasurement: "lx", DeviceClass: "illuminance", StateClass: api.SensorStateClass_STATE_CLASS_MEASUREMENT})
}

func State(m *illuminance.Monitor) *api.SensorStateResponse {
	if !m.Supported() {
		return nil
	}
	r, err := m.Snapshot()
	return &api.SensorStateResponse{Key: key, State: float32(r.Lux), MissingState: err != nil}
}
