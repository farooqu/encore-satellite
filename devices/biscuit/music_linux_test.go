package biscuit

import (
	"context"
	"errors"
	"math"
	"net"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/backends/bluetooth"
	"github.com/farooqu/encore-satellite/backends/esphome/sensors"
	"github.com/farooqu/encore-satellite/backends/incubator"
	"github.com/farooqu/encore-satellite/core/music"
	"github.com/farooqu/encore-satellite/core/session"
	"github.com/farooqu/encore-satellite/platforms/linux"
	"github.com/farooqu/encore-satellite/platforms/linuxsendspin"
	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
)

func TestMicrophoneMutedEncryptedAPI(t *testing.T) {
	var driver atomic.Int32
	driver.Store(1)
	gate := linux.NewPrivacy(func() (bool, error) {
		switch driver.Load() {
		case 0:
			return false, nil
		case 1:
			return true, nil
		default:
			return false, errors.New("privacy unreadable")
		}
	})
	p, _ := buttonMusic(t)
	muted, _ := gate.Check(nil)
	d := &biscuitControls{Proxy: bluetooth.New(managementRadio{}, bluetooth.Options{}), player: p, microphone: sensors.NewMicrophone(muted)}
	key, err := esphome.GeneratePSK()
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx, l, esphome.Info{Name: "privacy-test"}, key) }()
	t.Cleanup(func() { cancel(); l.Close(); <-done })
	peer := newManagementPeer(t, l.Addr().String(), key)
	peer.send(&api.ListEntitiesRequest{})
	var mic, speaker uint32
listing:
	for {
		switch v := peer.receive().(type) {
		case *api.ListEntitiesBinarySensorResponse:
			if v.ObjectId == "microphone_muted" {
				mic = v.Key
			}
		case *api.ListEntitiesSwitchResponse:
			if v.ObjectId == "speaker_mute" {
				speaker = v.Key
			}
		case *api.ListEntitiesDoneResponse:
			break listing
		}
	}
	if mic == 0 || speaker == 0 || mic == speaker {
		t.Fatal("missing separate read-only microphone and speaker entities")
	}
	lastSpeaker := make(map[*managementPeer]*api.SwitchStateResponse)
	state := func(peer *managementPeer, wantMic, wantSpeaker bool) {
		t.Helper()
		for {
			switch v := peer.receive().(type) {
			case *api.SwitchStateResponse:
				if v.Key == speaker {
					lastSpeaker[peer] = v
				}
			case *api.BinarySensorStateResponse:
				speakerState := lastSpeaker[peer]
				if v.Key == mic && v.State == wantMic && !v.MissingState && speakerState != nil && speakerState.State == wantSpeaker && !speakerState.MissingState {
					return
				}
			}
		}
	}
	peer.send(&api.SubscribeStatesRequest{})
	state(peer, true, false)
	captureDone := make(chan error, 1)
	go func() {
		voice := &incubator.Server{Engine: session.New(p)}
		captureDone <- capture(ctx, &captureStub{}, func() (bool, error) { return false, nil }, gate, voice, &captureMeter{origin: time.Now()}, func() {}, d.microphone.SetMuted)
	}()
	t.Cleanup(func() {
		cancel()
		if err := <-captureDone; err != nil {
			t.Error(err)
		}
	})
	driver.Store(0)
	state(peer, false, false)
	peer.send(&api.SwitchCommandRequest{Key: speaker, State: true})
	peer.send(&api.SubscribeStatesRequest{})
	state(peer, false, true)
	driver.Store(1)
	state(peer, true, true)
	peer.send(&api.SwitchCommandRequest{Key: speaker, State: false})
	peer.send(&api.SubscribeStatesRequest{})
	state(peer, true, false)
	driver.Store(0)
	state(peer, false, false)
	driver.Store(-1)
	state(peer, true, false)
	other := newManagementPeer(t, l.Addr().String(), key)
	other.send(&api.SubscribeStatesRequest{})
	state(other, true, false)
	driver.Store(0)
	state(other, false, false)
}

func TestNativeVolumeEncryptedAPIAndPhysicalKeys(t *testing.T) {
	p, sample := buttonMusic(t)
	c := Config{Schema: 1, Name: "volume-test"}
	d := &biscuitControls{Proxy: bluetooth.New(managementRadio{}, bluetooth.Options{}), player: p, music: newMusic(c, &settings{config: c}, p)}
	key, err := esphome.GeneratePSK()
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx, l, esphome.Info{Name: c.Name}, key) }()
	t.Cleanup(func() { cancel(); l.Close(); <-done })
	peer := newManagementPeer(t, l.Addr().String(), key)
	peer.send(&api.HelloRequest{ClientInfo: "volume-test", ApiVersionMajor: 1, ApiVersionMinor: 12})
	peer.receive()
	peer.send(&api.ListEntitiesRequest{})
	var volume, mute uint32
