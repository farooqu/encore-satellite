package sensors_test

import (
	"context"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/backends/esphome/sensors"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"
)

func TestMicrophoneReportsLatestStateAfterBlockedBroadcast(t *testing.T) {
	m := sensors.NewMicrophone(false)
	ctx, cancel := context.WithCancel(t.Context())
	done, unblock := make(chan struct{}), make(chan struct{})
	defer func() { cancel(); <-done }()
	defer close(unblock)
	broadcasts := make(chan bool)
	go func() {
		defer close(done)
		m.Run(ctx, func(msg proto.Message) error {
			select {
			case broadcasts <- msg.(*api.BinarySensorStateResponse).State:
			case <-ctx.Done():
			}
			<-unblock
			return nil
		})
	}()
	receive := func(want bool) {
		t.Helper()
		select {
		case got := <-broadcasts:
			if got != want {
				t.Fatalf("reported mute = %t, want %t", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("no microphone state broadcast")
		}
	}
	m.SetMuted(true)
	receive(true)
	updated := make(chan struct{})
	go func() {
		m.SetMuted(false)
		m.SetMuted(true)
		m.SetMuted(false)
		close(updated)
	}()
	select {
	case <-updated:
	case <-time.After(time.Second):
		t.Fatal("a blocked subscriber stalled mute changes")
	}
	unblock <- struct{}{}
	receive(false)
}
