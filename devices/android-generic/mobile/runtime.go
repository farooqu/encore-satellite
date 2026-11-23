// Package mobile is the Incubator-only APK composition root, bound with gomobile.
// It deliberately imports no Assist backend or runtime backend selector.
package mobile

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/farooqu/encore-satellite/backends/bluetooth"
	esphomeadapter "github.com/farooqu/encore-satellite/backends/esphome"
	"github.com/farooqu/encore-satellite/backends/esphome/announcements"
	"github.com/farooqu/encore-satellite/backends/esphome/controls"
	"github.com/farooqu/encore-satellite/backends/esphome/sensors"
	"github.com/farooqu/encore-satellite/backends/incubator"
	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/core/illuminance"
	"github.com/farooqu/encore-satellite/core/session"
	"github.com/farooqu/encore-satellite/internal/esphomepairing"
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
	Muted             bool   `json:"muted"`
	Channel           string `json:"channel"`
	ChannelGeneration int64  `json:"channel_generation"`
	BluetoothProxy    bool   `json:"bluetooth_proxy"`
	BluetoothPending  bool   `json:"bluetooth_setup_pending,omitempty"`
}

type Runtime struct {
	mu                  sync.Mutex
	path                string
	mac                 string
	manufacturer        string
	model               string
	config              settings
	engine              *session.Engine
	voice               *incubator.Server
	control             *esphome.Server
	announcements       *announcements.Player
	announcementStateMu sync.Mutex
	running             context.Context
	cancel              context.CancelFunc
	pairing             string // empty, waiting, or expired
	done                sync.WaitGroup
	sequence            uint64
	samples             int64
	captureMu           sync.Mutex
	dashboard           *dashboard
	problem             string
	updater             UpdateHost
	updateState         *api.UpdateStateResponse
	updateStatusText    string
	proxy               *bluetooth.Proxy
	radio               *androidhost.BluetoothScanner
	bluetoothConfigured bool
	light               *illuminance.Monitor
	microphone          *sensors.Microphone
	captureSuspended    bool
}

func Open(directory string, output Audio) (*Runtime, error) {
	return OpenWithBluetoothDefault(directory, output, false)
}

// OpenWithBluetoothDefault applies the device capability only to a new installation.
// Existing configurations retain their choice, including the legacy zero value.
func OpenWithBluetoothDefault(directory string, output Audio, supported bool) (*Runtime, error) {
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
		var id [6]byte
		if _, err = rand.Read(id[:]); err != nil {
			return nil, err
		}
		id[0] = (id[0] | 2) & 0xfe
		r.config.MAC = net.HardwareAddr(id[:]).String()
		r.config.Channel = "dev"
		r.config.BluetoothProxy = supported
		r.config.BluetoothPending = supported
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
	r.mac = r.config.MAC
	r.engine = session.New(androidhost.NewPlayback(output))
	r.engine.SetMuted(r.config.Muted)
	r.announcements = announcements.New(r.engine)
	r.microphone = sensors.NewMicrophone(r.config.Muted)
	// Live Monitor uses the primary capture and the foreground indicator gate.
	// Keep the separate wire AEC and intercom capabilities off.
	r.voice = &incubator.Server{Engine: r.engine, Name: r.Name(), MAC: r.config.MAC, Version: "0.1.0-android", EnableModels: true, Monitor: true}
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

func (r *Runtime) Name() string { return "encore-android-" + strings.ReplaceAll(r.mac[9:], ":", "") }
func (r *Runtime) MAC() string  { return r.mac }

// NoiseKey is for local pairing UI only; never include it in snapshots/logs.
func (r *Runtime) NoiseKey() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.config.Key
}

func (r *Runtime) Start() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		return nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	if r.config.Key != "" {
		key, err := esphome.ParsePSK(r.config.Key)
		if err != nil || key.IsZero() {
			cancel()
			return errors.New("invalid saved ESPHome key")
		}
		if err := r.startPaired(ctx, key); err != nil {
			cancel()
			return err
		}
	}
	r.running, r.cancel = ctx, cancel
	r.done.Go(func() { r.proxy.Run(ctx) })
	if r.light.Supported() {
		sources := r.sensors()
		r.done.Go(func() { sources.Illuminance.Run(ctx, func() { _ = r.broadcastControl(sources.IlluminanceState()) }) })
	}
	return nil
}

