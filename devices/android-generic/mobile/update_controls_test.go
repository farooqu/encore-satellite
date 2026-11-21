package mobile

import (
	"context"
	"net"
	"testing"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
)

type updateRequests chan bool

func (c updateRequests) RequestUpdate(_ string, _ int64, install bool) { c <- install }

func TestAPKUpdateCapabilityOnESPHome(t *testing.T) {
	for _, supported := range []bool{false, true} {
		name := "without updater"
		if supported {
			name = "with updater"
		}
		t.Run(name, func(t *testing.T) {
			r, err := Open(t.TempDir(), silentOutput{})
			if err != nil {
				t.Fatal(err)
			}
			requests := make(updateRequests, 2)
			if supported {
				r.ConfigureUpdates(requests, "1.2.3")
			}
			l, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(t.Context())
			done := make(chan error, 1)
			s := &esphome.Server{Handler: r, WriteTimeout: time.Second}
			r.control = s
			go func() { done <- s.Serve(ctx, l) }()
			t.Cleanup(func() { cancel(); <-done })
			send, receive := loopbackPeer(t, l.Addr().String())
			send(&api.ListEntitiesRequest{})
			var update, check, diagnostic uint32
		listing:
			for {
				switch v := receive().(type) {
				case *api.ListEntitiesTextResponse:
					if v.ObjectId == "dashboard_url" {
						t.Fatal("renderer-less runtime advertised dashboard URL")
					}
				case *api.ListEntitiesUpdateResponse:
					if v.ObjectId == "apk_update" {
						update = v.Key
					}
				case *api.ListEntitiesTextSensorResponse:
					if v.ObjectId == "apk_update_status" {
						diagnostic = v.Key
					}
				case *api.ListEntitiesButtonResponse:
					if v.ObjectId == "reload_dashboard" {
						t.Fatal("renderer-less runtime advertised dashboard reload")
					}
					if v.ObjectId == "check_updates" {
						check = v.Key
					}
				case *api.ListEntitiesDoneResponse:
					break listing
				}
			}
			if (!supported && (update != 0 || check != 0)) || (supported && (update != 0x454e4303 || check != 0x454e4305)) {
				t.Fatalf("unexpected update capability: update=%x check=%x", update, check)
			}
			if (diagnostic != 0) != supported {
				t.Fatalf("unexpected APK diagnostic: %x", diagnostic)
			}
			send(&api.SubscribeStatesRequest{})
			var state *api.UpdateStateResponse
			var microphone, bluetooth bool
			for !microphone || !bluetooth {
				switch v := receive().(type) {
				case *api.UpdateStateResponse:
					state = v
				case *api.TextStateResponse:
					if v.Key == dashboardKey {
						t.Fatal("renderer-less runtime published dashboard state")
					}
				case *api.BinarySensorStateResponse:
					microphone = true
				case *api.SwitchStateResponse:
					bluetooth = true
				}
			}
			if (state != nil) != supported || (supported && (state.Key != update || state.CurrentVersion != "1.2.3" || state.LatestVersion != state.CurrentVersion || !state.MissingState)) {
				t.Fatalf("unexpected update state: %v", state)
			}
			if err := r.Handle(ctx, nil, &api.TextCommandRequest{Key: dashboardKey, State: "https://dashboard.example.test"}); err != nil || r.config.Dashboard != "" {
				t.Fatalf("renderer-less runtime handled cached dashboard command: %v", err)
			}
			if supported {
				send(&api.ButtonCommandRequest{Key: check})
				select {
				case install := <-requests:
					if install {
						t.Fatal("check command attempted installation")
					}
				case <-time.After(time.Second):
					t.Fatal("check command did not reach updater")
				}
				assertStatus := func(latest, status string, known, busy bool) {
					t.Helper()
					if got, ok := receive().(*api.UpdateStateResponse); !ok || got.CurrentVersion != "1.2.3" ||
						got.LatestVersion != latest || got.MissingState == known || got.InProgress != busy {
						t.Fatalf("unexpected update state: %v", got)
					}
					if got, ok := receive().(*api.TextSensorStateResponse); !ok || got.Key != diagnostic || got.State != status {
						t.Fatalf("unexpected diagnostic: %v", got)
					}
				}
				r.UpdateStatus("dev", 0, "1.2.4", "Available", true, false)
				assertStatus("1.2.4", "Available", true, false)
				send(&api.UpdateCommandRequest{Key: update, Command: api.UpdateCommand_UPDATE_COMMAND_UPDATE})
				select {
				case install := <-requests:
					if !install {
						t.Fatal("Install command was treated as a check")
					}
				case <-time.After(time.Second):
					t.Fatal("Install command did not reach updater")
				}
				r.UpdateStatus("dev", 0, "", "Downloading", false, true)
				assertStatus("1.2.4", "Downloading", true, true)
				r.UpdateStatus("dev", 0, "1.2.4", "Installing; waiting for Android", true, true)
				assertStatus("1.2.4", "Installing; waiting for Android", true, true)
				r.UpdateStatus("dev", 0, "1.2.4", "Approve on device", true, false)
				assertStatus("1.2.4", "Approve on device", true, false)
				r.UpdateStatus("dev", 0, "1.2.4", "Android installation cancelled or failed", true, false)
				assertStatus("1.2.4", "Android installation cancelled or failed", true, false)
				r.UpdateStatus("dev", 0, "", "Bad feed signature", false, false)
				assertStatus("1.2.3", "Bad feed signature", false, false)

				// Simulate the replacement APK configuring a new version, then
				// have a new ESPHome connection subscribe to the resulting state.
				r.ConfigureUpdates(requests, "1.2.4")
				r.UpdateStatus("dev", 0, "1.2.4", "Installed Encore 1.2.4", true, false)
				send, receive := loopbackPeer(t, l.Addr().String())
				send(&api.SubscribeStatesRequest{})
				for gotStatus, gotVersion := false, false; !gotStatus || !gotVersion; {
					switch got := receive().(type) {
					case *api.TextSensorStateResponse:
						gotStatus = got.Key == diagnostic && got.State == "Installed Encore 1.2.4"
					case *api.UpdateStateResponse:
						gotVersion = got.Key == update && got.CurrentVersion == "1.2.4" && got.LatestVersion == "1.2.4" && !got.MissingState && !got.InProgress
					}
				}
			}
		})
	}
}
