package biscuit

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"sync"
	"testing"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"

	"github.com/farooqu/encore-satellite/backends/bluetooth"
	"github.com/farooqu/encore-satellite/backends/incubator"
	"github.com/farooqu/encore-satellite/core/session"
)

type announcementSink struct {
	mu      sync.Mutex
	samples []int16
	drain   chan struct{}
	release chan struct{}
}

func (p *announcementSink) Write(ctx context.Context, samples []int16) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	p.mu.Lock()
	p.samples = append(p.samples, samples...)
	p.mu.Unlock()
	return nil
}
func (p *announcementSink) Flush() {}
func (p *announcementSink) Drain(ctx context.Context) error {
	p.drain <- struct{}{}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.release:
		return nil
	}
}

func TestAnnouncementsEncryptedAPIHTTPPCMAndIncubatorLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	p := &announcementSink{drain: make(chan struct{}, 1), release: make(chan struct{})}
	engine := session.New(p)
	player := newSpeaker(&speakerPCM{})
	defer player.Close()
	engine.SetActivityHandler(player.SetVoiceActive)
	config := Config{Schema: 1, Name: "announcement-test"}
	d := &biscuitControls{
		Proxy:         bluetooth.New(managementRadio{}, bluetooth.Options{}),
		player:        player,
		music:         newMusic(config, &settings{path: filepath.Join(t.TempDir(), "config.json"), config: config}, player),
		announcements: newAnnouncementPlayer(engine, player),
	}
	key, err := esphome.GeneratePSK()
	if err != nil {
		t.Fatal(err)
	}
	l, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- d.Serve(ctx, l, esphome.Info{Name: config.Name, MACAddress: "02:00:00:00:00:75"}, key) }()
	t.Cleanup(func() { cancel(); l.Close(); <-done })
	peer := newManagementPeer(t, l.Addr().String(), key)
	peer.send(&api.HelloRequest{ClientInfo: "announcement-test", ApiVersionMajor: 1, ApiVersionMinor: 12})
	peer.receive()
	peer.send(&api.ListEntitiesRequest{})
	var entity *api.ListEntitiesMediaPlayerResponse
