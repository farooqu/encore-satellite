package sensors_test

import (
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/backends/esphome/sensors"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"
)

type microphonePeer struct {
	writes  chan bool
	unblock <-chan struct{}
}

func (p *microphonePeer) Send(msg proto.Message) error {
	p.writes <- msg.(*api.BinarySensorStateResponse).State
	if p.unblock != nil {
		<-p.unblock
	}
	return nil
}

func (*microphonePeer) Close() error { return nil }

func TestMicrophoneIsolatesSlowSubscriberAndCoalescesUpdates(t *testing.T) {
	m := sensors.NewMicrophone(false)
	unblock := make(chan struct{})
	slow := &microphonePeer{writes: make(chan bool, 8), unblock: unblock}
	fast := &microphonePeer{writes: make(chan bool, 8)}
	defer m.Unsubscribe(slow)
	defer m.Unsubscribe(fast)
	defer close(unblock)
	receive := func(peer *microphonePeer, want bool) {
		t.Helper()
		select {
		case got := <-peer.writes:
			if got != want {
				t.Fatalf("reported mute = %t, want %t", got, want)
			}
		case <-time.After(time.Second):
			t.Fatal("no microphone state received")
		}
	}
	m.Subscribe(slow)
	receive(slow, false)
	go func() {
		m.SetMuted(true)
		m.Subscribe(fast)
	}()
	receive(fast, true) // Initial snapshot while the slow write is in flight.
	go m.SetMuted(false)
	receive(fast, false) // Live update, still without releasing the slow writer.
	unblock <- struct{}{}
	receive(slow, false) // The pending true was replaced by the latest state.
}
