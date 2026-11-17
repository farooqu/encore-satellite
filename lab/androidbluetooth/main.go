// Android gomobile composition with a simulated native scanner. The runtime
// uses its normal 6053/6055 listeners; only fixture controls bind loopback.
// No Android device, physical RF, production credentials or radio is used.
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"sync"

	"github.com/farooqu/encore-satellite/devices/android-generic/mobile"
)

type audio struct{}

func (audio) WriteVoice([]byte, int64) bool { return true }
func (audio) DrainVoice(int64) bool         { return true }
func (audio) FlushVoice(int64)              {}

type native struct {
	sync.Mutex
	token         int64
	scanning      bool
	starts, stops int
}

func (n *native) StartScan(token int64) error {
	n.Lock()
	defer n.Unlock()
	n.token, n.scanning = token, true
	n.starts++
	return nil
}
func (n *native) StopScan(token int64) error {
	n.Lock()
	defer n.Unlock()
	if n.token == token {
		n.scanning = false
		n.stops++
	}
	return nil
}

func main() {
	sdk := flag.Int("sdk", 34, "simulated native API level")
	hardware := flag.Bool("hardware", true, "simulated BLE support")
	flag.Parse()
	directory, err := os.MkdirTemp("", "encore-android-ble-")
	if err != nil {
		log.Fatal(err)
	}
	defer os.RemoveAll(directory)
	r, err := mobile.Open(directory, audio{})
	if err != nil {
		log.Fatal(err)
	}
	defer r.Close()
	n := &native{}
	if err := r.ConfigureBluetooth(n, *sdk, *hardware); err != nil {
		log.Fatal(err)
	}
	if err := r.Start(); err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		log.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/state", func(w http.ResponseWriter, req *http.Request) {
		n.Lock()
		defer n.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"scanning": n.scanning, "starts": n.starts, "stops": n.stops, "token": n.token, "enabled": r.BluetoothEnabled()})
	})
	mux.HandleFunc("/pause", func(w http.ResponseWriter, req *http.Request) {
		r.PauseBluetooth(req.URL.Query().Get("value") != "false")
	})
	mux.HandleFunc("/enable", func(w http.ResponseWriter, req *http.Request) {
		if err := r.SetBluetoothEnabled(req.URL.Query().Get("value") == "true"); err != nil {
			http.Error(w, err.Error(), 500)
		}
	})
	mux.HandleFunc("/report", func(w http.ResponseWriter, req *http.Request) {
		n.Lock()
		token := n.token
		n.Unlock()
		typ := 255
		if *sdk >= 35 {
			typ = 1
		}
		if req.URL.Query().Get("stale") == "true" {
			token--
		}
		r.BluetoothAdvertisement(token, "C0:FE:19:82:E3:45", typ, -73, []byte{2, 1, 6, 4, 9, 'A', '7', 'L', 5, 0xff, 0x34, 0x12, 0x82, 0xe3})
	})
	server := &http.Server{Handler: mux}
	go server.Serve(listener)
	defer server.Close()
	fmt.Printf("{\"port\":6053,\"key\":%q,\"control\":%q}\n", r.NoiseKey(), "http://"+listener.Addr().String())
	bufio.NewScanner(os.Stdin).Scan()
}
