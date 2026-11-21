package biscuit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/farooqu/encore-satellite/backends/esphome/sensors"
	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
)

// Management belongs to this Linux device host, not the shared voice backend or
// Android applications. Only biscuitControls' authenticated Noise endpoint exposes it.
type management struct {
	wifi    *wifiManager
	actions *esphome.Entities
	args    map[uint32]int
}

func newManagement() (*management, error) {
	m := &management{wifi: newWiFiManager(), actions: esphome.NewEntities(), args: make(map[uint32]int)}
	definitions := []*esphome.Action{
		{Name: "wifi_status", Description: "Read network change status; never returns credentials", Answers: true,
			Run: func(esphome.Call) (any, error) { return m.wifi.status() }},
		{Name: "wifi_stage", Description: "Try new WPA2 credentials; reconnect and confirm the returned transaction ID or the old network is restored. Keep passwords out of HA traces and saved automations.", Answers: true,
			Args: []esphome.Arg{{Name: "ssid", Type: esphome.ArgString}, {Name: "password", Type: esphome.ArgString}}, Run: func(c esphome.Call) (any, error) {
				return m.wifi.stage(c.String("ssid"), c.String("password"))
			}},
		{Name: "wifi_confirm", Description: "Commit a successfully joined candidate network using its transaction ID", Answers: true,
			Args: []esphome.Arg{{Name: "id", Type: esphome.ArgString}}, Run: func(c esphome.Call) (any, error) {
				return m.wifi.confirm(c.String("id"))
			}},
		{Name: "wifi_cancel", Description: "Cancel a network change and restore the saved network", Answers: true,
			Args: []esphome.Arg{{Name: "id", Type: esphome.ArgString}}, Run: func(c esphome.Call) (any, error) {
				return m.wifi.cancel(c.String("id"))
			}},
	}
	for _, a := range definitions {
		// Action keys use the library's public ESPHome object-id hash.
		m.args[(&esphome.Base{ObjectID: "action:" + a.Name}).Key()] = len(a.Args)
	}
	if err := m.actions.AddActions(definitions...); err != nil {
		return nil, err
	}
	return m, nil
}

func (m *management) sensorSources() sensors.Sources {
	var sources sensors.Sources
	if m.wifi != nil {
		sources.WiFi = func() (string, string, error) {
			status, err := m.wifi.status()
			return status.SSID, status.State, err
		}
	}
	return sources
}

func (m *management) execute(ctx context.Context, c *esphome.Conn, r *api.ExecuteServiceRequest) error {
	n, ok := m.args[r.Key]
	if !ok || len(r.Args) != n {
		if r.ReturnResponse {
			return c.Send(&api.ExecuteServiceResponse{CallId: r.CallId, ErrorMessage: "unknown management action or invalid arguments"})
		}
		return nil
	}
	return m.actions.Handle(ctx, c, r)
}

func (d *biscuitControls) watchManagement(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var previous string
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			states := d.management.sensorSources().States()
			b, _ := json.Marshal(states) // Only public status, never action arguments.
			if string(b) != previous {
				_ = d.broadcast()
				previous = string(b)
			}
		}
	}
}
