// An isolated wake-evidence injection host, not a wake detector or device.
package main

import (
	"context"
	"encoding/json"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/farooqu/encore-satellite/backends/assist"
	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/core/session"
	"github.com/farooqu/encore-satellite/core/wake"
	"github.com/farooqu/encore-satellite/internal/lab"
	esphome "github.com/ygelfand/go-esphome-device"
)

func main() {
	ctx := context.Background()
	player := lab.NewSpeaker(ctx)
	engine := session.New(player)
	_ = engine.Select(session.Assist)
	var mu sync.Mutex
	traces := []map[string]any{}
	server := assist.Server{Engine: engine, Trace: func(kind string, data map[string]string) {
		mu.Lock()
		defer mu.Unlock()
		traces = append(traces, map[string]any{"kind": kind, "data": data})
		if len(traces) > 512 {
			traces = traces[len(traces)-512:]
		}
	}}
	// Explicit fixture model identity. The caller supplies the audio and detection;
	// this host does not claim to execute a trained wake model.
	if phrase := os.Getenv("ENCORE_WAKE_PHRASE"); phrase != "" {
		server.ExperimentalWakeWords = map[string]string{"fixture_model": phrase}
	}
	state := os.Getenv("ENCORE_LAB_STATE")
	if state == "" {
		home, _ := os.UserHomeDir()
		state = filepath.Join(home, ".local/state/encore-lab")
	}
	keyBytes, err := os.ReadFile(filepath.Join(state, "noise.psk"))
	if err != nil {
		log.Fatal(err)
	}
	key, err := esphome.ParsePSK(string(keyBytes))
	if err != nil {
		log.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:6056")
	if err != nil {
		log.Fatal(err)
	}
	go func() {
		log.Fatal(server.Serve(ctx, listener, esphome.Info{Name: "encore-wake-lab", FriendlyName: "Encore wake evidence lab", MACAddress: "02:00:00:ec:00:02", Manufacturer: "Encore", Model: "Wake evidence injection fixture"}, key))
	}()
	respond := func(w http.ResponseWriter, value any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(value)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]any{"session": engine.Snapshot(), "playback": player.Snapshot()})
	})
	mux.HandleFunc("GET /trace", func(w http.ResponseWriter, r *http.Request) { mu.Lock(); defer mu.Unlock(); respond(w, traces) })
	var captureMu sync.Mutex
	var sequence uint64
	mux.HandleFunc("POST /command", func(w http.ResponseWriter, r *http.Request) {
		captureMu.Lock()
		defer captureMu.Unlock()
		var c struct {
			Type      string         `json:"type"`
			Value     bool           `json:"value"`
			Samples   []int16        `json:"samples"`
			Detection wake.Detection `json:"detection"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 16384)).Decode(&c); err != nil {
			http.Error(w, "invalid command", 400)
			return
		}
		var err error
		switch c.Type {
		case "capture":
			sequence++
			err = engine.Capture(audio.Frame{Sequence: sequence, CapturedAt: time.Duration(sequence) * time.Millisecond, Samples: c.Samples})
		case "wake":
			err = engine.Wake(c.Detection)
		case "start":
			err = engine.StartManual()
		case "end":
			err = engine.FinishInput()
		case "stop":
			err = engine.Stop()
		case "hold_playback":
			player.Hold(c.Value)
		default:
			http.Error(w, "unknown command", 400)
			return
		}
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		respond(w, engine.Snapshot())
	})
	log.Fatal(http.ListenAndServe("127.0.0.1:8100", mux))
}
