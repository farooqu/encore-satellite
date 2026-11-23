package biscuit

import (
	"context"
	"errors"
	"log"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/libp2p/zeroconf/v2"
	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"

	"github.com/farooqu/encore-satellite/backends/bluetooth"
	"github.com/farooqu/encore-satellite/backends/esphome/announcements"
	"github.com/farooqu/encore-satellite/backends/esphome/controls"
	"github.com/farooqu/encore-satellite/backends/esphome/sensors"
	"github.com/farooqu/encore-satellite/core/music"
	"github.com/farooqu/encore-satellite/internal/esphomepairing"
	"github.com/farooqu/encore-satellite/platforms/linuxsendspin"
)

const musicStateDir = "/data/encore/sendspin"
const musicExecutable = "/usr/local/bin/encore-sendspin"

// Both BLE and music publish a whole config. Serialize changes at the owner so
// concurrent controls cannot replace one another's independently saved settings.
type settings struct {
	mu     sync.Mutex
	path   string
	config Config
}

func (s *settings) update(change func(*Config)) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	next := s.config
	change(&next)
	if err := SaveConfig(s.path, next); err != nil {
		return err
	}
	s.config = next
	return nil
}

type musicService struct {
	mu       sync.Mutex
	configMu sync.Mutex
	settings *settings
	player   *speaker
	name     string
	enabled  bool
	revision uint64
	worker   *musicWorker
	changed  chan struct{}
	discover func(context.Context) (string, error)
	run      func(context.Context, linuxsendspin.Options) error
}

type musicWorker struct {
	revision     uint64
	ready        linuxsendspin.Readiness // guarded by musicService.mu
	commands     chan linuxsendspin.CommandRequest
	pausePending time.Time // physical Pause must be followed promptly by Stopped
	resumePaused bool      // explicit Action pause in this authenticated group
}

func (w *musicWorker) updateReadiness(next linuxsendspin.Readiness) {
	sameGroup := next.Ready && w.ready.Ready && next.Group != 0 && next.Group == w.ready.Group
	if !sameGroup || !next.Stopped {
		w.resumePaused = false
	} else if w.ready.Playing {
		w.resumePaused = time.Now().Before(w.pausePending)
	}
	// A different snapshot fences the pending command, even if the mask
	// changed without any playback transition.
	w.pausePending = time.Time{}
	w.ready = next
}

