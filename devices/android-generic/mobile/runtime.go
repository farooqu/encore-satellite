// Package mobile is the Incubator-only APK composition root, bound with gomobile.
// It deliberately imports no Assist backend or runtime backend selector.
package mobile

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/farooqu/encore-satellite/backends/bluetooth"
	lightapi "github.com/farooqu/encore-satellite/backends/illuminance"
	"github.com/farooqu/encore-satellite/backends/incubator"
	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/core/illuminance"
	"github.com/farooqu/encore-satellite/core/session"
	androidhost "github.com/farooqu/encore-satellite/platforms/android"
	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"
)

// Audio is the native in-process JNI boundary. No dashboard JS bridge exists.
type Audio interface {
	WriteVoice(pcm []byte, epoch int64) bool
	DrainVoice(epoch int64) bool
	FlushVoice(epoch int64)
}

type settings struct {
	MAC               string `json:"mac"`
	Key               string `json:"key"`
	Dashboard         string `json:"dashboard"`
	Muted             bool   `json:"muted"`
	Channel           string `json:"channel"`
	ChannelGeneration int64  `json:"channel_generation"`
	BluetoothProxy    bool   `json:"bluetooth_proxy"`
}

type Runtime struct {
	mu                  sync.Mutex
	path                string
	config              settings
	engine              *session.Engine
	voice               *incubator.Server
	control             *esphome.Server
	cancel              context.CancelFunc
	done                sync.WaitGroup
	sequence            uint64
	samples             int64
	captureMu           sync.Mutex
	reload              int
	problem             string
	updater             UpdateHost
	updateState         *api.UpdateStateResponse
	proxy               *bluetooth.Proxy
	radio               *androidhost.BluetoothScanner
	bluetoothConfigured bool
	light               *illuminance.Monitor
}

func Open(directory string, output Audio) (*Runtime, error) {
	if output == nil {
		return nil, errors.New("native audio required")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	r := &Runtime{path: filepath.Join(directory, "encore.json")}
	b, err := os.ReadFile(r.path)
	if err == nil {
		if err = json.Unmarshal(b, &r.config); err != nil {
			return nil, err
		}
	} else if errors.Is(err, os.ErrNotExist) {
		key, err := esphome.GeneratePSK()
		if err != nil {
			return nil, err
		}
		var id [6]byte
		if _, err = rand.Read(id[:]); err != nil {
			return nil, err
		}
		id[0] = (id[0] | 2) & 0xfe
		r.config.Key = key.String()
		r.config.MAC = net.HardwareAddr(id[:]).String()
		if err = r.save(r.config); err != nil {
			return nil, err
		}
	} else {
		return nil, err
	}
	if r.config.Channel == "" {
		r.config.Channel = "dev"
	}
	if r.config.Channel != "dev" && r.config.Channel != "stable" {
		return nil, errors.New("invalid saved update channel")
	}
	r.engine = session.New(androidhost.NewPlayback(output))
	r.engine.SetMuted(r.config.Muted)
	r.voice = &incubator.Server{Engine: r.engine, Name: r.Name(), MAC: r.config.MAC, Version: "0.1.0-android", EnableModels: true}
	r.proxy = r.newBluetooth(nil)
	return r, nil
}

func (r *Runtime) save(c settings) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if err = os.WriteFile(r.path+".tmp", b, 0600); err != nil {
		return err
	}
	return os.Rename(r.path+".tmp", r.path)
}

func (r *Runtime) Name() string { return "encore-" + strings.ReplaceAll(r.config.MAC[9:], ":", "") }
func (r *Runtime) MAC() string  { return r.config.MAC }

// NoiseKey is for local pairing UI only; never include it in snapshots/logs.
func (r *Runtime) NoiseKey() string { return r.config.Key }

