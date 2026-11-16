// Virtual radio fixture for the pinned HA client's encrypted protocol test.
// No hardware, RF, BlueZ or pairing security is simulated by this fixture.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/farooqu/encore-satellite/backends/bluetooth"
	"github.com/farooqu/encore-satellite/core/ble"
	esphome "github.com/ygelfand/go-esphome-device"
)

type radio struct {
	sync.Mutex
	scans, opens, closes, blocked int
	failSave                      bool
}

func (r *radio) Scan(ctx context.Context, active bool, ready func() error, found func(ble.Advertisement)) error {
	r.Lock()
	r.scans++
	r.Unlock()
	if err := ready(); err != nil {
		return err
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			found(ble.Advertisement{Address: 0xc0ffee123456, AddressType: 1, RSSI: -73, Data: []byte{2, 1, 6, 3, 0xff, 0x34, 0x12}})
		}
	}
}

type central struct {
	r      *radio
	value  func(string, uint16, []byte)
	data   map[uint16][]byte
	notify bool
}

func (c *central) Connect(ctx context.Context, a string, random bool) (int, error) {
	if a == "AA:BB:CC:DD:EE:FF" {
		c.r.Lock()
		c.r.blocked++
		c.r.Unlock()
		<-ctx.Done()
		c.r.Lock()
		c.r.blocked--
		c.r.Unlock()
		return 0, ctx.Err()
	}
	if !random {
		return 0, errors.New("fixture expects random address")
	}
	return 77, nil
}
func (*central) Disconnect(context.Context, string) error { return nil }
func (*central) Services(string) ([]ble.Service, error) {
	return []ble.Service{{Handle: 0x10, UUID: "11223344-5566-7788-99aa-bbccddeeff00", Characteristics: []ble.Characteristic{{Handle: 0x15, UUID: "00002a19-0000-1000-8000-00805f9b34fb", Flags: []string{"read", "write", "notify"}, Descriptors: []ble.Descriptor{{Handle: 0x17, UUID: "00002901-0000-1000-8000-00805f9b34fb"}}}}}}, nil
}
func (c *central) Read(_ context.Context, _ string, h uint16) ([]byte, error) {
	if h == 0x66 {
		return nil, errors.New("fixture read failure")
	}
	if d, ok := c.data[h]; ok {
		return d, nil
	}
	if h == 0x17 {
		return []byte("descriptor"), nil
	}
	return []byte{0x19, 0x82, 0xe3}, nil
}
func (c *central) Write(_ context.Context, a string, h uint16, d []byte, _ bool) error {
	c.data[h] = append([]byte(nil), d...)
	if c.notify {
		c.value(a, h, d)
	}
	return nil
}
func (c *central) Notify(_ context.Context, _ string, _ uint16, on bool) error {
	c.notify = on
	return nil
}
func (*central) Pair(context.Context, string) error   { return nil }
func (*central) Unpair(context.Context, string) error { return nil }
func (c *central) Close() error                       { c.r.Lock(); c.r.closes++; c.r.Unlock(); return nil }

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	key, err := esphome.ParsePSK(os.Getenv("BLE_TEST_PSK"))
	if err != nil {
		log.Fatal(err)
	}
	state := os.Getenv("BLE_TEST_STATE")
	b, _ := os.ReadFile(state)
	r := &radio{}
	p := bluetooth.New(r, bluetooth.Options{Enabled: string(b) == "true", Pairing: true, SaveEnabled: func(on bool) error {
		r.Lock()
		fail := r.failSave
		r.Unlock()
		if fail {
			return errors.New("fixture save failure")
		}
		if err := os.WriteFile(state+".new", []byte(fmt.Sprint(on)), 0600); err != nil {
			return err
		}
		return os.Rename(state+".new", state)
	}, OpenCentral: func(value func(string, uint16, []byte), _ func(string)) (ble.Central, error) {
		r.Lock()
		r.opens++
		r.Unlock()
		return &central{r: r, value: value, data: map[uint16][]byte{}}, nil
	}})
	api, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	control, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	serveDone := make(chan struct{})
	go func() {
		defer close(serveDone)
		_ = p.Serve(ctx, api, esphome.Info{Name: "encore-ble-fixture", MACAddress: "02:00:00:EC:00:03", Model: "Virtual BLE protocol fixture"}, key)
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("/state", func(w http.ResponseWriter, _ *http.Request) {
		r.Lock()
		defer r.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"scans": r.scans, "opens": r.opens, "closes": r.closes, "blocked": r.blocked})
	})
	mux.HandleFunc("/pause", func(w http.ResponseWriter, req *http.Request) { p.SetPaused(req.URL.Query().Get("on") == "true") })
	mux.HandleFunc("/fail-save", func(w http.ResponseWriter, req *http.Request) {
		r.Lock()
		r.failSave = req.URL.Query().Get("on") == "true"
		r.Unlock()
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(control) }()
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"api": api.Addr().String(), "control": "http://" + control.Addr().String()})
	<-ctx.Done()
	_ = server.Close()
	_ = api.Close()
	<-serveDone
}
