package mobile

import (
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
)

func TestAndroidESPHomeAnnouncementsUseVoiceOutputThroughDrain(t *testing.T) {
	const pcm = "\x00\x80\x34\x12\xff\x7f"
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		// Fixed-size PCM16, 16 kHz mono WAV with three asymmetric samples.
		_, _ = io.WriteString(w, "RIFF\x2a\x00\x00\x00WAVEfmt \x10\x00\x00\x00\x01\x00\x01\x00\x80\x3e\x00\x00\x00\x7d\x00\x00\x02\x00\x10\x00data\x06\x00\x00\x00"+pcm)
	}))
	t.Cleanup(fixture.Close)
	output := &announcementOutput{pcm: make(chan string, 1), draining: make(chan struct{}, 1),
		complete: make(chan struct{}), flushed: make(chan struct{}, 1)}
	r, err := Open(t.TempDir(), output)
	if err != nil {
		t.Fatal(err)
	}
	key := esphome.PSK{9, 27}
	r.config.Key = key.String()
	if err := r.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(r.Close)
	send, receive := noisePeer(t, "127.0.0.1:6053", key)
	send(&api.HelloRequest{ClientInfo: "announcement-test", ApiVersionMajor: 1, ApiVersionMinor: 12})
	receive()
	send(&api.ListEntitiesRequest{})
	var entity *api.ListEntitiesMediaPlayerResponse
listing:
	for {
		switch m := receive().(type) {
		case *api.ListEntitiesMediaPlayerResponse:
			if entity != nil {
				t.Fatal("unexpected second media player")
			}
			entity = m
		case *api.ListEntitiesDoneResponse:
			break listing
		}
	}
	wantFeatures := uint32(esphome.MediaPlayerFeaturePlayMedia | esphome.MediaPlayerFeatureAnnounce | esphome.MediaPlayerFeatureStop)
	if entity == nil || entity.ObjectId != "announcements" || entity.DisabledByDefault || entity.FeatureFlags != wantFeatures {
		t.Fatal("incorrect announcement discovery", entity)
	}
	// Wait for the first media player state, rather than skipping an incorrect
	// state and mistaking a later update for the expected transition.
	state := func(want api.MediaPlayerState) {
		t.Helper()
		for {
			if got, ok := receive().(*api.MediaPlayerStateResponse); ok {
				if got.Key != entity.Key || got.State != want {
					t.Fatalf("announcement state = %v, want %v", got, want)
				}
				return
			}
		}
	}
	const idle = api.MediaPlayerState_MEDIA_PLAYER_STATE_IDLE
	send(&api.SubscribeStatesRequest{})
	state(idle)
	for _, stop := range []bool{false, true} {
		send(&api.MediaPlayerCommandRequest{Key: entity.Key, HasMediaUrl: true, MediaUrl: fixture.URL, HasAnnouncement: true, Announcement: true})
		state(api.MediaPlayerState_MEDIA_PLAYER_STATE_PLAYING)
		if got := <-output.pcm; got != pcm {
			t.Fatalf("native PCM = % x, want % x", got, pcm)
		}
		select {
		case <-output.draining:
		case <-time.After(time.Second):
			t.Fatal("announcement never reached native drain")
		}
		if got := r.engine.Snapshot().Output; got.Owner != "announcement" || got.State != "draining" {
			t.Fatalf("lost voice output ownership during drain: %+v", got)
		}
		if stop {
			send(&api.MediaPlayerCommandRequest{Key: entity.Key, HasCommand: true, Command: api.MediaPlayerCommand_MEDIA_PLAYER_COMMAND_STOP})
		} else {
			select {
			case output.complete <- struct{}{}:
			case <-time.After(time.Second):
				t.Fatal("announcement no longer waiting for native drain")
			}
		}
		state(idle)
	}
}

type announcementOutput struct {
	pcm                         chan string
	draining, complete, flushed chan struct{}
}

func (o *announcementOutput) WriteVoice(pcm []byte, _ int64) bool {
	o.pcm <- string(pcm)
	return true
}

func (o *announcementOutput) DrainVoice(_ int64) bool {
	o.draining <- struct{}{}
	select {
	case <-o.complete:
		return true
	case <-o.flushed:
		return false
	case <-time.After(5 * time.Second):
		return false
	}
}

func (o *announcementOutput) FlushVoice(_ int64) {
	select {
	case o.flushed <- struct{}{}:
	default:
	}
}