func (r *Runtime) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		return nil
	}
	key, err := esphome.ParsePSK(r.config.Key)
	if err != nil {
		return err
	}
	vl, err := net.Listen("tcp", ":6055")
	if err != nil {
		return err
	}
	cl, err := net.Listen("tcp", ":6053")
	if err != nil {
		vl.Close()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel = cancel
	r.control = &esphome.Server{Info: esphome.Info{Name: r.Name(), FriendlyName: "Encore Incubator", MACAddress: r.config.MAC, Manufacturer: "Encore", Model: "Generic Android", Version: "0.1.0", ProjectName: "encore.incubator"}, PSK: &key, Handler: r, WriteTimeout: 2 * time.Second}
	r.control.Info.BluetoothFeatures = r.proxy.Features()
	r.done.Go(func() { r.proxy.Run(ctx) })
	if r.light.Supported() {
		r.done.Go(func() { r.light.Run(ctx, func() { _ = r.control.Broadcast(lightapi.State(r.light)) }) })
	}
	r.done.Go(func() {
		if err := r.voice.Serve(ctx, vl); err != nil {
			r.fail(err)
		}
	})
	r.done.Go(func() {
		if err := r.control.Serve(ctx, cl); err != nil {
			r.fail(err)
		}
	})
	return nil
}

func (r *Runtime) fail(err error) { r.mu.Lock(); r.problem = err.Error(); r.mu.Unlock() }

func (r *Runtime) Close() {
	r.mu.Lock()
	cancel := r.cancel
	r.cancel = nil
	r.mu.Unlock()
	if cancel != nil {
		cancel()
		r.engine.SetMuted(true)
		r.done.Wait()
	}
}

func (r *Runtime) Capture(pcm []byte) error {
	if len(pcm) == 0 || len(pcm)%2 != 0 || len(pcm) > audio.MaxSamples*2 {
		return errors.New("invalid PCM block")
	}
	r.captureMu.Lock()
	defer r.captureMu.Unlock()
	samples := make([]int16, len(pcm)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(pcm[2*i:]))
	}
	f := audio.Frame{Samples: samples, Sequence: r.sequence, CapturedAt: time.Duration(r.samples) * time.Second / audio.SampleRate}
	r.sequence++
	r.samples += int64(len(samples))
	return r.voice.Capture(f)
}

func (r *Runtime) Talk() error { return r.engine.StartManual() }
func (r *Runtime) Stop()       { _ = r.engine.Stop() }
func (r *Runtime) SetMuted(muted bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	// Gate before saving and retain effective mute for subsequent settings saves.
	// Only an explicitly requested, successfully saved unmute clears this latch.
	r.engine.SetMuted(true)
	r.config.Muted = true
	c := r.config
	c.Muted = muted
	if err := r.save(c); err != nil {
		return err
	}
	r.config = c
	r.engine.SetMuted(muted)
	return nil
}

// The paired ESPHome controller supplies the full URL, including its origin.
// Choosing http:// explicitly permits trusted-LAN HTTP; TLS errors stay fatal.
func dashboardURL(value string) (string, error) {
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || len(value) > 1024 || u.User != nil || u.RawQuery != "" || u.Fragment != "" || (u.Scheme != "https" && u.Scheme != "http") {
		return "", errors.New("expected a full HTTP(S) Dashboard URL without credentials, query tokens or fragments")
	}
	return u.String(), nil
}

// Snapshot combines engine facts with current native permission/capture health.
// The host must pass false for unknown, stopped, silenced or stale capture.
func (r *Runtime) Snapshot(captureReady bool) string {
	r.mu.Lock()
	c, rev, problem := r.config, r.reload, r.problem
	r.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"voice": r.voice.Snapshot(captureReady), "models": r.voice.ModelCount(), "dashboard": c.Dashboard, "reload": rev, "problem": problem})
	return string(b)
}

func (r *Runtime) DismissProblem() {
	r.mu.Lock()
	r.problem = ""
	r.mu.Unlock()
}

const dashboardKey uint32 = 0x454e4301
const reloadKey uint32 = 0x454e4302

