package controls_test

import (
	"errors"
	"testing"

	"github.com/farooqu/encore-satellite/backends/esphome/controls"
	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"
)

func TestBindingsDiscoverAndDispatchOnlyImplementedControls(t *testing.T) {
	volume := controls.SpeakerVolume(80, 2.5)
	writeErr := errors.New("device rejected write")
	var received float32
	b := controls.Bindings{
		controls.SpeakerMute.Bind(nil),
		volume.Bind(func(_ *esphome.Conn, command *api.NumberCommandRequest) error {
			received = command.State
			return writeErr
		}),
	}
	var listed []proto.Message
	if err := b.List(func(m proto.Message) error { listed = append(listed, m); return nil }); err != nil || len(listed) != 1 {
		t.Fatalf("discovery = %v, err = %v", listed, err)
	}
	ent, ok := listed[0].(*api.ListEntitiesNumberResponse)
	if !ok || ent.ObjectId != "speaker_volume" || ent.MaxValue != 80 || ent.Step != 2.5 {
		t.Fatalf("volume discovery = %v", listed[0])
	}
	for _, command := range []proto.Message{
		&api.SwitchCommandRequest{Key: controls.SpeakerMute.Key(), State: true},
		&api.SwitchCommandRequest{Key: ent.Key, State: true},
		&api.NumberCommandRequest{Key: controls.SpeakerMute.Key(), State: 32},
	} {
		if handled, err := b.Handle(nil, command); handled || err != nil || received != 0 {
			t.Fatalf("unexpected command dispatch: %v (%v, %v, %v)", command, handled, err, received)
		}
	}
	if handled, err := b.Handle(nil, &api.NumberCommandRequest{Key: ent.Key, State: 37.5}); !handled || !errors.Is(err, writeErr) || received != 37.5 {
		t.Fatalf("valid command = (%v, %v), device saw %v", handled, err, received)
	}
	want := &api.NumberStateResponse{Key: ent.Key, State: 23.5}
	var sent []proto.Message
	err := b.SendStates(func(m proto.Message) error { sent = append(sent, m); return nil }, []proto.Message{
		&api.SwitchStateResponse{Key: controls.SpeakerMute.Key(), State: true}, want,
	})
	if err != nil || len(sent) != 1 || !proto.Equal(sent[0], want) {
		t.Fatalf("published states = %v, err = %v", sent, err)
	}
}