// ESPHomeVersion is the compatibility version used by Android's discovery advertisement.
const ESPHomeVersion = esphomeadapter.CompatibilityVersion

// ESPHomeFriendlyName is the unit's default display name in Home Assistant.
func (r *Runtime) ESPHomeFriendlyName() string {
	return "Encore Android · " + strings.ToUpper(strings.ReplaceAll(r.mac[12:], ":", ""))
}

// ConfigureHardware supplies Android's hardware labels before starting ESPHome.
func (r *Runtime) ConfigureHardware(manufacturer, model string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.manufacturer, r.model = manufacturer, model
}

// controlInfo is called under mu, before starting either ESPHome listener.
func (r *Runtime) controlInfo() esphome.Info {
	var version string
	if r.updateState != nil {
		version = r.updateState.CurrentVersion
	}
	return esphomeadapter.DeviceInfo(esphome.Info{Name: r.Name(), FriendlyName: r.ESPHomeFriendlyName(), MACAddress: r.MAC(),
		Manufacturer: r.manufacturer, Model: r.model, Version: version})
}

// startPaired is called under mu with a validated key, after bootstrap has
// closed if this is first pairing. Bind both listeners before serving either.
func (r *Runtime) startPaired(ctx context.Context, key esphome.PSK) error {
	l, err := net.Listen("tcp", ":6053")
	if err != nil {
		return err
	}
	vl, err := net.Listen("tcp", ":6055")
	if err != nil {
		l.Close()
		return err
	}
	r.serveListener(ctx, vl, r.voice.Serve)
	info := r.controlInfo()
	info.BluetoothFeatures = r.proxy.Features()
	control := &esphome.Server{Info: info, PSK: &key, Handler: r, WriteTimeout: 2 * time.Second,
		OnSetEncryptionKey: esphomepairing.RefuseKey}
	r.control = control
	r.serveListener(ctx, l, control.Serve)
	r.done.Go(func() { r.announcements.Run(ctx) })
	r.done.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-r.announcements.Changes():
				_ = r.sendAnnouncementState(control.Broadcast)
			}
		}
	})
	return nil
}

func (r *Runtime) sendAnnouncementState(send func(proto.Message) error) error {
	// Serialize the initial snapshot with broadcasts so it cannot overwrite a
	// newer state. The app uses unity gain; Android manages system volume.
	r.announcementStateMu.Lock()
	defer r.announcementStateMu.Unlock()
	return send(r.announcements.State(1))
}

type runtimeListener struct {
	net.Listener
	close func() error
}

func (l runtimeListener) Close() error { return l.close() }

func (r *Runtime) serveListener(ctx context.Context, l net.Listener, serve func(context.Context, net.Listener) error) {
	// Cancellation can wake Accept before the server's concurrent Close releases
	// the port. Join that Close before reporting that the runtime has stopped.
	listener := runtimeListener{l, sync.OnceValue(l.Close)}
	r.done.Go(func() {
		defer listener.Close()
		if err := serve(ctx, listener); err != nil {
			r.fail(err)
		}
	})
}

func (r *Runtime) broadcastControl(msg proto.Message) error {
	r.mu.Lock()
	control := r.control
	r.mu.Unlock()
	if control == nil {
		return nil
	}
	return control.Broadcast(msg)
}

// PairHomeAssistant is called only by the local first-run UI. Normal listeners
// stay closed until the restricted listener has persisted the first key.
func (r *Runtime) PairHomeAssistant() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.pairHomeAssistant(r.running)
}

// pairHomeAssistant requires mu. The bootstrap also caps ctx at two minutes.
func (r *Runtime) pairHomeAssistant(ctx context.Context) error {
	if r.cancel == nil || r.config.Key != "" || r.pairing == "waiting" {
		return errors.New("ESPHome pairing unavailable")
	}
	l, err := net.Listen("tcp", ":6053")
	if err != nil {
		return err
	}
	r.pairing = "waiting"
	info := r.controlInfo()
	r.done.Go(func() {
		var key esphome.PSK
		err := esphomepairing.Serve(ctx, l, info, func(supplied esphome.PSK) error {
			r.mu.Lock()
			defer r.mu.Unlock()
			if ctx.Err() != nil || r.config.Key != "" {
				return errors.New("ESPHome pairing unavailable")
			}
			next := r.config
			next.Key = supplied.String()
			if err := r.save(next); err != nil {
				r.problem = "Could not save ESPHome key"
				return errors.New(r.problem)
			}
			r.config, key = next, supplied
			return nil
		})
		r.mu.Lock()
		defer r.mu.Unlock()
		r.pairing = ""
		if r.running.Err() != nil {
			return
		}
		if err == nil && key.IsZero() {
			r.pairing = "expired"
			return
		}
		if err == nil {
			err = r.startPaired(r.running, key)
		}
		if err != nil {
			r.problem = "ESPHome could not start. Restart the satellite and try again."
		}
	})
	return nil
}

