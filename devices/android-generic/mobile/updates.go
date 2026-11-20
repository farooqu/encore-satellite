package mobile

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"sync"

	"github.com/farooqu/encore-satellite/backends/esphome/controls"
	"github.com/farooqu/encore-satellite/platforms/android/update"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"
)

// UpdateHost queues work on Android, never on the ESPHome connection thread.
type UpdateHost interface {
	RequestUpdate(channel string, generation int64, install bool)
}

const updateKey = controls.AndroidUpdateKey
const channelKey = controls.AndroidChannelKey

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
	if r.config.Channel != channel || r.config.ChannelGeneration != generation || r.updateState == nil || r.updater == nil {
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
	ctx    context.Context
	cancel context.CancelFunc
}

func NewUpdates(base, key, appID, certificate, abi, directory, installedCore string, installed int64, sdk int) (*Updates, error) {
	public, err := base64.StdEncoding.DecodeString(key)
	if err != nil || len(public) != 32 {
		return nil, errors.New("update root not provisioned")
	}
	ctx, cancel := context.WithCancel(context.Background())
	return &Updates{ctx: ctx, cancel: cancel, client: update.Client{BaseURL: base, Key: public, ApplicationID: appID, CertificateSHA256: certificate, ABI: abi, Directory: directory, InstalledCore: installedCore, Installed: installed, SDK: sdk}}, nil
}

// Close cancels in-flight HTTP and permanently rejects further Fetch calls.
// It must not take mu: Fetch holds that lock while waiting for HTTP.
// A restarted Android worker creates a new Updates instance.
func (u *Updates) Close() { u.cancel() }

// Fetch returns metadata as JSON; download=true also supplies its verified path.
func (u *Updates) Fetch(channel string, download bool) (string, error) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if err := u.ctx.Err(); err != nil {
		return "", err
	}
	var release *update.Release
	var path string
	var err error
	if download {
		release, path, err = u.client.Download(u.ctx, channel)
	} else {
		release, err = u.client.Check(u.ctx, channel)
	}
	if err != nil {
		return "", err
	}
	if err := u.ctx.Err(); err != nil {
		return "", err
	}
	b, err := json.Marshal(map[string]any{"release": release, "path": path})
	return string(b), err
}
