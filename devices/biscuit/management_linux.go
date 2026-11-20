package biscuit

import (
	"context"
	"encoding/json"
	"time"

	"github.com/farooqu/encore-satellite/backends/esphome/sensors"
	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"
)

// Management belongs to this Linux device host, not the shared voice backend or
// Android applications. Only biscuitControls' authenticated Noise endpoint exposes it.
type management struct {
	ssh     *sshManager
	wifi    *wifiManager
	actions *esphome.Entities
	args    map[uint32]int
}

var sshEnabledKey = (&esphome.Base{ObjectID: "ssh_enabled"}).Key()

func newManagement(ssh *sshManager) (*management, error) {
	m := &management{ssh: ssh, wifi: newWiFiManager(), actions: esphome.NewEntities(), args: make(map[uint32]int)}
	var definitions []*esphome.Action
	if ssh != nil {
		sshStatus := func() (any, error) {
			s, err := ssh.status()
			return map[string]any{"enabled": s.Enabled, "running": s.Running, "fingerprints": s.Fingerprints}, err
		}
		definitions = []*esphome.Action{
			{Name: "ssh_status", Description: "Read SSH enablement, running status and public-key fingerprints", Answers: true,
				Run: func(esphome.Call) (any, error) { return sshStatus() }},
			{Name: "ssh_add_key", Description: "Add a public key; test a new SSH login before removing the old key", Answers: true,
				Args: []esphome.Arg{{Name: "public_key", Type: esphome.ArgString}}, Run: func(c esphome.Call) (any, error) {
					if err := m.ssh.addKey(c.String("public_key")); err != nil {
						return nil, err
					}
					return sshStatus()
				}},
			{Name: "ssh_remove_key", Description: "Remove an authorized public key by its SHA256 fingerprint", Answers: true,
				Args: []esphome.Arg{{Name: "fingerprint", Type: esphome.ArgString}}, Run: func(c esphome.Call) (any, error) {
					if err := m.ssh.removeKey(c.String("fingerprint")); err != nil {
						return nil, err
					}
					return sshStatus()
				}},
			{Name: "ssh_set_enabled", Description: "Enable key-only SSH or disable SSH and terminate its sessions", Answers: true,
				Args: []esphome.Arg{{Name: "enabled", Type: esphome.ArgBool}}, Run: func(c esphome.Call) (any, error) {
					if err := m.ssh.setEnabled(c.Bool("enabled")); err != nil {
						return nil, err
					}
					return sshStatus()
				}},
		}
	}
	definitions = append(definitions,
		&esphome.Action{Name: "wifi_status", Description: "Read network change status; never returns credentials", Answers: true,
			Run: func(esphome.Call) (any, error) { return m.wifi.status() }},
		&esphome.Action{Name: "wifi_stage", Description: "Try new WPA2 credentials; reconnect and confirm the returned transaction ID or the old network is restored. Keep passwords out of HA traces and saved automations.", Answers: true,
			Args: []esphome.Arg{{Name: "ssid", Type: esphome.ArgString}, {Name: "password", Type: esphome.ArgString}}, Run: func(c esphome.Call) (any, error) {
				return m.wifi.stage(c.String("ssid"), c.String("password"))
			}},
		&esphome.Action{Name: "wifi_confirm", Description: "Commit a successfully joined candidate network using its transaction ID", Answers: true,
			Args: []esphome.Arg{{Name: "id", Type: esphome.ArgString}}, Run: func(c esphome.Call) (any, error) {
				return m.wifi.confirm(c.String("id"))
			}},
		&esphome.Action{Name: "wifi_cancel", Description: "Cancel a network change and restore the saved network", Answers: true,
			Args: []esphome.Arg{{Name: "id", Type: esphome.ArgString}}, Run: func(c esphome.Call) (any, error) {
				return m.wifi.cancel(c.String("id"))
			}},
	)
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
	if m.ssh != nil {
		sources.SSH = func() (bool, []string, error) {
			status, err := m.ssh.status()
			return status.Running, status.Fingerprints, err
		}
	}
	return sources
}

func (m *management) list(ctx context.Context, c *esphome.Conn) error {
	if m.ssh != nil {
		if err := c.Send(&api.ListEntitiesSwitchResponse{Key: sshEnabledKey, ObjectId: "ssh_enabled", Name: "SSH enabled", Icon: "mdi:console-network", EntityCategory: esphome.CategoryConfig}); err != nil {
			return err
		}
	}
	// Exactly one ListEntitiesDone, after music, Bluetooth and management.
	return m.actions.Handle(ctx, c, &api.ListEntitiesRequest{})
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

func (m *management) states() ([]proto.Message, *api.SwitchStateResponse) {
	sources := m.sensorSources()
	if m.ssh == nil {
		return sources.States(), nil
	}
	// The writable switch and diagnostics must describe the same SSH snapshot.
	status, err := m.ssh.status()
	sources.SSH = func() (bool, []string, error) { return status.Running, status.Fingerprints, err }
	return sources.States(), &api.SwitchStateResponse{Key: sshEnabledKey, State: status.Enabled, MissingState: err != nil}
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
			states, ssh := d.management.states()
			if ssh != nil {
				states = append(states, ssh)
			}
			b, _ := json.Marshal(states) // Only public status, never action arguments.
			if string(b) != previous {
				_ = d.broadcast()
				previous = string(b)
			}
		}
	}
}
