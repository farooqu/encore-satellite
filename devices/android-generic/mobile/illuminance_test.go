package mobile

import (
	"bufio"
	"context"
	"encoding/binary"
	"io"
	"net"
	"testing"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"
)

type nativeLight string

func (h nativeLight) Sample() string { return string(h) }

// Exercise the real composition's catalog and state subscription over TCP.
// Only this loopback test harness allows plaintext; Runtime.Start requires Noise.
func TestLightProtocolCapabilityAndAvailability(t *testing.T) {
	for _, tc := range []struct {
		name    string
		host    LightHost
		missing bool
	}{
		{"absent", nil, true},
		{"supported", nativeLight(`{"lux":213.5,"age_ms":123}`), false},
		{"registration failure", nativeLight(`{"problem":"Light sensor registration failed"}`), true},
		{"permission failure", nativeLight(`{"problem":"Light sensor permission denied"}`), true},
		{"stale", nativeLight(`{"lux":213.5,"age_ms":30001}`), true},
		{"future", nativeLight(`{"lux":213.5,"age_ms":-1}`), true},
		{"invalid", nativeLight(`{"lux":-1,"age_ms":0}`), true},
		{"missing fields", nativeLight(`{}`), true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r, err := Open(t.TempDir(), silentOutput{})
			if err != nil {
				t.Fatal(err)
			}
			defer r.Close()
			if err := r.SetLightHost(tc.host); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.host != nil {
				ready := make(chan struct{}, 2)
				done := make(chan struct{})
				go func() { defer close(done); r.light.Run(ctx, func() { ready <- struct{}{} }) }()
				<-ready
				defer func() { cancel(); <-done }()
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			server := &esphome.Server{Info: esphome.Info{Name: "light-test"}, Handler: r, WriteTimeout: time.Second}
			done := make(chan error, 1)
			go func() { done <- server.Serve(ctx, listener) }()
			defer func() { cancel(); <-done }()
			send, receive := loopbackPeer(t, listener.Addr().String())
			send(&api.ListEntitiesRequest{})
			var entity *api.ListEntitiesSensorResponse
		listing:
			for {
				switch m := receive().(type) {
				case *api.ListEntitiesDoneResponse:
					break listing
				case *api.ListEntitiesSensorResponse:
					if entity != nil {
						t.Fatal("duplicate sensor")
					}
					entity = m
				}
			}
			if (entity != nil) != (tc.host != nil) {
				t.Fatalf("hardware catalog mismatch: %v", entity)
			}
			if entity != nil && (entity.ObjectId != "illuminance" || entity.UnitOfMeasurement != "lx" || entity.DeviceClass != "illuminance" || entity.StateClass != api.SensorStateClass_STATE_CLASS_MEASUREMENT) {
				t.Fatalf("wrong unit contract: %v", entity)
			}
			send(&api.SubscribeStatesRequest{})
			var state *api.SensorStateResponse
		states:
			for {
				switch m := receive().(type) {
				case *api.SensorStateResponse:
					state = m
				case *api.SwitchStateResponse: // The Bluetooth switch follows the sensor snapshots.
					break states
				}
			}
			if tc.host != nil {
				if state == nil || state.Key != entity.Key || state.MissingState != tc.missing {
					t.Fatalf("availability: %v", state)
				}
				if !tc.missing && state.State != 213.5 {
					t.Fatalf("wrong lux %v", state)
				}
				if tc.missing && state.State != 0 {
					t.Fatal("leaked stale numeric value")
				}
			} else if state != nil {
				t.Fatalf("absent hardware has state: %v", state)
			}
		})
	}
}

func loopbackPeer(t *testing.T, address string) (send func(proto.Message), receive func() proto.Message) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	if err := conn.SetDeadline(time.Now().Add(3 * time.Second)); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	send = func(message proto.Message) {
		t.Helper()
		b, err := proto.Marshal(message)
		if err != nil {
			t.Fatal(err)
		}
		info, _ := esphome.Lookup(message)
		frame := binary.AppendUvarint([]byte{0}, uint64(len(b)))
		frame = binary.AppendUvarint(frame, uint64(info.ID))
		if _, err := conn.Write(append(frame, b...)); err != nil {
			t.Fatal(err)
		}
	}
	receive = func() proto.Message {
		t.Helper()
		marker, err := reader.ReadByte()
		if err != nil || marker != 0 {
			t.Fatalf("frame: %d %v", marker, err)
		}
		n, err := binary.ReadUvarint(reader)
		if err != nil {
			t.Fatal(err)
		}
		id, err := binary.ReadUvarint(reader)
		if err != nil {
			t.Fatal(err)
		}
		b := make([]byte, n)
		if _, err := io.ReadFull(reader, b); err != nil {
			t.Fatal(err)
		}
		message, ok := esphome.NewMessage(uint32(id))
		if !ok {
			t.Fatalf("unknown API id %d", id)
		}
		if err := proto.Unmarshal(b, message); err != nil {
			t.Fatal(err)
		}
		return message
	}
	send(&api.HelloRequest{ClientInfo: "loopback test", ApiVersionMajor: 1, ApiVersionMinor: 10})
	if _, ok := receive().(*api.HelloResponse); !ok {
		t.Fatal("no hello")
	}
	return send, receive
}
