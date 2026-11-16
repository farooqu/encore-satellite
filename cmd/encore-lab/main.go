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
	"github.com/farooqu/encore-satellite/backends/bluetooth"
	"github.com/farooqu/encore-satellite/backends/incubator"
	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/core/session"
	"github.com/farooqu/encore-satellite/core/wake"
	"github.com/farooqu/encore-satellite/internal/lab"
)

const name = "encore-lab"
const mac = "02:00:00:ec:00:01" // Locally administered virtual identity, not a hardware MAC.

var errRestart = errors.New("apply saved voice mode")

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
	settings, err := loadSettings(filepath.Join(state, "settings.json"))
	if err != nil {
		return err
	}
	// Music belongs to the process, not the replaceable voice runtime. Keep
	// its receiver, stream generation and controls across lab mode Apply.
	var music *lab.Music
	var musicControls *http.ServeMux
	if binary := os.Getenv("ENCORE_SENDSPIN_LAB_CLIENT"); binary != "" {
		music = lab.NewMusic()
		music.SetPrivacy(settings.snapshot().Muted)
		musicControls = http.NewServeMux()
		port := os.Getenv("PORT")
		if port == "" {
			port = "8099"
		}
		if err := lab.ServeMusic(ctx, musicControls, music, binary, state, port); err != nil {
			return err
		}
	}
	for ctx.Err() == nil {
		if err := serve(ctx, key, settings, music, musicControls); !errors.Is(err, errRestart) {
			return err
		}
	}
	return nil
}