func (r *Runtime) Handle(ctx context.Context, c *esphome.Conn, msg proto.Message) error {
	switch m := msg.(type) {
	case *api.ListEntitiesRequest:
		if err := lightapi.List(c, r.light); err != nil {
			return err
		}
		if err := c.Send(&api.ListEntitiesTextResponse{ObjectId: "dashboard_url", Key: dashboardKey, Name: "Dashboard URL", EntityCategory: esphome.CategoryConfig, MaxLength: 1024}); err != nil {
			return err
		}
		if err := c.Send(&api.ListEntitiesButtonResponse{ObjectId: "reload_dashboard", Key: reloadKey, Name: "Reload dashboard", EntityCategory: esphome.CategoryConfig}); err != nil {
			return err
		}
		for _, entity := range []proto.Message{
			&api.ListEntitiesUpdateResponse{ObjectId: "apk_update", Key: updateKey, Name: "APK update", DeviceClass: "firmware", EntityCategory: esphome.CategoryConfig},
			&api.ListEntitiesSelectResponse{ObjectId: "update_channel", Key: channelKey, Name: "Update channel", Options: []string{"dev", "stable"}, EntityCategory: esphome.CategoryConfig},
			&api.ListEntitiesButtonResponse{ObjectId: "check_updates", Key: checkKey, Name: "Check for updates", EntityCategory: esphome.CategoryConfig},
		} {
			if err := c.Send(entity); err != nil {
				return err
			}
		}
		return r.proxy.Handle(ctx, c, msg)
	case *api.SubscribeStatesRequest:
		if state := lightapi.State(r.light); state != nil {
			if err := c.Send(state); err != nil {
				return err
			}
		}
		r.mu.Lock()
		v := r.config.Dashboard
		channel := r.config.Channel
		state := &api.UpdateStateResponse{Key: updateKey, MissingState: true}
		if r.updateState != nil {
			state = proto.Clone(r.updateState).(*api.UpdateStateResponse)
		}
		r.mu.Unlock()
		if err := c.Send(&api.SelectStateResponse{Key: channelKey, State: channel}); err != nil {
			return err
		}
		if err := c.Send(state); err != nil {
			return err
		}
		if err := c.Send(&api.TextStateResponse{Key: dashboardKey, State: v}); err != nil {
			return err
		}
		return r.proxy.Handle(ctx, c, msg)
	case *api.SelectCommandRequest:
		if m.Key != channelKey || (m.State != "dev" && m.State != "stable") {
			return nil
		}
		r.mu.Lock()
		next := r.config
		next.Channel = m.State
		if next.Channel != r.config.Channel {
			next.ChannelGeneration++
		}
		if err := r.save(next); err != nil {
			r.mu.Unlock()
			return err
		}
		r.config = next
		if r.updateState != nil {
			r.updateState.LatestVersion = ""
			r.updateState.MissingState = true
			r.updateState.InProgress = false
		}
		host := r.updater
		r.mu.Unlock()
		if err := r.control.Broadcast(&api.SelectStateResponse{Key: channelKey, State: m.State}); err != nil {
			return err
		}
		r.UpdateStatus(m.State, next.ChannelGeneration, "", "Checking selected channel", false, false)
		if host != nil {
			host.RequestUpdate(m.State, next.ChannelGeneration, false)
		}
	case *api.UpdateCommandRequest:
		if m.Key == updateKey && (m.Command == api.UpdateCommand_UPDATE_COMMAND_UPDATE || m.Command == api.UpdateCommand_UPDATE_COMMAND_CHECK) {
			r.requestUpdate(m.Command == api.UpdateCommand_UPDATE_COMMAND_UPDATE)
		}
	case *api.TextCommandRequest:
		if m.Key != dashboardKey {
			return nil
		}
		r.mu.Lock()
		v, err := dashboardURL(m.State)
		if err == nil {
			next := r.config
			next.Dashboard = v
			err = r.save(next)
			if err == nil {
				r.config = next
				r.reload++
			}
		}
		if err != nil {
			r.problem = fmt.Sprintf("Dashboard configuration: %s", err)
		} else {
			r.problem = ""
		}
		v = r.config.Dashboard
		r.mu.Unlock()
		return r.control.Broadcast(&api.TextStateResponse{Key: dashboardKey, State: v})
	case *api.ButtonCommandRequest:
		if m.Key == checkKey {
			r.requestUpdate(false)
		}
		if m.Key == reloadKey {
			r.mu.Lock()
			r.reload++
			r.mu.Unlock()
		}
	}
	return r.proxy.Handle(ctx, c, msg)
}

func (r *Runtime) requestUpdate(install bool) {
	r.mu.Lock()
	host, channel, generation := r.updater, r.config.Channel, r.config.ChannelGeneration
	r.mu.Unlock()
	if host != nil {
		host.RequestUpdate(channel, generation, install)
	}
}

// DependencyReport identifies the immutable backend in this package.
func DependencyReport() string {
	return "incubator-only; mixednet; microWakeWord frontend; no Assist adapter"
}
