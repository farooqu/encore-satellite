// encore-lab is a loopback-only virtual satellite, not a device daemon.
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
	"path/filepath"
	"sync"
	"syscall"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/mdns"

	"github.com/farooqu/encore-satellite/backends/assist"
	"github.com/farooqu/encore-satellite/backends/incubator"
	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/core/session"
	"github.com/farooqu/encore-satellite/core/wake"
	"github.com/farooqu/encore-satellite/internal/lab"
)

const name = "encore-lab"
const mac = "02:00:00:ec:00:01" // Locally administered virtual identity, not a hardware MAC.

func main() {
	if err := run(); err != nil {
		log.Fatal(err)
	}
}

func run() error {
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()
	state := os.Getenv("ENCORE_LAB_STATE")
	if state == "" {
		home, err := os.UserHomeDir()
		if err != nil {
			return err
		}
		state = filepath.Join(home, ".local/state/encore-lab")
	}
	if err := os.MkdirAll(state, 0700); err != nil {
		return err
	}
	keyPath := filepath.Join(state, "noise.psk")
	keyData, err := os.ReadFile(keyPath)
	if errors.Is(err, os.ErrNotExist) {
		key, keyErr := esphome.GeneratePSK()
		if keyErr != nil {
			return keyErr
		}
		keyData = []byte(key.String())
		if err = os.WriteFile(keyPath, keyData, 0600); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	key, err := esphome.ParsePSK(string(keyData))
	if err != nil {
		return err
	}
	player := lab.NewSpeaker(ctx)
	engine := session.New(player)
	if err := engine.Select(session.Assist); err != nil {
		return err
	}
	var traceMu sync.Mutex
	traces := make([]map[string]any, 0)
	trace := func(kind string, data map[string]string) {
		traceMu.Lock()
		defer traceMu.Unlock()
		traces = append(traces, map[string]any{"time": time.Now().UTC(), "kind": kind, "data": data})
		if len(traces) > 512 {
			traces = traces[len(traces)-512:]
		}
	}
	errCh := make(chan error, 3)
	assistListener, err := net.Listen("tcp", ":6053")
	if err != nil {
		return err
	}
	defer assistListener.Close()
	incubatorListener, err := net.Listen("tcp", "127.0.0.1:6055")
	if err != nil {
		return err
	}
	defer incubatorListener.Close()
	advert, err := mdns.Advertise(mdns.Config{Name: name, FriendlyName: "Encore virtual satellite", Port: 6053, MACAddress: mac, Version: "2026.10.1", Platform: "encore", Board: "virtual", Encrypted: true})
	if err != nil {
		return err
	}
	defer advert.Close()
	go func() {
		s := assist.Server{Engine: engine, Trace: trace}
		errCh <- s.Serve(ctx, assistListener, esphome.Info{Name: name, FriendlyName: "Encore virtual satellite", MACAddress: mac, Manufacturer: "Encore", Model: "Virtual protocol fixture", Version: "0.1.0-dev.0", ProjectName: "encore.satellite"}, key)
	}()
	go func() {
		s := incubator.Server{Engine: engine, Name: name, MAC: mac, Version: "0.1.0-dev.0"}
		errCh <- s.Serve(ctx, incubatorListener)
	}()

	var captureMu sync.Mutex
	var sequence uint64
	var capturedSamples int64
	mux := http.NewServeMux()
	respond := func(w http.ResponseWriter, data any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(data)
	}
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]any{"session": engine.Snapshot(), "playback": player.Snapshot()})
	})
	mux.HandleFunc("GET /trace", func(w http.ResponseWriter, r *http.Request) {
		traceMu.Lock()
		defer traceMu.Unlock()
		respond(w, traces)
	})
	mux.HandleFunc("POST /command", func(w http.ResponseWriter, r *http.Request) {
		var c struct {
			Type      string          `json:"type"`
			Value     bool            `json:"value"`
			Backend   session.Backend `json:"backend"`
			Samples   []int16         `json:"samples"`
			Detection wake.Detection  `json:"detection"`
		}
		decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&c); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var err error
		switch c.Type {
		case "start":
			err = engine.StartManual()
		case "wake":
			err = engine.Wake(c.Detection)
		case "end":
			err = engine.FinishInput()
		case "stop":
			err = engine.Stop()
		case "mute":
			engine.SetMuted(c.Value)
		case "backend":
			err = engine.Select(c.Backend)
		case "hold_playback":
			player.Hold(c.Value)
		case "capture":
			captureMu.Lock()
			err = engine.Capture(audio.Frame{Samples: c.Samples, Sequence: sequence, CapturedAt: time.Duration(capturedSamples) * time.Second / audio.SampleRate})
			sequence++
			capturedSamples += int64(len(c.Samples))
			captureMu.Unlock()
		default:
			err = fmt.Errorf("unknown fixture command %q", c.Type)
		}
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		respond(w, engine.Snapshot())
	})
	port := os.Getenv("PORT")
	if port == "" {
		port = "8099"
	}
	httpServer := &http.Server{Addr: "127.0.0.1:" + port, Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	go func() { errCh <- httpServer.ListenAndServe() }()
	log.Printf("Virtual satellite ready; controls loopback-only; Noise key at %s (not logged)", keyPath)
	select {
	case <-ctx.Done():
	case err = <-errCh:
	}
	cancel()
	shutdown, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	_ = httpServer.Shutdown(shutdown)
	return err
}
