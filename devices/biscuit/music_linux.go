package biscuit

import (
	"context"
	"errors"
	"log"
	"net"
	"sync"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"

	"github.com/farooqu/encore-satellite/backends/bluetooth"
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
	mu                        sync.Mutex
	settings                  *settings
	player                    *speaker
	name, url                 string
	enabled                   bool
	revision, runningRevision uint64
	running                   bool
	changed                   chan struct{}
	commands                  chan linuxsendspin.Command
	run                       func(context.Context, linuxsendspin.Options) error
}

func newMusic(c Config, s *settings, player *speaker) *musicService {
	return &musicService{settings: s, player: player, name: c.Name, url: c.SendspinURL, enabled: c.Sendspin, changed: make(chan struct{}, 1), commands: make(chan linuxsendspin.Command, 1), run: linuxsendspin.Run}
}
func (m *musicService) snapshot() (bool, string, uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.enabled, m.url, m.revision
}
func (m *musicService) configure(enabled *bool, url *string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if (enabled == nil || *enabled == m.enabled) && (url == nil || *url == m.url) {
		return nil
	}
	err := m.settings.update(func(c *Config) {
		if enabled != nil {
			c.Sendspin = *enabled
		}
		if url != nil {
			c.SendspinURL = *url
		}
	})
	if err != nil {
		return err
	}
	if enabled != nil {
		m.enabled = *enabled
	}
	if url != nil {
		m.url = *url
	}
	m.revision++
	// Kill audible output synchronously. Lifecycle teardown/replacement follows
	// on the music worker, never while holding a voice/session lock.
	m.player.StopMusic()
	select {
	case <-m.commands:
	default:
	}
	select {
	case m.changed <- struct{}{}:
	default:
	}
	return nil
}

// Only the intents this player implements are translated to companion commands.
var sendspinCommands = map[music.Command]linuxsendspin.Command{
	music.Play: linuxsendspin.Play,
	music.Stop: linuxsendspin.Stop,
}

func (m *musicService) command(intent music.Command) {
	m.mu.Lock()
	defer m.mu.Unlock()
	cmd, supported := sendspinCommands[intent]
	ready := m.enabled && m.running && m.runningRevision == m.revision
	if !intent.CanDispatch(ready, supported) {
		return
	}
	switch intent {
	case music.Play:
		m.player.PlayMusic()
	case music.Stop:
		m.player.StopMusic()
	}
	// Latest explicit intent wins; privacy is independently enforced in Mixer.
	select {
	case <-m.commands:
	default:
	}
	select {
	case m.commands <- cmd:
	default:
	}
}

func (m *musicService) Run(ctx context.Context) {
	var cancel context.CancelFunc
	var done chan error
	var retry <-chan time.Time
	stop := func() {
		m.mu.Lock()
		m.running = false
		m.mu.Unlock()
		if cancel != nil {
			cancel()
			<-done
			cancel = nil
			done = nil
		}
		m.player.StopMusic()
	}
	defer stop()
	for {
		enabled, url, revision := m.snapshot()
		if enabled && done == nil && retry == nil {
			childCtx, childCancel := context.WithCancel(ctx)
			cancel = childCancel
			done = make(chan error, 1)
			m.mu.Lock()
			m.running, m.runningRevision = true, revision
			m.mu.Unlock()
			// Enabling/reconnection never releases an existing Stop/privacy latch.
			opts := linuxsendspin.Options{Name: m.name, URL: url, StateDir: musicStateDir, Executable: musicExecutable, Output: m.player, Commands: m.commands}
			go func(result chan error) { result <- m.run(childCtx, opts) }(done)
		}
		select {
		case <-ctx.Done():
			return
		case <-m.changed:
			m.mu.Lock()
			stale := m.runningRevision != m.revision
			m.mu.Unlock()
			if stale {
				stop()
				retry = nil
			}
		case <-retry:
			retry = nil
		case err := <-done:
			m.mu.Lock()
			m.running = false
			m.mu.Unlock()
			cancel()
			cancel = nil
			done = nil
			m.player.StopMusic()
			if ctx.Err() == nil {
				log.Printf("Sendspin stopped (%v); voice and BLE remain available", err)
			}
			retry = time.After(5 * time.Second)
		}
	}
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
	announcements *announcementPlayer
	mu            sync.Mutex
	stateMu       sync.Mutex
	subscribers   map[*esphome.Conn]struct{}
}

var (
	speakerVolumeKey = (&esphome.Base{ObjectID: "speaker_volume"}).Key()
	speakerMuteKey   = (&esphome.Base{ObjectID: "speaker_mute"}).Key()
	musicKey         = (&esphome.Switch{Base: esphome.Base{ObjectID: "sendspin"}}).Key()
	musicURLKey      = (&esphome.Text{Base: esphome.Base{ObjectID: "sendspin_url"}}).Key()
	musicPlayKey     = (&esphome.Button{Base: esphome.Base{ObjectID: "sendspin_play"}}).Key()
	musicStopKey     = (&esphome.Button{Base: esphome.Base{ObjectID: "sendspin_stop"}}).Key()
)

