package mobile

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
)

type silentOutput struct{}

func (silentOutput) WriteVoice([]byte, int64) bool { return true }
func (silentOutput) DrainVoice(int64) bool         { return true }
func (silentOutput) FlushVoice(int64)              {}

func TestMicrophoneMuteSensor(t *testing.T) {
	dir := t.TempDir()
	r, err := Open(dir, silentOutput{})
	if err != nil {
		t.Fatal(err)
	}
	if err := r.SetMuted(true); err != nil {
		t.Fatal(err)
	}
	r, err = Open(dir, silentOutput{})
	if err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	server := &esphome.Server{Info: esphome.Info{Name: "mute-test"}, Handler: r, WriteTimeout: time.Second}
	published, served := make(chan struct{}), make(chan error, 1)
	go func() { defer close(published); r.microphone.Run(ctx, server.Broadcast) }()
	go func() { served <- server.Serve(ctx, listener) }()
	t.Cleanup(func() { cancel(); <-published; <-served })

	send, receive := loopbackPeer(t, listener.Addr().String())
	send(&api.ListEntitiesRequest{})
	var sensor *api.ListEntitiesBinarySensorResponse
listing:
	for {
		switch entity := receive().(type) {
		case *api.ListEntitiesBinarySensorResponse:
			if entity.ObjectId == "microphone_muted" {
				sensor = entity
			}
		case *api.ListEntitiesDoneResponse:
			break listing
		}
	}
	if sensor == nil || sensor.Name != "Microphone muted" {
		t.Fatalf("missing read-only microphone mute entity: %v", sensor)
	}
	state := func(want bool) {
		t.Helper()
		for {
			if value, ok := receive().(*api.BinarySensorStateResponse); ok && value.Key == sensor.Key {
				if value.State != want || value.MissingState {
					t.Fatalf("microphone mute = %v, want %v", value, want)
				}
				return
			}
		}
	}
	send(&api.SubscribeStatesRequest{})
	state(true)
	if err := r.SetMuted(false); err != nil {
		t.Fatal(err)
	}
	state(false)
	if err := os.Mkdir(r.path+".tmp", 0700); err != nil {
		t.Fatal(err)
	}
	if err := r.SetMuted(true); err == nil {
		t.Fatal("expected saved settings to fail")
	}
	state(true) // The latch changed despite the failed write.
	if err := os.Remove(r.path + ".tmp"); err != nil {
		t.Fatal(err)
	}
	send(&api.SwitchCommandRequest{Key: sensor.Key, State: false})
	send(&api.SubscribeStatesRequest{})
	state(true) // Only the app can clear the latch.
}
