package mobile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"

	"github.com/farooqu/encore-satellite/platforms/android/update"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"
)

// UpdateHost queues work on Android, never on the ESPHome connection thread.
type UpdateHost interface {
	RequestUpdate(channel string, generation int64, install bool)
}

const updateKey uint32 = 0x454e4303
const channelKey uint32 = 0x454e4304
const checkKey uint32 = 0x454e4305

func (r *Runtime) ConfigureUpdates(host UpdateHost, current string) {
	r.mu.Lock()
	r.updater = host
	r.updateState = &api.UpdateStateResponse{Key: updateKey, CurrentVersion: current, MissingState: true, Title: "Encore APK"}
	r.mu.Unlock()
}

func (r *Runtime) UpdateChannel() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.config.Channel
}

func (r *Runtime) UpdateGeneration() int64 {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.config.ChannelGeneration
}

// UpdateStatus reports failures as unknown, not a falsely up-to-date package.
func (r *Runtime) UpdateStatus(channel string, generation int64, latest, status string, known, busy bool) {
	r.mu.Lock()
	if r.config.Channel != channel || r.config.ChannelGeneration != generation || r.updateState == nil {
		r.mu.Unlock()
		return
	}
	r.updateState.LatestVersion = latest
	r.updateState.ReleaseSummary = status
	r.updateState.MissingState = !known
	r.updateState.InProgress = busy
	state := proto.Clone(r.updateState)
	control := r.control
	r.mu.Unlock()
	if control != nil {
		_ = control.Broadcast(state)
	}
}

// Updates is a narrow JNI wrapper around the platform's release verifier.
type Updates struct {
	mu     sync.Mutex
	client update.Client
}

func NewUpdates(base, key, appID, certificate, abi, directory, installedCore string, installed int64, sdk int) (*Updates, error) {
	public, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(public) != 32 {
		return nil, errors.New("update root not provisioned")
	}
	return &Updates{client: update.Client{BaseURL: base, Key: public, ApplicationID: appID, CertificateSHA256: certificate, ABI: abi, Directory: directory, InstalledCore: installedCore, Installed: installed, SDK: sdk}}, nil
}

// Fetch returns metadata as JSON; download=true also supplies its verified path.
func (u *Updates) Fetch(channel string, download bool) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	var release *update.Release
	var path string
	var err error
	if download {
		release, path, err = u.client.Download(context.Background(), channel)
	} else {
		release, err = u.client.Check(context.Background(), channel)
	}
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(map[string]any{"release": release, "path": path})
	return string(b), err
}
