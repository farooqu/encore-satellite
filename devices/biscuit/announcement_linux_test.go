package biscuit

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"

	"github.com/farooqu/encore-satellite/backends/bluetooth"
	"github.com/farooqu/encore-satellite/backends/esphome/announcements"
	"github.com/farooqu/encore-satellite/backends/incubator"
	"github.com/farooqu/encore-satellite/core/session"
)

type announcementSink struct {
	mu      sync.Mutex
	samples []int16
	drain   chan struct{}
	release chan error
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
	case err := <-p.release:
		return err
	}
}

type announcementLogs struct {
	lines chan string
	leak  atomic.Bool
}

func (l *announcementLogs) Write(p []byte) (int, error) {
	line := string(p)
	if strings.Contains(line, "fixture-private-token") {
		l.leak.Store(true)
	}
	if strings.Contains(line, "esphome: announcement revision=") {
		l.lines <- line
	}
	return len(p), nil
}

func TestAnnouncementsEncryptedAPIHTTPPCMAndIncubatorLifecycle(t *testing.T) {
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	logs, previous := &announcementLogs{lines: make(chan string, 16)}, log.Writer()
	log.SetOutput(logs)
	t.Cleanup(func() {
		log.SetOutput(previous)
		if logs.leak.Load() {
			t.Error("announcement logged sensitive input")
		}
	})
	receipt := func(want string) {
		t.Helper()
		select {
		case line := <-logs.lines:
			_, fields, _ := strings.Cut(line, " outcome=")
			if strings.TrimSpace(fields) != want {
				t.Fatal("wrong announcement receipt; wanted", want)
			}
		case <-time.After(time.Second):
			t.Fatal("missing announcement receipt", want)
		}
	}
	p := &announcementSink{drain: make(chan struct{}, 1), release: make(chan error)}
	engine := session.New(p)
	player := newSpeaker(&speakerPCM{})
	defer player.Close()
	engine.SetActivityHandler(player.SetVoiceActive)
	config := Config{Schema: 1, Name: "announcement-test"}
	d := &biscuitControls{
		Proxy:         bluetooth.New(managementRadio{}, bluetooth.Options{}),
		player:        player,
		music:         newMusic(config, &settings{path: filepath.Join(t.TempDir(), "config.json"), config: config}, player),
		announcements: announcements.New(engine),
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
	fixed := slices.Clone(wav)
	binary.LittleEndian.PutUint32(fixed[4:], uint32(len(fixed)-8))
	binary.LittleEndian.PutUint32(fixed[40:], uint32(len(fixed)-44))
	// Match the direct attempt's lengths, with synthetic audio after 2s silence.
	full := make([]byte, 640078)
	copy(full, wav[:36])
	binary.LittleEndian.PutUint32(full[4:], uint32(len(full)-8))
	copy(full[36:], "JUNK\x1a\x00\x00\x00")
	copy(full[70:], "data")
	binary.LittleEndian.PutUint32(full[74:], 640000)
	full[78+64000] = 37
	stalled, interrupted := make(chan struct{}, 1), make(chan struct{}, 1)
	allowTruncatedEOF := make(chan struct{})
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/stalled.wav":
			w.Write(wav[:44])
			w.(http.Flusher).Flush()
			stalled <- struct{}{}
			<-r.Context().Done()
			interrupted <- struct{}{}
			return
		case "/truncated.wav":
			// One whole block arrives and the second is one byte short.
			short := append(slices.Clone(wav[:44]), make([]byte, 1279)...)
			binary.LittleEndian.PutUint32(short[4:], 36+1280)
			binary.LittleEndian.PutUint32(short[40:], 1280)
			w.Write(short)
			w.(http.Flusher).Flush()
			select {
			case <-allowTruncatedEOF:
			case <-r.Context().Done():
			}
			return
		case "/unterminated.wav":
			conn, rw, err := w.(http.Hijacker).Hijack()
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.Close()
			// All declared PCM is present, but chunked transfer never ends cleanly.
			fmt.Fprintf(rw, "HTTP/1.1 200 OK\r\nTransfer-Encoding: chunked\r\n\r\n%x\r\n%s\r\n", len(fixed), fixed)
			if err := rw.Flush(); err != nil {
				t.Error(err)
			}
			return
		case "/trailing.wav":
			w.Write(fixed[:4])
			var riffSize [4]byte
			const tail = "JUNK\x04\x00\x00\x00tail"
			binary.LittleEndian.PutUint32(riffSize[:], uint32(len(fixed)+len(tail)-8))
			w.Write(riffSize[:])
			w.Write(fixed[8:])
			w.(http.Flusher).Flush()
			io.WriteString(w, tail)
			return
		case "/excess-trailing.wav":
			w.Write(fixed)
			w.Write(make([]byte, 64*1024+1))
			return
		case "/bad-redirect.wav":
			http.Redirect(w, r, "file:///unavailable?fixture-private-token", http.StatusFound)
			return
		case "/full.wav":
			w.Header().Set("Content-Length", "640078")
			w.Write(full)
			return
		}
		w.Write(wav)
	}))
	defer func() { cancel(); httpServer.Close() }()
	play := func(path string) {
		peer.send(&api.MediaPlayerCommandRequest{Key: entity.Key, HasMediaUrl: true, MediaUrl: httpServer.URL + path + "?signature=fixture-private-token", HasAnnouncement: true, Announcement: true})
	}
	play("/response.wav")
	event(0x09, "announcing")
	<-p.drain
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_PLAYING)
	if engine.Snapshot().Output.State != "draining" {
		t.Fatal("physical drain not retained")
	}
	p.release <- nil
	event(0x09, "announced")
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_IDLE)
	receipt("completed accepted_blocks=1 accepted_samples=7 drain=ok")
	p.mu.Lock()
	if !slices.Equal(p.samples, want) {
		t.Error("wrong decoded response", p.samples)
	}
	p.mu.Unlock()

	play("/full.wav")
	event(0x09, "announcing")
	<-p.drain
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_PLAYING)
	p.release <- nil
	event(0x09, "announced")
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_IDLE)
	receipt("completed accepted_blocks=1000 accepted_samples=320000 drain=ok")

	play("/bad-redirect.wav")
	receipt("http_failed accepted_blocks=0 accepted_samples=0 drain=not_attempted")
	play("/truncated.wav")
	event(0x09, "announcing")
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_PLAYING)
	close(allowTruncatedEOF)
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_IDLE)
	receipt("wav_incomplete accepted_blocks=1 accepted_samples=320 drain=not_attempted")
	play("/unterminated.wav")
	event(0x09, "announcing")
	receipt("wav_incomplete accepted_blocks=1 accepted_samples=7 drain=not_attempted")
	play("/excess-trailing.wav")
	event(0x09, "announcing")
	receipt("wav_failed accepted_blocks=1 accepted_samples=7 drain=not_attempted")
	play("/trailing.wav")
	event(0x09, "announcing")
	<-p.drain
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_PLAYING)
	p.release <- nil
	event(0x09, "announced")
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_IDLE)
	receipt("completed accepted_blocks=1 accepted_samples=7 drain=ok")
	play("/response.wav")
	event(0x09, "announcing")
	<-p.drain
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_PLAYING)
	p.release <- errors.New("fixture-private-token")
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_IDLE)
	receipt("drain_failed accepted_blocks=1 accepted_samples=7 drain=failed")

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
		if privacy {
			receipt("mute_fence accepted_blocks=0 accepted_samples=0 drain=not_attempted")
		} else {
			receipt("ha_stop accepted_blocks=0 accepted_samples=0 drain=not_attempted")
		}
		engine.SetMuted(false)
	}
	play("/response.wav") // No poisoned lane after either cancelled fetch.
	event(0x09, "announcing")
	<-p.drain
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_PLAYING)
	p.release <- nil
	event(0x09, "announced")
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_IDLE)
	receipt("completed accepted_blocks=1 accepted_samples=7 drain=ok")

	play("/stalled.wav")
	<-stalled
	play("/response.wav") // Replacing a URL cancels the old fetch, not its successor.
	select {
	case <-interrupted:
	case <-time.After(time.Second):
		t.Fatal("replacement did not cancel the old HTTP request")
	}
	receipt("ha_replaced accepted_blocks=0 accepted_samples=0 drain=not_attempted")
	event(0x09, "announcing")
	<-p.drain
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_PLAYING)
	p.release <- nil
	event(0x09, "announced")
	state(api.MediaPlayerState_MEDIA_PLAYER_STATE_IDLE)
	receipt("completed accepted_blocks=1 accepted_samples=7 drain=ok")

	play("/stalled.wav")
	<-stalled
	peer.conn.Close()
	select {
	case <-interrupted:
	case <-time.After(time.Second):
		t.Fatal("owning HA connection loss did not cancel blocked HTTP")
	}
	receipt("ha_disconnected accepted_blocks=0 accepted_samples=0 drain=not_attempted")
}
