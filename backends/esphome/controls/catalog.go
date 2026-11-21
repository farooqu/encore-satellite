package controls

import (
	"github.com/farooqu/encore-satellite/core/audio"
	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
)

func key(id string) uint32 { return (&esphome.Base{ObjectID: id}).Key() }

func button(id, name, icon string, k uint32, category api.EntityCategory) Entity[*api.ButtonCommandRequest] {
	return Entity[*api.ButtonCommandRequest]{&api.ListEntitiesButtonResponse{ObjectId: id, Key: k, Name: name, Icon: icon, EntityCategory: category}}
}

// UpdateTitle identifies the software independently of its packaging format.
const UpdateTitle = "Encore Satellite"

func update(id string, k uint32) Entity[*api.UpdateCommandRequest] {
	return Entity[*api.UpdateCommandRequest]{&api.ListEntitiesUpdateResponse{ObjectId: id, Key: k, Name: "Firmware update", DeviceClass: "firmware", EntityCategory: esphome.CategoryConfig}}
}

func UpdateChannel(k uint32) Entity[*api.SelectCommandRequest] {
	return Entity[*api.SelectCommandRequest]{&api.ListEntitiesSelectResponse{ObjectId: "update_channel", Key: k, Name: "Update channel", Options: []string{"dev", "stable"}, EntityCategory: esphome.CategoryConfig}}
}

func CheckUpdates(k uint32) Entity[*api.ButtonCommandRequest] {
	return button("check_updates", "Check for updates", "", k, esphome.CategoryConfig)
}

func SpeakerVolume(max, step float32) Entity[*api.NumberCommandRequest] {
	return Entity[*api.NumberCommandRequest]{&api.ListEntitiesNumberResponse{ObjectId: "speaker_volume", Key: key("speaker_volume"), Name: "Speaker volume", Icon: "mdi:volume-high", MaxValue: max, Step: step, UnitOfMeasurement: "%"}}
}

func VoiceMode(options ...string) Entity[*api.SelectCommandRequest] {
	return Entity[*api.SelectCommandRequest]{&api.ListEntitiesSelectResponse{ObjectId: "voice_mode", Key: key("voice_mode"), Name: "Voice mode (restart required)", Options: options, EntityCategory: esphome.CategoryConfig}}
}

const (
	DashboardKey      uint32 = 0x454e4301
	AndroidUpdateKey  uint32 = 0x454e4303
	AndroidChannelKey uint32 = 0x454e4304
)

var (
	Dashboard       = Entity[*api.TextCommandRequest]{&api.ListEntitiesTextResponse{ObjectId: "dashboard_url", Key: DashboardKey, Name: "Dashboard URL", EntityCategory: esphome.CategoryConfig, MaxLength: 1024}}
	ReloadDashboard = button("reload_dashboard", "Reload dashboard", "", 0x454e4302, esphome.CategoryConfig)
	AndroidUpdate   = update("apk_update", AndroidUpdateKey)
	AndroidChannel  = UpdateChannel(AndroidChannelKey)
	AndroidCheck    = CheckUpdates(0x454e4305)
	LinuxUpdate     = update("linux_update", key("linux_update"))
	LinuxChannel    = UpdateChannel(key("update_channel"))
	LinuxCheck      = CheckUpdates(key("check_updates"))
	SpeakerMute     = Entity[*api.SwitchCommandRequest]{&api.ListEntitiesSwitchResponse{ObjectId: "speaker_mute", Key: key("speaker_mute"), Name: "Speaker mute", Icon: "mdi:volume-off"}}
	Sendspin        = Entity[*api.SwitchCommandRequest]{&api.ListEntitiesSwitchResponse{ObjectId: "sendspin", Key: key("sendspin"), Name: "Sendspin music", Icon: "mdi:music", EntityCategory: esphome.CategoryConfig}}
	SendspinURL     = Entity[*api.TextCommandRequest]{&api.ListEntitiesTextResponse{ObjectId: "sendspin_url", Key: key("sendspin_url"), Name: "Sendspin server", Icon: "mdi:server", EntityCategory: esphome.CategoryConfig, MaxLength: 255}}
	SendspinPlay    = button("sendspin_play", "Resume music", "mdi:play", key("sendspin_play"), 0)
	SendspinStop    = button("sendspin_stop", "Stop music", "mdi:stop", key("sendspin_stop"), 0)
	RingBrightness  = Entity[*api.NumberCommandRequest]{&api.ListEntitiesNumberResponse{ObjectId: "ring_brightness", Key: key("ring_brightness"), Name: "Ring brightness", Icon: "mdi:brightness-6", EntityCategory: esphome.CategoryConfig, MinValue: 1, MaxValue: 100, Step: 1, UnitOfMeasurement: "%"}}
	RingMotion      = Entity[*api.SwitchCommandRequest]{&api.ListEntitiesSwitchResponse{ObjectId: "ring_reduced_motion", Key: key("ring_reduced_motion"), Name: "Ring reduced motion", Icon: "mdi:animation-outline", EntityCategory: esphome.CategoryConfig}}
	ApplyVoiceMode  = button("apply_voice_mode", "Apply and restart", "mdi:restart", key("apply_voice_mode"), esphome.CategoryConfig)
	Announcements   = Entity[*api.MediaPlayerCommandRequest]{&api.ListEntitiesMediaPlayerResponse{
		ObjectId: "announcements", Key: key("announcements"), Name: "Announcements", Icon: "mdi:speaker-message",
		FeatureFlags: uint32(esphome.MediaPlayerFeaturePlayMedia | esphome.MediaPlayerFeatureAnnounce | esphome.MediaPlayerFeatureStop),
		SupportedFormats: []*api.MediaPlayerSupportedFormat{
			{Format: "wav", SampleRate: audio.SampleRate, NumChannels: 1, SampleBytes: 2},
			{Format: "wav", SampleRate: audio.SampleRate, NumChannels: 1, SampleBytes: 2, Purpose: api.MediaPlayerFormatPurpose_MEDIA_PLAYER_FORMAT_PURPOSE_ANNOUNCEMENT},
		},
	}}
)