func newMusic(c Config, s *settings, player *speaker) *musicService {
	return &musicService{settings: s, player: player, name: c.Name, enabled: c.Sendspin, changed: make(chan struct{}, 1), discover: discoverMusic, run: linuxsendspin.Run}
}
func (m *musicService) snapshot() (bool, uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled, m.revision
}
func (m *musicService) configure(enabled bool) error {
	m.configMu.Lock()
	defer m.configMu.Unlock()
	wasEnabled, _ := m.snapshot()
	if enabled == wasEnabled {
		return nil
	}
	err := m.settings.update(func(c *Config) {
		c.Sendspin = enabled
	})
	if err != nil {
		return err
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	m.enabled = enabled
	m.revision++
	// Kill audible output synchronously. Lifecycle teardown/replacement follows
	// on the music worker, never while holding a voice/session lock.
	m.player.StopMusic()
	select {
	case m.changed <- struct{}{}:
	default:
	}
	return nil
}

// actionPlayback only takes over from idle voice for authoritative playback or
// for a Stopped transition immediately following our own Pause. This SDK and
// Music Assistant do not distinguish paused from stopped on the wire.
func (m *musicService) actionPlayback() bool {
	if m == nil {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	w := m.worker
	if !m.enabled || w == nil || w.revision != m.revision || !w.ready.Ready {
		return false
	}
	if w.ready.Playing {
		if !m.player.PlayMusic() && w.ready.Supports(linuxsendspin.Pause) && w.queue(linuxsendspin.Pause) && w.ready.Group != 0 {
			w.pausePending = time.Now().Add(5 * time.Second)
		}
		return true // Unsupported Pause is a no-op, not a voice invocation.
	}
	if w.ready.Stopped && w.resumePaused {
		if w.ready.Supports(linuxsendspin.Play) {
			m.player.PlayMusic()
			w.queue(linuxsendspin.Play)
		}
		return true // Keep the resume intent if Play is unavailable or not acted on.
	}
	return false
}

// Only the intents this player implements are translated to companion commands.
var sendspinCommands = map[music.Command]linuxsendspin.Command{
	music.Play:     linuxsendspin.Play,
	music.Stop:     linuxsendspin.Stop,
	music.Pause:    linuxsendspin.Pause,
	music.Next:     linuxsendspin.Next,
	music.Previous: linuxsendspin.Previous,
}

func (m *musicService) command(intent music.Command) {
	m.mu.Lock()
	defer m.mu.Unlock()
	// Stop is a local safety action even with no companion, or a server
	// that does not advertise the network Stop command.
	if intent == music.Stop {
		m.player.StopMusic()
	}
	w := m.worker
	if !m.enabled || w == nil || w.revision != m.revision || !w.ready.Ready {
		return
	}
	if intent == music.Stop {
		select {
		case <-w.commands:
		default:
		}
	}
	if intent == music.Play {
		// This can resume local audio even when the remote queue is already
		// playing and does not offer Play. Privacy still owns the final veto.
		m.player.PlayMusic()
	}
	cmd := sendspinCommands[intent]
	canDispatch := intent.CanDispatch(true, w.ready.Supports(cmd))
	if intent == music.Stop || intent == music.Play || canDispatch {
		// A separate explicit transport action takes ownership from the key.
		w.resumePaused, w.pausePending = false, time.Time{}
	}
	if !canDispatch {
		return
	}
	w.queue(cmd)
}

func (w *musicWorker) queue(cmd linuxsendspin.Command) bool {
	// Latest explicit intent wins; privacy is independently enforced in Mixer.
	select {
	case <-w.commands:
	default:
	}
	select {
	case w.commands <- linuxsendspin.CommandRequest{Command: cmd, Generation: w.ready.Generation}:
		return true
	default:
		return false
	}
}

func (m *musicService) Run(ctx context.Context) {
	var cancel context.CancelFunc
	var done chan error
	var worker *musicWorker
	var retry <-chan time.Time
	var retryRevision uint64
	clear := func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.worker != nil {
			m.worker = nil
			m.player.StopMusic()
		}
	}
	stop := func() {
		clear()
		if cancel != nil {
			cancel()
			<-done
			cancel = nil
			done = nil
		}
	}
	defer stop()
	for {
		enabled, revision := m.snapshot()
		if worker != nil && worker.revision != revision {
			stop()
			worker = nil
			continue // Settings may have changed again while joining the old process.
		}
		if retryRevision != revision {
			retry = nil
		}
		if enabled && done == nil && retry == nil {
			w := &musicWorker{revision: revision, commands: make(chan linuxsendspin.CommandRequest, 1)}
			childCtx, childCancel := context.WithCancel(ctx)
			m.mu.Lock()
			if revision != m.revision || ctx.Err() != nil {
				m.mu.Unlock()
				childCancel()
				if ctx.Err() != nil {
					return
				}
				continue
			}
			worker, m.worker = w, w
			m.mu.Unlock()
			cancel = childCancel
			done = make(chan error, 1)
			opts := linuxsendspin.Options{Name: m.name, StateDir: musicStateDir, Executable: musicExecutable, Output: m.player, Commands: w.commands,
				OnReadiness: func(ready linuxsendspin.Readiness) {
					m.mu.Lock()
					defer m.mu.Unlock()
					if m.worker != w || w.revision != m.revision || childCtx.Err() != nil {
						return
					}
					if w.ready.Ready && !ready.Ready {
						m.player.StopMusic()
					}
					w.updateReadiness(ready)
				},
			}
			go func(result chan error) {
				endpoint, err := m.discover(childCtx)
				if err != nil {
					result <- err
					return
				}
				opts.URL = endpoint
				result <- m.run(childCtx, opts)
			}(done)
		}
		select {
		case <-ctx.Done():
			return
		case <-m.changed:
		case <-retry:
			retry = nil
		case err := <-done:
			clear()
			cancel()
			cancel = nil
			done = nil
			if ctx.Err() == nil {
				log.Printf("Sendspin stopped (%v); voice and BLE remain available", err)
			}
			retryRevision = worker.revision
			worker = nil
			retry = time.After(5 * time.Second)
		}
	}
}

// Discovery is Wi-Fi only and refuses to guess between different servers.
// mDNS supplies a dial address, never credentials or proof of identity.
func discoverMusic(ctx context.Context) (string, error) {
	iface, err := net.InterfaceByName("wlan0")
	if err != nil {
		return "", errors.New("sendspin: Wi-Fi interface unavailable for discovery")
	}
	browseCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	entries := make(chan *zeroconf.ServiceEntry, 8)
	done := make(chan error, 1)
	go func() {
		done <- zeroconf.Browse(browseCtx, "_sendspin-server._tcp", "local.", entries,
			zeroconf.SelectIfaces([]net.Interface{*iface}), zeroconf.SelectIPTraffic(zeroconf.IPv4))
	}()
	return chooseMusicEndpoint(ctx, entries, done)
}

func chooseMusicEndpoint(ctx context.Context, entries <-chan *zeroconf.ServiceEntry, done <-chan error) (string, error) {
	var instance, endpoint string
	ambiguous := false
	accept := func(entry *zeroconf.ServiceEntry) {
		if url := musicEndpointFromAdvertisement(entry); url != "" {
			if instance != "" && (instance != entry.Instance || endpoint != url) {
				ambiguous = true
			}
			instance, endpoint = entry.Instance, url
		}
	}
	// zeroconf can be blocked delivering an entry even after cancellation.
	// Keep draining until Browse exits, including on ambiguous results.
	for {
		select {
		case err := <-done:
			if ctx.Err() != nil {
				return "", ctx.Err()
			}
			if err != nil {
				return "", errors.New("sendspin: mDNS browse failed")
			}
			if entries != nil {
				for entry := range entries {
					accept(entry)
				}
			}
			if ambiguous {
				return "", errors.New("sendspin: multiple servers discovered; advertise only one")
			}
			if endpoint == "" {
				return "", errors.New("sendspin: no compatible server discovered on Wi-Fi")
			}
			return endpoint, nil
		case entry, ok := <-entries:
			if ok {
				accept(entry)
			} else {
				entries = nil
			}
		}
	}
}

func musicEndpointFromAdvertisement(entry *zeroconf.ServiceEntry) string {
	if entry == nil || entry.Instance == "" || entry.Port < 1 || entry.Port > 65535 {
		return ""
	}
	path := ""
	for _, txt := range entry.Text {
		if value, found := strings.CutPrefix(txt, "path="); found {
			if path != "" || value != "/sendspin" {
				return ""
			}
			path = value
		}
	}
	if path == "" {
		return ""
	}
	for _, ip := range entry.AddrIPv4 {
		if addr := ip.To4(); addr != nil && addr.IsGlobalUnicast() {
			return "ws://" + net.JoinHostPort(addr.String(), strconv.Itoa(entry.Port)) + path
		}
	}
	return ""
}

// Dot adds its music controls before letting Proxy emit the sole ListEntitiesDone.
// This endpoint is independent of the package's Incubator voice transport.
type biscuitControls struct {
	*bluetooth.Proxy
	player        *speaker
	music         *musicService
	management    *management
	updates       *linuxUpdates
	ring          *ringSettings
	announcements *announcements.Player
	microphone    *sensors.Microphone
	mu            sync.Mutex
	stateMu       sync.Mutex
	subscribers   map[*esphome.Conn]struct{}
}

var (
	speakerVolume    = controls.SpeakerVolume(speakerMaxPercent, 1.0/120*100)
	speakerVolumeKey = speakerVolume.Key()
	speakerMuteKey   = controls.SpeakerMute.Key()
	musicKey         = controls.Sendspin.Key()
)

var musicButtons = []struct {
	entity *api.ListEntitiesButtonResponse
	intent music.Command
}{
	{musicButton("sendspin_pause", "Pause music", "mdi:pause"), music.Pause},
	{musicButton("sendspin_next", "Next track", "mdi:skip-next"), music.Next},
	{musicButton("sendspin_previous", "Previous track", "mdi:skip-previous"), music.Previous},
}

func musicButton(id, name, icon string) *api.ListEntitiesButtonResponse {
	return &api.ListEntitiesButtonResponse{
		ObjectId: id, Key: (&esphome.Base{ObjectID: id}).Key(), Name: name, Icon: icon,
	}
}

func (d *biscuitControls) sensorSources() sensors.Sources {
	var sources sensors.Sources
	if d.management != nil {
		sources = d.management.sensorSources()
	}
	if d.microphone != nil {
		sources.MicrophoneMuted = d.microphone.Muted
	}
	return sources
}

func (d *biscuitControls) writable() controls.Bindings {
	var bindings controls.Bindings
	report := func(err error) error {
		if err != nil {
			return err
		}
		return d.broadcast()
	}
	bindings = append(bindings,
		speakerVolume.Bind(func(_ *esphome.Conn, v *api.NumberCommandRequest) error {
			return report(d.player.setOutputVolume(v.State))
		}),
		controls.SpeakerMute.Bind(func(_ *esphome.Conn, v *api.SwitchCommandRequest) error {
			d.player.setOutputMute(v.State)
			return d.broadcast()
		}),
	)
	if d.announcements != nil {
		bindings = append(bindings, controls.Announcements.Bind(d.announcements.Command))
	}
	if d.ring != nil {
		bindings = append(bindings,
			controls.RingBrightness.Bind(func(_ *esphome.Conn, v *api.NumberCommandRequest) error {
				return report(d.ring.configure(&v.State, nil))
			}),
			controls.RingMotion.Bind(func(_ *esphome.Conn, v *api.SwitchCommandRequest) error {
				return report(d.ring.configure(nil, &v.State))
			}),
		)
	}
	if d.updates != nil {
		if d.updates.supportsChannelSelection() {
			bindings = append(bindings, controls.LinuxChannel.Bind(func(_ *esphome.Conn, v *api.SelectCommandRequest) error {
				return report(d.updates.selectChannel(v.State))
			}))
		}
		bindings = append(bindings,
			controls.LinuxCheck.Bind(func(_ *esphome.Conn, _ *api.ButtonCommandRequest) error {
				d.updates.request(false)
				return d.broadcast()
			}),
			controls.LinuxUpdate.Bind(func(_ *esphome.Conn, v *api.UpdateCommandRequest) error {
				if v.DeviceId != 0 {
					return nil
				}
				switch v.Command {
				case api.UpdateCommand_UPDATE_COMMAND_CHECK:
					d.updates.request(false)
				case api.UpdateCommand_UPDATE_COMMAND_UPDATE:
					d.updates.request(true)
				}
				return d.broadcast()
			}),
		)
	}
	if d.music != nil {
		bindings = append(bindings,
			controls.Sendspin.Bind(func(_ *esphome.Conn, v *api.SwitchCommandRequest) error {
				return report(d.music.configure(v.State))
			}),
			controls.SendspinPlay.Bind(func(_ *esphome.Conn, _ *api.ButtonCommandRequest) error {
				d.music.command(music.Play)
				return nil
			}),
			controls.SendspinStop.Bind(func(_ *esphome.Conn, _ *api.ButtonCommandRequest) error {
				d.music.command(music.Stop)
				return nil
			}),
		)
	}
	return bindings
}

func (d *biscuitControls) Handle(ctx context.Context, c *esphome.Conn, msg proto.Message) error {
	if controls.IsCommand(msg) {
		if handled, err := d.writable().Handle(c, msg); handled {
			return err
		}
	}
	switch v := msg.(type) {
	case *api.ButtonCommandRequest:
		if d.music != nil && v.DeviceId == 0 {
			for _, button := range musicButtons {
				if v.Key == button.entity.Key {
					d.music.command(button.intent)
					return nil
				}
			}
		}
	case *api.ListEntitiesRequest:
		if err := d.writable().List(c.Send); err != nil {
			return err
		}
		if d.music != nil {
			for _, button := range musicButtons {
				if err := c.Send(proto.Clone(button.entity)); err != nil {
					return err
				}
			}
		}
		if err := d.sensorSources().List(c.Send); err != nil {
			return err
		}
		if d.management != nil {
			// The Proxy's standalone handler ends the listing itself. Compose
			// its one fixed entity here so all Linux-only actions precede Done.
			if err := d.Proxy.ListControls(c); err != nil {
				return err
			}
			// Management actions terminate the combined entity list exactly once.
			return d.management.actions.Handle(ctx, c, msg)
		}
	case *api.SubscribeStatesRequest:
		d.mu.Lock()
		if d.subscribers == nil {
			d.subscribers = make(map[*esphome.Conn]struct{})
		}
		d.subscribers[c] = struct{}{}
		d.mu.Unlock()
		if err := d.state(c); err != nil {
			return err
		}
		if d.microphone != nil {
			d.microphone.Subscribe(c)
		}
	case *api.ExecuteServiceRequest:
		if d.management != nil {
			if err := d.management.execute(ctx, c, v); err != nil {
				return err
			}
			return d.broadcast()
		}
	}
	return d.Proxy.Handle(ctx, c, msg)
}
func (d *biscuitControls) state(c *esphome.Conn) error {
	// Serialize snapshots and delivery so concurrent key/API broadcasts cannot
	// send an older volume after a newer one on the same subscription.
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	volume, muted := d.player.outputState()
	states := []proto.Message{
		&api.NumberStateResponse{Key: speakerVolumeKey, State: volume},
		&api.SwitchStateResponse{Key: speakerMuteKey, State: muted},
	}
	if d.announcements != nil {
		states = append(states, d.announcements.State(volume/100))
	}
	if d.ring != nil {
		states = append(states, d.ring.states()...)
	}
	if d.updates != nil {
		states = append(states, d.updates.states()...)
	}
	if d.music != nil {
		enabled, _ := d.music.snapshot()
		states = append(states, &api.SwitchStateResponse{Key: musicKey, State: enabled})
	}
	bindings := d.writable()
	if err := bindings.SendStates(c.Send, states); err != nil {
		return err
	}
	if d.management != nil {
		for _, state := range d.management.sensorSources().States() {
			if err := c.Send(state); err != nil {
				return err
			}
		}
	}
	return nil
}

func (d *biscuitControls) broadcast() error {
	d.mu.Lock()
	peers := make([]*esphome.Conn, 0, len(d.subscribers))
	for c := range d.subscribers {
		peers = append(peers, c)
	}
	d.mu.Unlock()
	for _, c := range peers {
		if err := d.state(c); err != nil {
			_ = c.Close()
		}
	}
	return nil
}
func (d *biscuitControls) ConnClosed(c *esphome.Conn) {
	if d.announcements != nil {
		d.announcements.ConnClosed(c)
	}
	d.mu.Lock()
	delete(d.subscribers, c)
	d.mu.Unlock()
	if d.microphone != nil {
		d.microphone.Unsubscribe(c)
	}
	d.Proxy.ConnClosed(c)
}
func (d *biscuitControls) Run(ctx context.Context) {
	var workers sync.WaitGroup
	workers.Go(func() { d.Proxy.Run(ctx) })
	if d.music != nil {
		workers.Go(func() { d.music.Run(ctx) })
	}
	workers.Go(func() {
		for {
			select {
			case <-ctx.Done():
				return
			case <-d.player.volumeChanged:
				_ = d.broadcast()
			}
		}
	})
	if d.announcements != nil {
		workers.Go(func() { d.announcements.Run(ctx) })
		workers.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-d.announcements.Changes():
					_ = d.broadcast()
				}
			}
		})
	}
	if d.management != nil {
		workers.Go(func() { d.watchManagement(ctx) })
	}
	if d.updates != nil {
		workers.Go(func() { d.updates.run(ctx, d.broadcast) })
	}
	workers.Wait()
}
func (d *biscuitControls) Serve(ctx context.Context, l net.Listener, info esphome.Info, key esphome.PSK) error {
	if key.IsZero() {
		return errors.New("dot: a nonzero Noise key is required")
	}
	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() { defer close(done); d.Run(ctx) }()
	defer func() { cancel(); <-done }()
	info.BluetoothFeatures = d.Features()
	info.VoiceFeatures = 0 // Common controls are not a second Assist voice endpoint.
	return (&esphome.Server{Info: info, PSK: &key, Handler: d, WriteTimeout: 2 * time.Second,
		OnSetEncryptionKey: esphomepairing.RefuseKey,
	}).Serve(ctx, l)
}
