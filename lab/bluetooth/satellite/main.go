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
	services, connects            int
	blockedReads                  int
	links                         map[string]bool
	failSave                      bool
	disconnectGate                chan struct{}
	disconnectContext             context.Context
	disconnecting                 bool
	delayDisconnectCancel         bool
	closeGate                     chan struct{}
	closing                       bool
	failClose                     bool
	modeSwitch                    bool
}

func (r *radio) Scan(ctx context.Context, active bool, ready func() error, found func(ble.Advertisement)) error {
	r.Lock()
	r.scans++
	r.Unlock()
	if err := ready(); err != nil {
		return err
	}
	data := []byte{2, 1, 6, 3, 0xff, 0x34, 0x12}
	if r.modeSwitch && active {
		// Model a local name available only in a scan response.
		data = append(data, 4, 0x09, 'L', 'a', 'b')
	}
	tick := time.NewTicker(50 * time.Millisecond)
	defer tick.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-tick.C:
			found(ble.Advertisement{Address: 0xc0ffee123456, AddressType: 1, RSSI: -73, Data: data})
		}
	}
}

type central struct {
	r      *radio
	value  func(string, uint16, []byte)
	data   map[uint16][]byte
	links  map[string]struct{}
	notify bool
}

func (c *central) Connect(ctx context.Context, a string, random bool) (int, error) {
	c.r.Lock()
	c.r.connects++
	c.r.Unlock()
	if a == "AA:BB:CC:DD:EE:FF" || a == "AA:BB:CC:DD:EE:FE" {
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
	c.links[a] = struct{}{}
	c.r.Lock()
	c.r.links[a] = true
	c.r.Unlock()
	return 77, nil
}
func (c *central) Disconnect(ctx context.Context, a string) error {
	if _, ok := c.links[a]; ok {
		c.r.Lock()
		gate := c.r.disconnectGate
		delayCancel := c.r.delayDisconnectCancel
		if gate != nil {
			c.r.disconnecting = true
			if c.r.disconnectContext == nil {
				c.r.disconnectContext = ctx
			}
		}
		c.r.Unlock()
		if gate != nil {
			defer func() {
				c.r.Lock()
				c.r.disconnecting = false
				c.r.Unlock()
			}()
			select {
			case <-gate:
			case <-ctx.Done():
				if delayCancel {
					select {
					case <-gate:
					case <-time.After(5 * time.Second):
					}
				}
				return ctx.Err()
			}
		}
		delete(c.links, a)
		c.r.Lock()
		delete(c.r.links, a)
		c.r.Unlock()
	}
	return nil
}
func (c *central) Services(string) ([]ble.Service, error) {
	c.r.Lock()
	c.r.services++
	c.r.Unlock()
	return []ble.Service{{Handle: 0x10, UUID: "11223344-5566-7788-99aa-bbccddeeff00", Characteristics: []ble.Characteristic{{Handle: 0x15, UUID: "00002a19-0000-1000-8000-00805f9b34fb", Flags: []string{"read", "write", "write-without-response", "notify"}, Descriptors: []ble.Descriptor{
		{Handle: 0x17, UUID: "00002901-0000-1000-8000-00805f9b34fb"},
		{Handle: 0x18, UUID: "00002902-0000-1000-8000-00805f9b34fb"},
	}}}}}, nil
}
func (c *central) Read(ctx context.Context, a string, h uint16) ([]byte, error) {
	if _, ok := c.links[a]; !ok {
		return nil, errors.New("fixture peer is not connected")
	}
	if h == 0x66 {
		return nil, errors.New("fixture read failure")
	}
	if h == 0x67 {
		c.r.Lock()
		c.r.blockedReads++
		c.r.Unlock()
		<-ctx.Done()
		c.r.Lock()
		c.r.blockedReads--
		c.r.Unlock()
		// Model successful I/O racing with operation-context cancellation.
		return []byte("late result"), nil
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
	if h == 0x18 {
		c.notify = len(d) > 0 && d[0]&3 != 0
	}
	if c.notify && h == 0x15 {
		c.value(a, h, d)
	}
	return nil
}
func (c *central) Notify(_ context.Context, _ string, _ uint16, on bool) error {
	c.notify = on
	return nil
}
func (*central) Pair(context.Context, string) error { return nil }
func (c *central) Unpair(ctx context.Context, a string) error {
	return c.Disconnect(ctx, a)
}
func (c *central) Close() error {
	c.r.Lock()
	if gate := c.r.closeGate; gate != nil {
		c.r.closing = true
		c.r.Unlock()
		select {
		case <-gate:
		case <-time.After(5 * time.Second):
		}
		c.r.Lock()
		c.r.closing = false
	}
	c.r.closes++
	if c.r.failClose {
		c.r.Unlock()
		return errors.New("fixture close failure")
	}
	for address := range c.links {
		delete(c.r.links, address)
	}
	c.r.Unlock()
	clear(c.links)
	return nil
}

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	key, err := esphome.ParsePSK(os.Getenv("BLE_TEST_PSK"))
	if err != nil {
		log.Fatal(err)
	}
	state := os.Getenv("BLE_TEST_STATE")
	b, _ := os.ReadFile(state)
	r := &radio{links: make(map[string]bool), modeSwitch: os.Getenv("BLE_TEST_MODE_SWITCH") == "true"}
	p := bluetooth.New(r, bluetooth.Options{Enabled: string(b) == "true", Pairing: true, ModeSwitch: r.modeSwitch, DefaultActive: true, SaveEnabled: func(on bool) error {
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
		return &central{r: r, value: value, data: map[uint16][]byte{}, links: map[string]struct{}{}}, nil
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
	mux.HandleFunc("/gatt-stats", func(w http.ResponseWriter, _ *http.Request) {
		r.Lock()
		defer r.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]int{"services": r.services, "connects": r.connects, "connected": len(r.links), "blocked_reads": r.blockedReads})
	})
	mux.HandleFunc("/links", func(w http.ResponseWriter, _ *http.Request) {
		r.Lock()
		defer r.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"active": r.links, "disconnecting": r.disconnecting, "closing": r.closing,
			"disconnect_cancelled": r.disconnecting && r.disconnectContext != nil && r.disconnectContext.Err() != nil})
	})
	mux.HandleFunc("/pause", func(w http.ResponseWriter, req *http.Request) { p.SetPaused(req.URL.Query().Get("on") == "true") })
	mux.HandleFunc("/fail-save", func(w http.ResponseWriter, req *http.Request) {
		r.Lock()
		r.failSave = req.URL.Query().Get("on") == "true"
		r.Unlock()
	})
	mux.HandleFunc("/fail-close", func(w http.ResponseWriter, req *http.Request) {
		r.Lock()
		r.failClose = req.URL.Query().Get("on") == "true"
		r.Unlock()
	})
	mux.HandleFunc("/hold-disconnect", func(w http.ResponseWriter, req *http.Request) {
		r.Lock()
		defer r.Unlock()
		switch req.URL.Query().Get("on") {
		case "true":
			if r.disconnectGate == nil {
				r.disconnectGate = make(chan struct{})
				r.disconnectContext = nil
			}
			r.delayDisconnectCancel = req.URL.Query().Get("delay-cancel") == "true"
		case "false":
			if r.disconnectGate != nil {
				close(r.disconnectGate)
				r.disconnectGate = nil
			}
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]bool{"waiting": r.disconnectContext != nil, "cancelled": r.disconnectContext != nil && r.disconnectContext.Err() != nil})
	})
	mux.HandleFunc("/hold-close", func(w http.ResponseWriter, req *http.Request) {
		r.Lock()
		defer r.Unlock()
		switch req.URL.Query().Get("on") {
		case "true":
			if r.closeGate == nil {
				r.closeGate = make(chan struct{})
			}
		case "false":
			if r.closeGate != nil {
				close(r.closeGate)
				r.closeGate = nil
			}
		}
	})
	server := &http.Server{Handler: mux, ReadHeaderTimeout: time.Second}
	go func() { _ = server.Serve(control) }()
	_ = json.NewEncoder(os.Stdout).Encode(map[string]string{"api": api.Addr().String(), "control": "http://" + control.Addr().String()})
	<-ctx.Done()
	_ = server.Close()
	_ = api.Close()
	<-serveDone
}
