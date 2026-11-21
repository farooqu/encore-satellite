package sensors_test

import (
	"context"
	"errors"
	"maps"
	"testing"

	"github.com/farooqu/encore-satellite/backends/esphome/sensors"
	"github.com/farooqu/encore-satellite/core/illuminance"
	esphome "github.com/ygelfand/go-esphome-device"
	"google.golang.org/protobuf/proto"
)

type lightSource struct{}

func (lightSource) Read(context.Context) (illuminance.Reading, error) {
	return illuminance.Reading{}, errors.New("no reading")
}

func TestDiscoveryAndStateFollowSuppliedCapabilities(t *testing.T) {
	for _, tc := range []struct {
		name    string
		sources sensors.Sources
		want    []string
	}{
		{name: "no capabilities", sources: sensors.Sources{Illuminance: illuminance.New(nil)}},
		{name: "microphone only", sources: sensors.Sources{MicrophoneMuted: func() bool { return false }}, want: []string{"microphone_muted"}},
		{name: "light only without sample", sources: sensors.Sources{Illuminance: illuminance.New(lightSource{})}, want: []string{"illuminance"}},
		{name: "microphone and light", sources: sensors.Sources{MicrophoneMuted: func() bool { return true }, Illuminance: illuminance.New(lightSource{})}, want: []string{"microphone_muted", "illuminance"}},
		{name: "network unavailable", sources: sensors.Sources{WiFi: func() (string, string, error) { return "", "", errors.New("offline") }}, want: []string{"wifi_ssid", "wifi_change_status"}},
		{name: "lab", sources: sensors.Sources{RunningVoiceMode: func() string { return "Voice Incubator" }}, want: []string{"running_voice_mode"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			want := map[uint32]string{}
			for _, id := range tc.want {
				want[(&esphome.Base{ObjectID: id}).Key()] = id
			}
			listed := map[uint32]string{}
			if err := tc.sources.List(func(message proto.Message) error {
				entity := message.(interface {
					GetObjectId() string
					GetKey() uint32
				})
				if _, duplicate := listed[entity.GetKey()]; duplicate {
					t.Fatalf("duplicate entity: %v", entity)
				}
				listed[entity.GetKey()] = entity.GetObjectId()
				return nil
			}); err != nil || !maps.Equal(listed, want) {
				t.Fatalf("listed %v, want %v (error %v)", listed, want, err)
			}
			for _, message := range tc.sources.States() {
				key := message.(interface{ GetKey() uint32 }).GetKey()
				if _, ok := listed[key]; !ok {
					t.Fatalf("state for unlisted or duplicated key: %v", message)
				}
				delete(listed, key)
			}
			if len(listed) != 0 {
				t.Fatalf("advertised entities without state: %v", listed)
			}
		})
	}
}
