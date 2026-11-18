package biscuit

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"sync"
	"time"

	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"

	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/core/session"
)

var announcementKey = (&esphome.Base{ObjectID: "announcements"}).Key()

type mediaRequest struct {
	url      string
	revision uint64
}

// announcementPlayer fetches HA's converted WAV on one bounded worker, using
// the engine's existing voice lane. It is independent of Sendspin music.
type announcementPlayer struct {
	engine   *session.Engine
	player   *speaker
	client   *http.Client
	requests chan mediaRequest
	changed  chan struct{}
	mu       sync.Mutex
	revision uint64
	owner    *esphome.Conn
	cancel   context.CancelFunc
	playing  bool
}

func newAnnouncementPlayer(engine *session.Engine, player *speaker) *announcementPlayer {
	return &announcementPlayer{engine: engine, player: player, client: &http.Client{Timeout: 3 * time.Minute}, requests: make(chan mediaRequest, 1), changed: make(chan struct{}, 1)}
}

func (*announcementPlayer) list(c *esphome.Conn) error {
	return c.Send(&api.ListEntitiesMediaPlayerResponse{
		Key: announcementKey, ObjectId: "announcements", Name: "Announcements", Icon: "mdi:speaker-message",
		FeatureFlags: uint32(esphome.MediaPlayerFeaturePlayMedia | esphome.MediaPlayerFeatureAnnounce | esphome.MediaPlayerFeatureStop),
		SupportedFormats: []*api.MediaPlayerSupportedFormat{
			{Format: "wav", SampleRate: audio.SampleRate, NumChannels: 1, SampleBytes: 2},
			{Format: "wav", SampleRate: audio.SampleRate, NumChannels: 1, SampleBytes: 2, Purpose: api.MediaPlayerFormatPurpose_MEDIA_PLAYER_FORMAT_PURPOSE_ANNOUNCEMENT},
		},
	})
}

func (m *announcementPlayer) state() *api.MediaPlayerStateResponse {
	m.mu.Lock()
	defer m.mu.Unlock()
	state := api.MediaPlayerState_MEDIA_PLAYER_STATE_IDLE
	if m.playing {
		state = api.MediaPlayerState_MEDIA_PLAYER_STATE_PLAYING
	}
	// HA does not map the protocol's ANNOUNCING state. Incubator receives its
	// own announcing/announced events from the engine, not a synthetic HA state.
	volume, _ := m.player.outputState()
	return &api.MediaPlayerStateResponse{Key: announcementKey, State: state, Volume: volume / 100}
}

func (m *announcementPlayer) signal() {
	select {
	case m.changed <- struct{}{}:
	default:
	}
}

func (m *announcementPlayer) stopLocked() {
	m.revision++
	if m.cancel != nil {
		m.cancel()
		_ = m.engine.StopAnnouncement()
	}
	select {
	case <-m.requests:
	default:
	}
}

func (m *announcementPlayer) command(c *esphome.Conn, cmd *api.MediaPlayerCommandRequest) error {
	if cmd.HasCommand && cmd.Command != api.MediaPlayerCommand_MEDIA_PLAYER_COMMAND_STOP {
		return errors.New("dot: unsupported announcement command")
	}
	if cmd.HasVolume {
		return errors.New("dot: announcement volume uses the shared speaker controls")
	}
	if cmd.HasMediaUrl {
		u, err := url.Parse(cmd.MediaUrl)
		if err != nil || len(cmd.MediaUrl) > 4096 || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" || u.User != nil || u.Fragment != "" || cmd.HasCommand {
			return errors.New("dot: announcement requires an HTTP(S) URL")
		}
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if cmd.HasCommand {
		m.stopLocked()
	}
	if cmd.HasMediaUrl {
		m.revision++
		m.owner = c
		if m.cancel != nil {
			m.cancel()
		}
		select {
		case <-m.requests:
		default:
		}
		m.requests <- mediaRequest{cmd.MediaUrl, m.revision}
	}
	return nil
}

func (m *announcementPlayer) closed(c *esphome.Conn) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.owner == c {
		m.stopLocked()
		m.owner = nil
	}
}

func (m *announcementPlayer) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
		case request := <-m.requests:
			child, cancel := context.WithCancel(ctx)
			m.mu.Lock()
			if request.revision != m.revision {
				m.mu.Unlock()
				cancel()
				continue
			}
			m.cancel = cancel
			m.mu.Unlock()
			err := m.engine.Announce(child, func(output context.Context, write func([]int16) error) error {
				req, err := http.NewRequestWithContext(output, http.MethodGet, request.url, nil)
				if err != nil {
					return err
				}
				response, err := m.client.Do(req)
				if err != nil {
					return err
				}
				defer response.Body.Close()
				if response.StatusCode != http.StatusOK {
					return fmt.Errorf("dot: announcement HTTP status %d", response.StatusCode)
				}
				return audio.ReadWAV(response.Body, func(samples []int16) error {
					if err := write(samples); err != nil {
						return err
					}
					m.mu.Lock()
					changed := request.revision == m.revision && child.Err() == nil && !m.playing
					if changed {
						m.playing = true
					}
					m.mu.Unlock()
					if changed {
						m.signal()
					}
					return nil
				})
			})
			cancel()
			m.mu.Lock()
			m.cancel, m.playing = nil, false
			m.mu.Unlock()
			m.signal()
			if err != nil && !errors.Is(err, context.Canceled) {
				// HTTP errors can embed signed URLs. Never log them or speech data.
				log.Print("Announcement failed or output was busy; no completed playback claimed")
			}
		}
	}
}