listing:
	for {
		switch v := peer.receive().(type) {
		case *api.ListEntitiesNumberResponse:
			if v.ObjectId == "speaker_volume" {
				volume = v.Key
				if v.MinValue != 0 || v.MaxValue != 100 || math.Abs(float64(v.Step)-100.0/120) > .00001 || v.UnitOfMeasurement != "%" {
					t.Fatal("native volume range must cover silence to software unity", v)
				}
			}
		case *api.ListEntitiesSwitchResponse:
			if v.ObjectId == "speaker_mute" {
				mute = v.Key
			}
		case *api.ListEntitiesDoneResponse:
			break listing
		}
	}
	if volume == 0 || mute == 0 || mute == volume {
		t.Fatal("missing native entities")
	}
	// Each volume snapshot precedes its mute state. Drain through a complete
	// matching pair, including any coalesced earlier notifications.
	state := func(want float32, muted bool) {
		t.Helper()
		matched := false
		for {
			switch v := peer.receive().(type) {
			case *api.NumberStateResponse:
				if v.Key == volume {
					matched = math.Abs(float64(v.State-want)) < .00001 && !v.MissingState
				}
			case *api.SwitchStateResponse:
				if v.Key == mute && matched && v.State == muted && !v.MissingState {
					return
				}
			}
		}
	}
	peer.send(&api.SubscribeStatesRequest{})
	state(50, false)
	peer.send(&api.NumberCommandRequest{Key: volume, State: 75})
	state(75, false)
	buttonWait(t, "API capped gain at startup instead of permitting 75%", func() bool { return sample() == 9000 })
	peer.send(&api.NumberCommandRequest{Key: volume, State: 12.5})
	state(12.5, false)
	buttonWait(t, "API did not change native PCM with Sendspin disabled", func() bool { return sample() == 1500 })
	peer.send(&api.SwitchCommandRequest{Key: mute, State: true})
	state(12.5, true)
	buttonWait(t, "output mute did not silence PCM", func() bool { return sample() == 0 })
	peer.send(&api.NumberCommandRequest{Key: volume, State: 20})
	state(20, true)
	if sample() != 0 {
		t.Fatal("volume change released output mute")
	}
	peer.send(&api.SwitchCommandRequest{Key: mute, State: false})
	state(20, false)
	buttonWait(t, "unmute did not restore selected gain", func() bool { return sample() == 2400 })
	e := session.New(p)
	keys := buttonEvents(t, linux.NewPrivacy(func() (bool, error) { return false, nil }), e, p, nil)
	keys(114, 1)
	keys(114, 0)
	state(100.0*23/120, false)
	buttonWait(t, "physical minus diverged from API state", func() bool { return sample() == 2300 })
	peer.send(&api.SwitchCommandRequest{Key: mute, State: true})
	state(100.0*23/120, true)
	keys(115, 1)
	keys(115, 0)
	state(20, false)
	buttonWait(t, "physical plus did not unmute shared gain", func() bool { return sample() == 2400 })
	if e.Snapshot().Muted {
		t.Fatal("speaker mute altered microphone privacy")
	}
	// A fresh subscriber must observe the current state, not startup defaults.
	other := newManagementPeer(t, l.Addr().String(), key)
	other.send(&api.SubscribeStatesRequest{})
	peer = other
	state(20, false)
	// Output controls cannot undo the real privacy key's immediate inhibition,
	// nor the music Stop latch that survives release of microphone privacy.
	keys(113, 1)
	keys(113, 0)
	buttonWait(t, "physical privacy did not inhibit music", func() bool { return e.Snapshot().Muted && sample() == 0 })
	peer.send(&api.NumberCommandRequest{Key: volume, State: 100})
	state(100, false)
	peer.send(&api.SwitchCommandRequest{Key: mute, State: false})
	state(100, false)
	keys(115, 1)
	keys(115, 0)
	state(100, false)
	if !e.Snapshot().Muted || sample() != 0 {
		t.Fatal("output controls released microphone privacy")
	}
	p.SetMusicPrivacy(false)
	time.Sleep(30 * time.Millisecond)
	if sample() != 0 {
		t.Fatal("output controls released privacy's music Stop latch")
	}
}