listing:
	for {
		switch message := peer.receive().(type) {
		case *api.ListEntitiesMediaPlayerResponse:
			if entity != nil {
				t.Fatal("multiple announcement media players")
			}
			entity = message
		case *api.ListEntitiesDoneResponse:
			break listing
		}
	}
	wantFeatures := uint32(esphome.MediaPlayerFeaturePlayMedia | esphome.MediaPlayerFeatureAnnounce | esphome.MediaPlayerFeatureStop)
	if entity == nil || entity.ObjectId != "announcements" || entity.DisabledByDefault || entity.DeviceId != 0 || entity.FeatureFlags != wantFeatures || len(entity.SupportedFormats) != 2 {
		t.Fatal("missing or untruthful entity discovery", entity)
	}
	for _, format := range entity.SupportedFormats {
		if format.Format != "wav" || format.SampleRate != 16000 || format.NumChannels != 1 || format.SampleBytes != 2 {
			t.Fatal("HA proxy would choose the wrong output format", format)
		}
	}
	peer.send(&api.SubscribeStatesRequest{})
	state := func(want api.MediaPlayerState) {
		t.Helper()
		for {
			if message, ok := peer.receive().(*api.MediaPlayerStateResponse); ok && message.Key == entity.Key && message.State == want {
				return
			}
		}
	}
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_IDLE)

	// A real Incubator socket must receive device events, not an invented HA
	// ANNOUNCING state (which HA's ESPHome state mapper does not support).
	voiceListener, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	voiceDone := make(chan error, 1)
	voice := &incubator.Server{Engine: engine, Name: config.Name, MAC: "02:00:00:00:00:75"}
	go func() { voiceDone <- voice.Serve(ctx, voiceListener) }()
	t.Cleanup(func() { cancel(); voiceListener.Close(); <-voiceDone })
	native, err := net.Dial("tcp4", voiceListener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer native.Close()
	event := func(kind byte, want string) {
		t.Helper()
		native.SetReadDeadline(time.Now().Add(3 * time.Second))
		var header [3]byte
		if _, err := io.ReadFull(native, header[:]); err != nil {
			t.Fatal(err)
		}
		body := make([]byte, binary.BigEndian.Uint16(header[1:]))
		if _, err := io.ReadFull(native, body); err != nil {
			t.Fatal(err)
		}
		var value map[string]any
		if header[0] != kind || json.Unmarshal(body, &value) != nil || (want != "" && value["type"] != want) {
			t.Fatalf("unexpected native frame %x %s", header[0], body)
		}
	}
	event(0x01, "")
	want := []int16{0, 32767, -32768, 1937, -591, 0x1234, -77}
	wav := make([]byte, 44+2*len(want))
	copy(wav, "RIFF")
	binary.LittleEndian.PutUint32(wav[4:], 0xffffffff)
	copy(wav[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(wav[16:], 16)
	binary.LittleEndian.PutUint16(wav[20:], 1)
	binary.LittleEndian.PutUint16(wav[22:], 1)
	binary.LittleEndian.PutUint32(wav[24:], 16000)
	binary.LittleEndian.PutUint32(wav[28:], 32000)
	binary.LittleEndian.PutUint16(wav[32:], 2)
	binary.LittleEndian.PutUint16(wav[34:], 16)
	copy(wav[36:], "data")
	binary.LittleEndian.PutUint32(wav[40:], 0xffffffff)
	for i, sample := range want {
		binary.LittleEndian.PutUint16(wav[44+2*i:], uint16(sample))
	}
	stalled, interrupted := make(chan struct{}, 1), make(chan struct{}, 1)
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/stalled.wav" {
			w.Write(wav[:44])
			w.(http.Flusher).Flush()
			stalled <- struct{}{}
			<-r.Context().Done()
			interrupted <- struct{}{}
			return
		}
		w.Write(wav)
	}))
	defer func() { cancel(); httpServer.Close() }()
	play := func(path string) {
		peer.send(&api.MediaPlayerCommandRequest{Key: entity.Key, HasMediaUrl: true, MediaUrl: httpServer.URL + path, HasAnnouncement: true, Announcement: true})
	}
	play("/response.wav")
	event(0x09, "announcing")
	<-p.drain
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_PLAYING)
	if engine.Snapshot().Output.State != "draining" {
		t.Fatal("physical drain not retained")
	}
	p.release <- struct{}{}
	event(0x09, "announced")
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_IDLE)
	p.mu.Lock()
	if !slices.Equal(p.samples, want) {
		t.Error("wrong decoded response", p.samples)
	}
	p.mu.Unlock()

	for _, privacy := range []bool{false, true} {
		play("/stalled.wav")
		<-stalled
		if privacy {
			engine.SetMuted(true)
		} else {
			peer.send(&api.MediaPlayerCommandRequest{Key: entity.Key, HasCommand: true, Command: api.MediaPlayerCommand_MEDIA_PLAYER_COMMAND_STOP})
		}
		select {
		case <-interrupted:
		case <-time.After(time.Second):
			t.Fatal("Stop/privacy did not cancel blocked HTTP")
		}
		state(api.MediaPlayerState_MEDIA_PLAYER_STATE_IDLE)
		engine.SetMuted(false)
	}
	play("/response.wav") // No poisoned lane after either cancelled fetch.
	event(0x09, "announcing")
	<-p.drain
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_PLAYING)
	p.release <- struct{}{}
	event(0x09, "announced")
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_IDLE)

	play("/stalled.wav")
	<-stalled
	play("/response.wav") // Replacing a URL cancels the old fetch, not its successor.
	select {
	case <-interrupted:
	case <-time.After(time.Second):
		t.Fatal("replacement did not cancel the old HTTP request")
	}
	event(0x09, "announcing")
	<-p.drain
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_PLAYING)
	p.release <- struct{}{}
	event(0x09, "announced")
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_IDLE)

	play("/stalled.wav")
	<-stalled
	peer.conn.Close()
	select {
	case <-interrupted:
	case <-time.After(time.Second):
		t.Fatal("owning HA connection loss did not cancel blocked HTTP")
	}
}
