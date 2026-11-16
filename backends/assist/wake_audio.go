package assist

import (
	"errors"
	"slices"
	"sort"

	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/encoding/protowire"

	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/core/session"
)

// Experimental compatibility: HA 2026.12.0.dev0 at 1395798e2338bd46ee522539f7281b336c44e3b8,
// aioesphomeapi 46.30.0. The pinned Go schema predates these fields. Unknown
// protobuf fields retain their normal wire encoding; no dependency fork needed.
const (
	featureWakeAudio   esphome.VoiceFeature    = 1 << 7
	subscribeWakeAudio uint32                  = 1 << 3
	eventWakeRejected  api.VoiceAssistantEvent = 13
)

func (s *Server) wakeConfiguration() *api.VoiceAssistantConfigurationResponse {
	s.mu.Lock()
	defer s.mu.Unlock()
	config := &api.VoiceAssistantConfigurationResponse{ActiveWakeWords: slices.Clone(s.activeWakeWords)}
	ids := []string{}
	for id := range s.ExperimentalWakeWords {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	sort.Strings(config.ActiveWakeWords)
	for _, id := range ids {
		config.AvailableWakeWords = append(config.AvailableWakeWords, &api.VoiceAssistantWakeWord{Id: id, WakeWord: s.ExperimentalWakeWords[id]})
	}
	config.MaxActiveWakeWords = uint32(len(ids))
	return config
}

func (s *Server) setWakeWords(ids []string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i, id := range ids {
		if _, ok := s.ExperimentalWakeWords[id]; !ok || slices.Contains(ids[:i], id) {
			return errors.New("assist: unsupported or duplicate active wake model")
		}
	}
	s.activeWakeWords = slices.Clone(ids)
	return nil
}

func (s *Server) startRequest(command session.Command, negotiated bool) (*api.VoiceAssistantRequest, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id := command.Detection.Word
	phrase := id
	if id == "manual" {
		phrase = ""
	} else if configured, ok := s.ExperimentalWakeWords[id]; ok {
		phrase = configured
	}
	request := &api.VoiceAssistantRequest{Start: true, WakeWordPhrase: phrase, AudioSettings: &api.VoiceAssistantAudioSettings{VolumeMultiplier: 1}}
	if phrase != "" && len(s.ExperimentalWakeWords) > 0 && !slices.Contains(s.activeWakeWords, id) {
		return nil, errors.New("assist: inactive wake model")
	}
	if !negotiated || phrase == "" {
		return request, nil
	}
	if _, ok := s.ExperimentalWakeWords[id]; !ok || command.PreRollBytes <= 0 || command.PreRollBytes%32 != 0 || command.PreRollBytes > audio.PreRollSamples*2 {
		return nil, errors.New("assist: negotiated wake audio needs a configured model and whole milliseconds of bounded pre-roll")
	}
	fields := protowire.AppendTag(nil, 6, protowire.BytesType)
	fields = protowire.AppendString(fields, id)
	fields = protowire.AppendTag(fields, 7, protowire.VarintType)
	fields = protowire.AppendVarint(fields, uint64(command.PreRollBytes))
	// Detection's protocol-2 uint8 scores are not HA float probabilities. Do not
	// fabricate them or a second audio channel. Bytes describe actual queued PCM.
	request.ProtoReflect().SetUnknown(fields)
	return request, nil
}