func TestNativeVolumeRangeAndConcurrentGainState(t *testing.T) {
	p, sample := buttonMusic(t)
	for _, invalid := range []float32{-0.001, 100.001, float32(math.NaN()), float32(math.Inf(1)), float32(math.Inf(-1))} {
		if err := p.setOutputVolume(invalid); err == nil {
			t.Fatalf("accepted invalid volume %v", invalid)
		}
		if volume, muted := p.outputState(); volume != 50 || muted {
			t.Fatal("rejected request changed state", volume, muted)
		}
	}
	for _, tc := range []struct {
		request, percent float32
		sample           int16
	}{{0, 0, 0}, {12.9, 12.5, 1500}, {12.95, 100.0 * 16 / 120, 1600}, {25, 25, 3000}, {50, 50, 6000}, {100.0 * 61 / 120, 100.0 * 61 / 120, 6100}, {75, 75, 9000}, {99.9, 100, 12000}, {100, 100, 12000}} {
		if err := p.setOutputVolume(tc.request); err != nil {
			t.Fatal(err)
		}
		if got, muted := p.outputState(); math.Abs(float64(got-tc.percent)) > .00001 || muted {
			t.Fatal("wrong applied/quantized state", got, muted)
		}
		buttonWait(t, "range/rounding did not match PCM", func() bool { return sample() == tc.sample })
	}
	// All intermediate values remain away from clamping. Lost adjustments must
	// not disappear behind a final absolute write that makes the test pass.
	for range 30 {
		if err := p.setOutputVolume(12.5); err != nil {
			t.Fatal(err)
		}
		var writers sync.WaitGroup
		for i := range 10 {
			writers.Go(func() { p.adjust(int32(1 - 2*(i%2))) })
		}
		writers.Wait()
		if got, muted := p.outputState(); got != 12.5 || muted {
			t.Fatal("concurrent keys lost an adjustment", got, muted)
		}
		buttonWait(t, "concurrent key state/gain diverged", func() bool { return sample() == 1500 })
	}
	// Race absolute API writes, keys, mute and readers. Once quiescent, the
	// published pair must describe actual PCM, regardless of which writer won.
	for range 30 {
		var writers sync.WaitGroup
		for i := range 8 {
			writers.Go(func() {
				switch i % 4 {
				case 0:
					if err := p.setOutputVolume(float32(5 + i)); err != nil {
						t.Error(err)
					}
				case 1:
					p.adjust(-1)
				case 2:
					p.setOutputMute(i == 2)
				case 3:
					p.outputState()
				}
			})
		}
		writers.Wait()
		volume, muted := p.outputState()
		want := int16(math.Round(float64(volume) * 120))
		if muted {
			want = 0
		}
		buttonWait(t, "concurrent API/key/mute state disagreed with PCM", func() bool { return sample() == want })
	}
}

func TestMusicSwitchLifecycleAndIndependentPersistence(t *testing.T) {
	c := Config{Schema: 1, Name: "test-dot"}
	s := &settings{path: filepath.Join(t.TempDir(), "config.json"), config: c}
	if err := SaveConfig(s.path, c); err != nil {
		t.Fatal(err)
	}
	player := newSpeaker(&speakerPCM{})
	defer player.Close()
	m := newMusic(c, s, player)
	started := make(chan string, 8)
	var live, maximum atomic.Int32
	m.run = func(ctx context.Context, o linuxsendspin.Options) error {
		n := live.Add(1)
		if n > maximum.Load() {
			maximum.Store(n)
		}
		defer live.Add(-1)
		started <- o.URL
		<-ctx.Done()
		return ctx.Err()
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { defer close(done); m.Run(ctx) }()
	defer func() { cancel(); <-done }()
	select {
	case <-started:
		t.Fatal("default off started music")
	case <-time.After(15 * time.Millisecond):
	}
	yes, no := true, false
	if err := m.configure(&yes, nil); err == nil {
		t.Fatal("enabled music without server configuration")
	}
	url := "ws://ma.local:8927/sendspin"
	if err := m.configure(nil, &url); err != nil {
		t.Fatal(err)
	}
	var writes sync.WaitGroup
	writes.Go(func() {
		if err := s.update(func(c *Config) { c.BluetoothProxy = true }); err != nil {
			t.Error(err)
		}
	})
	writes.Go(func() {
		if err := m.configure(&yes, nil); err != nil {
			t.Error(err)
		}
	})
	writes.Wait()
	select {
	case got := <-started:
		if got != url {
			t.Fatal("wrong endpoint")
		}
	case <-time.After(time.Second):
		t.Fatal("music did not start")
	}
	got, err := LoadConfig(s.path)
	if err != nil || !got.BluetoothProxy || !got.Sendspin || got.SendspinURL != url || got.Name != c.Name {
		t.Fatalf("independent setting lost: %+v %v", got, err)
	}
	if err := m.configure(&no, nil); err != nil {
		t.Fatal(err)
	}
	until := time.Now().Add(time.Second)
	for live.Load() != 0 {
		if time.Now().After(until) {
			t.Fatal("off did not join child")
		}
		time.Sleep(time.Millisecond)
	}
	// OFF must stay off; commands from that period cannot turn into autoplay.
	m.command(music.Play)
	select {
	case <-m.commands:
		t.Fatal("queued Play while disabled")
	default:
	}
	select {
	case <-started:
		t.Fatal("off restarted receiver")
	case <-time.After(15 * time.Millisecond):
	}
	s.path = filepath.Join(t.TempDir(), "missing", "config.json")
	if err := m.configure(&yes, nil); err == nil {
		t.Fatal("accepted failed persistence")
	}
	if enabled, _, _ := m.snapshot(); enabled {
		t.Fatal("failed save changed live music switch")
	}
	if maximum.Load() != 1 {
		t.Fatalf("overlapping music children: %d", maximum.Load())
	}
}