func (r *Runtime) fail(err error) { r.mu.Lock(); r.problem = err.Error(); r.mu.Unlock() }

func (r *Runtime) Close() {
	r.mu.Lock()
	cancel := r.cancel
	r.cancel = nil
	if cancel != nil {
		cancel()
		r.control = nil
		r.pairing = ""
		r.engine.SetMuted(true)
		// Shutdown gates this runtime without changing the saved preference.
		r.microphone.SetMuted(true)
	}
	r.mu.Unlock()
	if cancel != nil {
		r.done.Wait()
	}
}

func (r *Runtime) Capture(pcm []byte) error {
	if len(pcm) == 0 || len(pcm)%2 != 0 || len(pcm) > audio.MaxSamples*2 {
		return errors.New("invalid PCM block")
	}
	r.mu.Lock()
	paired := r.control != nil
	r.mu.Unlock()
	if !paired {
		return nil
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

func (r *Runtime) Talk() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.control == nil {
		return errors.New("ESPHome pairing is required for voice")
	}
	return r.engine.StartManual()
}
func (r *Runtime) Stop() { _ = r.engine.Stop() }

func (r *Runtime) SetPlaybackInterrupted(interrupted bool) {
	r.engine.SetPlaybackInterrupted(interrupted)
}

func (r *Runtime) StopAlert(sequence int64) { r.engine.StopAlert(uint64(sequence)) }

// SetMonitorVisible applies the Android overlay state for one exact request.
func (r *Runtime) SetMonitorVisible(id int64, visible bool) {
	if id <= 0 {
		return
	}
	if visible {
		r.engine.ConfirmMonitor(uint64(id))
	} else {
		r.engine.HideMonitor(uint64(id))
	}
}

// SuspendCapture gates voice while Android reconnects the satellite without
// while-in-use microphone permission. This does not change the saved mute setting.
func (r *Runtime) SuspendCapture(suspended bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.captureSuspended = suspended
	muted := r.config.Muted || suspended
	r.engine.SetMuted(muted)
	r.microphone.SetMuted(muted)
}

func (r *Runtime) SetMuted(muted bool) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	defer func() { r.microphone.SetMuted(r.config.Muted || r.captureSuspended) }()
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
	r.engine.SetMuted(muted || r.captureSuspended)
	return nil
}

// Snapshot combines engine facts with current native permission/capture health.
// The host must pass false for unknown, stopped, silenced or stale capture.
func (r *Runtime) Snapshot(captureReady bool) string {
	r.mu.Lock()
	var rev int
	if r.dashboard != nil {
		rev = r.dashboard.reload
	}
	problem := r.problem
	esphomeState := r.pairing
	captureReady = captureReady && r.control != nil
	if r.control != nil {
		esphomeState = "paired"
	} else if esphomeState == "" && r.config.Key == "" {
		esphomeState = "unpaired"
	}
	r.mu.Unlock()
	b, _ := json.Marshal(map[string]any{"voice": r.voice.Snapshot(captureReady), "models": r.voice.ModelCount(), "reload": rev, "problem": problem, "esphome": esphomeState})
	return string(b)
}

func (r *Runtime) DismissProblem() {
	r.mu.Lock()
	r.problem = ""
	r.mu.Unlock()
}

type dashboard struct{ reload int }

// ConfigureDashboard lets ESPHome reload a renderer via Snapshot.
func (r *Runtime) ConfigureDashboard() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		return errors.New("dashboard capability must be set before startup")
	}
	if r.dashboard == nil {
		r.dashboard = &dashboard{}
	}
	return nil
}

func (r *Runtime) sensors() sensors.Sources {
	return sensors.Sources{Illuminance: r.light, MicrophoneMuted: r.microphone.Muted}
}

