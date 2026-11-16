package lab

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"time"
)

// ServeMusic installs opt-in loopback lab controls and a private persistent
// switch. The supervised Go host owns its C++ child's entire lifetime. No
// Sendspin listener or connection exists while the switch is off.
func ServeMusic(ctx context.Context, mux *http.ServeMux, music *Music, binaryPath, state, port string) error {
	var mu sync.Mutex
	var cancel context.CancelFunc
	setting := filepath.Join(state, "sendspin-enabled.json")
	enable := func(enabled bool) error {
		mu.Lock()
		defer mu.Unlock()
		if music.Snapshot().Enabled == enabled {
			return nil
		}
		// Invalidate the sink before cancelling the old decoder process.
		generation := music.Enable(enabled)
		if cancel != nil {
			cancel()
			cancel = nil
		}
		if enabled {
			logFile, err := os.OpenFile(filepath.Join(state, "sendspin-client.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
			if err != nil {
				music.Enable(false)
				return err
			}
			child, stop := context.WithCancel(ctx)
			cmd := exec.CommandContext(child, binaryPath, filepath.Join(state, "sendspin-keys.json"), port, strconv.FormatUint(generation, 10))
			cmd.Stdout, cmd.Stderr = logFile, logFile
			if err := cmd.Start(); err != nil {
				stop()
				logFile.Close()
				music.Enable(false)
				return err
			}
			cancel = stop
			go func() {
				_ = cmd.Wait()
				_ = logFile.Close()
				mu.Lock()
				defer mu.Unlock()
				if music.Snapshot().Generation == generation {
					music.Enable(false)
				}
			}()
		}
		return os.WriteFile(setting, []byte(strconv.FormatBool(enabled)), 0600)
	}
	if data, err := os.ReadFile(setting); err == nil {
		var enabled bool
		if err := json.Unmarshal(data, &enabled); err != nil {
			return err
		}
		if err := enable(enabled); err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	respond := func(w http.ResponseWriter, data any) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(data)
	}
	mux.HandleFunc("GET /music/state", func(w http.ResponseWriter, r *http.Request) { respond(w, music.Snapshot()) })
	mux.HandleFunc("GET /music/blocks", func(w http.ResponseWriter, r *http.Request) { respond(w, music.Blocks()) })
	mux.HandleFunc("GET /music.wav", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "audio/wav")
		_, _ = w.Write(music.WAV())
	})
	mux.HandleFunc("POST /music/record", func(w http.ResponseWriter, r *http.Request) { music.Record(); respond(w, music.Snapshot()) })
	mux.HandleFunc("POST /music/play", func(w http.ResponseWriter, r *http.Request) { music.Play(); respond(w, music.Snapshot()) })
	mux.HandleFunc("POST /music/stop", func(w http.ResponseWriter, r *http.Request) { music.Stop(); respond(w, music.Snapshot()) })
	mux.HandleFunc("POST /music/enable", func(w http.ResponseWriter, r *http.Request) {
		var c struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128)).Decode(&c); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := enable(c.Enabled); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		respond(w, music.Snapshot())
	})
	identity := func(r *http.Request) (uint64, uint64, error) {
		generation, err := strconv.ParseUint(r.Header.Get("X-Encore-Generation"), 10, 64)
		if err != nil {
			return 0, 0, err
		}
		stream, err := strconv.ParseUint(r.Header.Get("X-Encore-Stream"), 10, 64)
		return generation, stream, err
	}
	mux.HandleFunc("POST /music/stream", func(w http.ResponseWriter, r *http.Request) {
		generation, stream, err := identity(r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16))
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if string(body) == "start" {
			err = music.BeginStream(generation, stream)
		} else if string(body) == "end" {
			music.EndStream(generation, stream)
		} else {
			err = fmt.Errorf("invalid stream action")
		}
		if err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		respond(w, music.Snapshot())
	})
	mux.HandleFunc("POST /music/volume", func(w http.ResponseWriter, r *http.Request) {
		generation, _, err := identity(r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		var c struct {
			Volume float64 `json:"volume"`
			Muted  bool    `json:"muted"`
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 128)).Decode(&c); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if err := music.SetVolume(generation, c.Volume, c.Muted); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		respond(w, music.Snapshot())
	})
	mux.HandleFunc("POST /music/pcm", func(w http.ResponseWriter, r *http.Request) {
		generation, stream, err := identity(r)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1920))
		if err != nil || len(data)%4 != 0 {
			http.Error(w, "expected stereo PCM16", 400)
			return
		}
		pcm := make([]int16, len(data)/2)
		for i := range pcm {
			pcm[i] = int16(binary.LittleEndian.Uint16(data[i*2:]))
		}
		writeCtx, stop := context.WithTimeout(r.Context(), time.Second)
		defer stop()
		if err := music.Write(writeCtx, generation, stream, pcm); err != nil {
			http.Error(w, err.Error(), 409)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
	return nil
}