// serve owns one complete runtime. Apply tears it down before constructing its
// replacement; ordinary setting changes and microphone mute do not restart BLE.
func serve(parent context.Context, key esphome.PSK, settings *settings, music *lab.Music, musicControls *http.ServeMux) error {
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	started := time.Now().UTC()
	initial := settings.snapshot()
	player := lab.NewSpeaker(ctx)
	engine := session.New(player)
	if music != nil {
		player.AttachMusic(music)
		engine.SetActivityHandler(music.SetVoiceActive)
	}
	if err := engine.Select(initial.VoiceMode); err != nil {
		return err
	}
	engine.SetMuted(initial.Muted)
	restart := make(chan struct{}, 1)
	requestRestart := func() {
		if settings.snapshot().VoiceMode != initial.VoiceMode {
			select {
			case restart <- struct{}{}:
			default:
			}
		}
	}
	radio := &lab.VirtualRadio{}
	proxy := bluetooth.New(radio, bluetooth.Options{Enabled: initial.BluetoothProxy, SaveEnabled: func(on bool) error {
		return settings.update(func(c *configuration) { c.BluetoothProxy = on })
	}})
	control := &voiceControls{RunningMode: initial.VoiceMode,
		Mode: func() session.Backend { return settings.snapshot().VoiceMode },
		SaveMode: func(mode session.Backend) error {
			return settings.update(func(c *configuration) { c.VoiceMode = mode })
		}, Restart: requestRestart, Bluetooth: proxy}
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
	var workers sync.WaitGroup
	// Wait for listeners, connections and the radio worker to finish before
	// a replacement runtime can reuse their ports or controller.
	defer workers.Wait()
	defer cancel()
	assistListener, err := net.Listen("tcp", ":6053")
	if err != nil {
		return err
	}
	assistListener = newManagementListener(ctx, assistListener)
	defer assistListener.Close()
	advert, err := mdns.Advertise(mdns.Config{Name: name, FriendlyName: "Encore virtual satellite", Port: 6053, MACAddress: mac, Version: "2026.10.1", Platform: "encore", Board: "virtual", Encrypted: true})
	if err != nil {
		return err
	}
	defer advert.Close()
	info := esphome.Info{Name: name, FriendlyName: "Encore virtual satellite", MACAddress: mac, Manufacturer: "Encore", Model: "Virtual protocol fixture", Version: "0.1.0-dev.0", ProjectName: "encore.satellite", ESPHomeVersion: "2026.10.1"}
	workers.Add(1)
	go func() {
		defer workers.Done()
		if initial.VoiceMode == session.Assist {
			s := assist.Server{Engine: engine, Trace: trace, Controls: control}
			errCh <- s.Serve(ctx, assistListener, info, key)
		} else {
			errCh <- control.Serve(ctx, assistListener, info, key)
		}
	}()
	var backendError string
	if initial.VoiceMode == session.Incubator {
		incubatorListener, err := net.Listen("tcp", "127.0.0.1:6055")
		if err != nil {
			// Voice startup failure must not remove BLE or the way back to
			// Assist. Keep management alive without silently falling back.
			backendError = err.Error()
			trace("backend_unavailable", map[string]string{"error": backendError})
		} else {
			defer incubatorListener.Close()
			workers.Add(1)
			go func() {
				defer workers.Done()
				s := incubator.Server{Engine: engine, Name: name, MAC: mac, Version: "0.1.0-dev.0"}
				errCh <- s.Serve(ctx, incubatorListener)
			}()
		}
	}

	// Admission, mutations and retirement share one fence. Never hold it
	// during body reads: an incomplete command must not prevent Apply.
	var commandMu sync.Mutex
	var retiring bool
	var handlers sync.WaitGroup
	var sequence uint64
	var capturedSamples int64
	mux := http.NewServeMux()
	port := os.Getenv("PORT")
	if port == "" {
		port = "8099"
	}
	if musicControls != nil {
		mux.Handle("/", musicControls)
	}
	respond := func(w http.ResponseWriter, data any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(data)
	}
	mux.HandleFunc("GET /state", func(w http.ResponseWriter, r *http.Request) {
		configured := settings.snapshot()
		respond(w, map[string]any{"session": engine.Snapshot(), "playback": player.Snapshot(), "started_at": started,
			"mode": map[string]any{"configured": configured.VoiceMode, "running": initial.VoiceMode, "restart_required": configured.VoiceMode != initial.VoiceMode, "backend_error": backendError},
			"ble":  map[string]any{"enabled": configured.BluetoothProxy, "starts": radio.Starts.Load(), "stops": radio.Stops.Load()}})
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
		commandMu.Lock()
		// Decode can finish after Apply or a client cancellation. No old
		// command may touch the retired engine or host-owned settings.
		if retiring || ctx.Err() != nil || r.Context().Err() != nil {
			commandMu.Unlock()
			http.Error(w, "runtime is shutting down", http.StatusServiceUnavailable)
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
			if music != nil {
				music.Stop()
			}
			err = engine.Stop()
		case "mute":
			if c.Value {
				if music != nil {
					music.SetPrivacy(true)
				}
				engine.SetMuted(true) // Privacy does not wait for disk I/O.
			}
			err = settings.setMuted(c.Value)
			if err == nil && !c.Value {
				if music != nil {
					music.SetPrivacy(false)
				}
				engine.SetMuted(false)
			}
		case "mode":
			err = control.SaveMode(c.Backend)
		case "restart":
			requestRestart()
		case "hold_playback":
			player.Hold(c.Value)
		case "capture":
			err = engine.Capture(audio.Frame{Samples: c.Samples, Sequence: sequence, CapturedAt: time.Duration(capturedSamples) * time.Second / audio.SampleRate})
			sequence++
			capturedSamples += int64(len(c.Samples))
		default:
			err = fmt.Errorf("unknown fixture command %q", c.Type)
		}
		snapshot := engine.Snapshot()
		commandMu.Unlock() // A stalled response writer must not prevent retirement.
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		respond(w, snapshot)
	})
	httpServer := &http.Server{Addr: "127.0.0.1:" + port, ReadHeaderTimeout: 5 * time.Second,
		BaseContext: func(net.Listener) context.Context { return ctx },
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			commandMu.Lock()
			if retiring || ctx.Err() != nil {
				commandMu.Unlock()
				w.Header().Set("Connection", "close")
				http.Error(w, "runtime is shutting down", http.StatusServiceUnavailable)
				return
			}
			handlers.Add(1) // Retirement fences Add before Wait, even at zero.
			commandMu.Unlock()
			defer handlers.Done()
			mux.ServeHTTP(w, r)
		})}
	workers.Add(1)
	go func() { defer workers.Done(); errCh <- httpServer.ListenAndServe() }()
	log.Printf("Virtual satellite ready; mode=%s; controls loopback-only", initial.VoiceMode)
	select {
	case <-ctx.Done():
	case <-restart:
		err = errRestart
	case err = <-errCh:
	}
	commandMu.Lock()
	retiring = true
	engine.SetMuted(true) // Cancel voice before withdrawing the old endpoint.
	cancel()
	commandMu.Unlock()
	shutdown, stop := context.WithTimeout(context.Background(), time.Second)
	defer stop()
	if err := httpServer.Shutdown(shutdown); err != nil {
		// Shutdown does not close active connections on timeout. Unblock
		// body reads/writes, then join handlers (Close itself does not).
		_ = httpServer.Close()
	}
	handlers.Wait()
	return err
}