func (d *biscuitControls) Handle(ctx context.Context, c *esphome.Conn, msg proto.Message) error {
	switch v := msg.(type) {
	case *api.ListEntitiesRequest:
		if d.announcements != nil {
			if err := d.announcements.list(c); err != nil {
				return err
			}
		}
		if d.ring != nil {
			if err := d.ring.list(c); err != nil {
				return err
			}
		}
		if d.updates != nil {
			if err := d.updates.list(c); err != nil {
				return err
			}
		}
		entities := []proto.Message{
			&api.ListEntitiesNumberResponse{Key: speakerVolumeKey, ObjectId: "speaker_volume", Name: "Speaker volume", Icon: "mdi:volume-high", MinValue: 0, MaxValue: speakerMaxPercent, Step: 1.0 / 120 * 100, UnitOfMeasurement: "%"},
			&api.ListEntitiesSwitchResponse{Key: speakerMuteKey, ObjectId: "speaker_mute", Name: "Speaker mute", Icon: "mdi:volume-off"},
		}
		if d.music != nil {
			entities = append(entities,
				&api.ListEntitiesSwitchResponse{Key: musicKey, ObjectId: "sendspin", Name: "Sendspin music", Icon: "mdi:music", EntityCategory: esphome.CategoryConfig},
				&api.ListEntitiesTextResponse{Key: musicURLKey, ObjectId: "sendspin_url", Name: "Sendspin server", Icon: "mdi:server", EntityCategory: esphome.CategoryConfig, MinLength: 0, MaxLength: 255},
				&api.ListEntitiesButtonResponse{Key: musicPlayKey, ObjectId: "sendspin_play", Name: "Resume music", Icon: "mdi:play"},
				&api.ListEntitiesButtonResponse{Key: musicStopKey, ObjectId: "sendspin_stop", Name: "Stop music", Icon: "mdi:stop"},
			)
		}
		for _, ent := range entities {
			if err := c.Send(ent); err != nil {
				return err
			}
		}
		if d.management != nil {
			// The Proxy's standalone handler ends the listing itself. Compose
			// its one fixed entity here so all Linux-only actions precede Done.
			if err := c.Send(&api.ListEntitiesSwitchResponse{ObjectId: "bluetooth_proxy", Key: (&esphome.Base{ObjectID: "bluetooth_proxy"}).Key(), Name: "Bluetooth proxy", Icon: "mdi:bluetooth", EntityCategory: esphome.CategoryConfig}); err != nil {
				return err
			}
			return d.management.list(ctx, c)
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
	case *api.MediaPlayerCommandRequest:
		if d.announcements != nil && v.Key == announcementKey && v.DeviceId == 0 {
			return d.announcements.command(c, v)
		}
	case *api.SelectCommandRequest:
		if d.updates != nil && v.Key == updateChannelKey {
			if err := d.updates.selectChannel(v.State); err != nil {
				return err
			}
			return d.broadcast()
		}
	case *api.UpdateCommandRequest:
		if d.updates != nil && v.Key == linuxUpdateKey && v.DeviceId == 0 {
			switch v.Command {
			case api.UpdateCommand_UPDATE_COMMAND_CHECK:
				d.updates.request(false)
			case api.UpdateCommand_UPDATE_COMMAND_UPDATE:
				d.updates.request(true)
			}
			return d.broadcast()
		}
	case *api.SwitchCommandRequest:
		if v.Key == speakerMuteKey {
			d.player.setOutputMute(v.State)
			return d.broadcast()
		}
		if d.ring != nil && v.Key == ringMotionKey {
			if err := d.ring.configure(nil, &v.State); err != nil {
				return err
			}
			return d.broadcast()
		}
		if d.management != nil && d.management.ssh != nil && v.Key == sshEnabledKey {
			if err := d.management.ssh.setEnabled(v.State); err != nil {
				return err
			}
			return d.broadcast()
		}
		if d.music != nil && v.Key == musicKey {
			if err := d.music.configure(&v.State, nil); err != nil {
				return err
			}
			return d.broadcast()
		}
	case *api.NumberCommandRequest:
		if v.Key == speakerVolumeKey {
			if err := d.player.setOutputVolume(v.State); err != nil {
				return err
			}
			return d.broadcast()
		}
		if d.ring != nil && v.Key == ringBrightnessKey {
			if err := d.ring.configure(&v.State, nil); err != nil {
				return err
			}
			return d.broadcast()
		}
	case *api.TextCommandRequest:
		if d.music != nil && v.Key == musicURLKey {
			if err := d.music.configure(nil, &v.State); err != nil {
				return err
			}
			return d.broadcast()
		}
	case *api.ButtonCommandRequest:
		if d.updates != nil && v.Key == checkUpdatesKey {
			d.updates.request(false)
			return d.broadcast()
		}
		if d.music != nil && v.Key == musicPlayKey {
			d.music.command(music.Play)
			return nil
		}
		if d.music != nil && v.Key == musicStopKey {
			d.music.command(music.Stop)
			return nil
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
	for _, state := range []proto.Message{
		&api.NumberStateResponse{Key: speakerVolumeKey, State: volume},
		&api.SwitchStateResponse{Key: speakerMuteKey, State: muted},
	} {
		if err := c.Send(state); err != nil {
			return err
		}
	}
	if d.announcements != nil {
		if err := c.Send(d.announcements.state()); err != nil {
			return err
		}
	}
	if d.ring != nil {
		for _, state := range d.ring.states() {
			if err := c.Send(state); err != nil {
				return err
			}
		}
	}
	if d.updates != nil {
		for _, state := range d.updates.states() {
			if err := c.Send(state); err != nil {
				return err
			}
		}
	}
	if d.music != nil {
		enabled, url, _ := d.music.snapshot()
		if err := c.Send(&api.SwitchStateResponse{Key: musicKey, State: enabled}); err != nil {
			return err
		}
		if err := c.Send(&api.TextStateResponse{Key: musicURLKey, State: url}); err != nil {
			return err
		}
	}
	if d.management != nil {
		for _, state := range d.management.states() {
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
		d.announcements.closed(c)
	}
	d.mu.Lock()
	delete(d.subscribers, c)
	d.mu.Unlock()
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
		workers.Go(func() { d.announcements.run(ctx) })
		workers.Go(func() {
			for {
				select {
				case <-ctx.Done():
					return
				case <-d.announcements.changed:
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