func (r *Runtime) writable() controls.Bindings {
	bindings := controls.Bindings{
		controls.AndroidChannel.Bind(r.setUpdateChannel),
		controls.Announcements.Bind(r.announcements.Command),
	}
	r.mu.Lock()
	dashboard := r.dashboard
	hasUpdater := r.updater != nil
	r.mu.Unlock()
	if dashboard != nil {
		bindings = append(bindings, controls.ReloadDashboard.Bind(func(_ *esphome.Conn, _ *api.ButtonCommandRequest) error {
			r.mu.Lock()
			dashboard.reload++
			r.mu.Unlock()
			return nil
		}))
	}
	if hasUpdater {
		bindings = append(bindings,
			controls.AndroidUpdate.Bind(func(_ *esphome.Conn, m *api.UpdateCommandRequest) error {
				if m.Command == api.UpdateCommand_UPDATE_COMMAND_UPDATE || m.Command == api.UpdateCommand_UPDATE_COMMAND_CHECK {
					r.requestUpdate(m.Command == api.UpdateCommand_UPDATE_COMMAND_UPDATE)
				}
				return nil
			}),
			controls.AndroidCheck.Bind(func(_ *esphome.Conn, _ *api.ButtonCommandRequest) error { r.requestUpdate(false); return nil }),
		)
	}
	return bindings
}

func (r *Runtime) Handle(ctx context.Context, c *esphome.Conn, msg proto.Message) error {
	if controls.IsCommand(msg) {
		if handled, err := r.writable().Handle(c, msg); handled {
			return err
		}
	}
	switch msg.(type) {
	case *api.ListEntitiesRequest:
		if err := r.sensors().List(c.Send); err != nil {
			return err
		}
		if err := r.writable().List(c.Send); err != nil {
			return err
		}
		r.mu.Lock()
		hasUpdater := r.updater != nil
		r.mu.Unlock()
		if hasUpdater {
			if err := c.Send(&api.ListEntitiesTextSensorResponse{Key: updateStatusKey, ObjectId: "apk_update_status",
				Name: "APK update status", EntityCategory: esphome.CategoryDiagnostic}); err != nil {
				return err
			}
		}
		return r.proxy.Handle(ctx, c, msg)
	case *api.SubscribeStatesRequest:
		if state := r.sensors().IlluminanceState(); state != nil {
			if err := c.Send(state); err != nil {
				return err
			}
		}
		r.microphone.Subscribe(c)
		r.mu.Lock()
		channel := r.config.Channel
		updateStatus := r.updateStatusText
		var state *api.UpdateStateResponse
		if r.updater != nil && r.updateState != nil {
			state = proto.Clone(r.updateState).(*api.UpdateStateResponse)
		}
		r.mu.Unlock()
		states := []proto.Message{&api.SelectStateResponse{Key: channelKey, State: channel}}
		if state != nil {
			if err := c.Send(updateDiagnostic(updateStatus)); err != nil {
				return err
			}
			states = append(states, state)
		}
		if err := r.writable().SendStates(c.Send, states); err != nil {
			return err
		}
		if err := r.sendAnnouncementState(c.Send); err != nil {
			return err
		}
		return r.proxy.Handle(ctx, c, msg)
	}
	return r.proxy.Handle(ctx, c, msg)
}

func (r *Runtime) ConnClosed(c *esphome.Conn) {
	r.announcements.ConnClosed(c)
	r.microphone.Unsubscribe(c)
	r.proxy.ConnClosed(c)
}

func (r *Runtime) setUpdateChannel(_ *esphome.Conn, m *api.SelectCommandRequest) error {
	if m.State != "dev" && m.State != "stable" {
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
		r.updateStatusText = "Checking selected channel"
		r.updateState.LatestVersion = r.updateState.CurrentVersion
		r.updateState.ReleaseSummary = r.updateStatusText
		r.updateState.ReleaseUrl = ""
		r.updateState.MissingState = true
		r.updateState.InProgress = false
	}
	host := r.updater
	r.mu.Unlock()
	broadcastErr := r.broadcastControl(&api.SelectStateResponse{Key: channelKey, State: m.State})
	r.UpdateStatus(m.State, next.ChannelGeneration, "", "Checking selected channel", false, false, "", "")
	if host != nil {
		host.RequestUpdate(m.State, next.ChannelGeneration, false)
	}
	return broadcastErr
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
