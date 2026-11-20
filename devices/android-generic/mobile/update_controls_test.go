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
			go func() { done <- s.Serve(ctx, l) }()
			t.Cleanup(func() { cancel(); <-done })
			send, receive := loopbackPeer(t, l.Addr().String())
			send(&api.ListEntitiesRequest{})
			var update, check uint32
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
			send(&api.SubscribeStatesRequest{})
			var state *api.UpdateStateResponse
		states:
			for {
				switch v := receive().(type) {
				case *api.UpdateStateResponse:
					state = v
				case *api.TextStateResponse:
					if v.Key == dashboardKey {
						t.Fatal("renderer-less runtime published dashboard state")
					}
				case *api.SwitchStateResponse: // Bluetooth state follows the update snapshot.
					break states
				}
			}
			if (state != nil) != supported || (supported && (state.Key != update || state.CurrentVersion != "1.2.3" || !state.MissingState)) {
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
			}
		})
	}
}
