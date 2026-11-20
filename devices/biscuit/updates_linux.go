package biscuit

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/farooqu/encore-satellite/backends/esphome/controls"
	"github.com/farooqu/encore-satellite/devices/biscuit/internal/nativeab"
	"github.com/farooqu/encore-satellite/platforms/linuxupdate"
	"github.com/ygelfand/go-esphome-device/api"
	"google.golang.org/protobuf/proto"
)

var (
	linuxUpdateKey   = controls.LinuxUpdate.Key()
	updateChannelKey = controls.LinuxChannel.Key()
	checkUpdatesKey  = controls.LinuxCheck.Key()
)

type updateRequest struct {
	channel    string
	generation uint64
	install    bool
	offer      *linuxupdate.Release
}

// Kept outside config.json so the previous good runtime can still parse its
// shared configuration after a trial rollback. Older runtimes ignore this file.
type updateSelection struct {
	Channel    string `json:"channel"`
	Generation uint64 `json:"generation"`
}

type arm64UpdateStore interface {
	InstalledHighWater(linuxupdate.Manifest) (int64, error)
	Install(linuxupdate.Manifest, ed25519.PublicKey, string, string, string) error
}

type linuxUpdates struct {
	mu              sync.Mutex
	selectionPath   string
	channel         string
	generation      uint64
	state           api.UpdateStateResponse
	jobs            chan updateRequest
	cancel          context.CancelFunc
	busy, activated bool
	client          linuxupdate.Client
	store           linuxupdate.Store
	arm64           arm64UpdateStore
	offer           *linuxupdate.Release
	beforeInstall   func() error
	trustPath       string
	reboot          func() error
}

func newLinuxUpdates(selectionPath, manifestPath string) (*linuxUpdates, error) {
	u := &linuxUpdates{selectionPath: selectionPath, jobs: make(chan updateRequest, 1),
		store:     linuxupdate.Store{Directory: "/store/encore", Booted: "/run/encore/slot", Recovery: "/dev/mmcblk0p12", Misc: "/dev/mmcblk0p8"},
		trustPath: "/data/encore/update-trust.json", reboot: func() error { return exec.Command("/bin/busybox.static", "reboot").Run() }}
	u.client.Directory = "/data/encore/updates"
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	if err = json.Unmarshal(b, &u.client.Installed); err != nil {
		return nil, err
	}
	u.state = api.UpdateStateResponse{Key: linuxUpdateKey, Title: "Encore Echo Dot", MissingState: true, ReleaseSummary: "Check for updates; signed update trust must be provisioned locally"}
	u.state.CurrentVersion = updateVersion(u.client.Installed.CoreVersion, u.client.Installed.Revision)
	if err := u.loadSelection(u.client.Installed.Channel); err != nil {
		return nil, err
	}
	return u, nil
}

func (u *linuxUpdates) loadSelection(defaultChannel string) error {
	var saved updateSelection
	b, err := os.ReadFile(u.selectionPath)
	if errors.Is(err, os.ErrNotExist) {
		saved.Channel = defaultChannel
		if !linuxupdate.Channel(saved.Channel) {
			return errors.New("invalid installed update channel")
		}
		b, _ = json.Marshal(saved)
		err = writePrivate(u.selectionPath, b)
	} else if err == nil {
		err = json.Unmarshal(b, &saved)
	}
	if err != nil {
		return fmt.Errorf("load/persist private update selection: %w", err)
	}
	if !linuxupdate.Channel(saved.Channel) {
		return errors.New("invalid saved update channel")
	}
	u.channel, u.generation = saved.Channel, saved.Generation
	return nil
}
func updateVersion(core string, revision int64) string {
	return fmt.Sprintf("%s (revision %d)", core, revision)
}

func newARM64Updates(selectionPath, manifestPath string) (*linuxUpdates, error) {
	b, err := os.ReadFile(manifestPath)
	if err != nil {
		return nil, err
	}
	var boot struct {
		linuxupdate.Manifest
		UpdateChannels bool `json:"update_channels"`
	}
	if err = json.Unmarshal(b, &boot); err != nil {
		return nil, err
	}
	m := boot.Manifest
	var store arm64UpdateStore = &linuxupdate.ARM64Store{Directory: "/store/encore", Booted: "/run/encore/slot"}
	if m.Target == nativeab.Target {
		if m.Format != nativeab.Format || m.BaseSHA256 != nativeab.LayoutID {
			return nil, errors.New("invalid native A/B boot identity")
		}
		store = nativeab.Device()
	}
	version := updateVersion(m.CoreVersion, m.Revision)
	u := &linuxUpdates{
		channel: "stable", jobs: make(chan updateRequest, 1),
		state: api.UpdateStateResponse{Key: linuxUpdateKey, Title: "Encore Biscuit", CurrentVersion: version, LatestVersion: version},
		client: linuxupdate.Client{Installed: m, Key: linuxupdate.BiscuitARM64Root(), Directory: "/data/encore/updates",
			BaseURL: "https://farooqu.github.io/encore-satellite"},
		arm64:  store,
		reboot: func() error { return exec.Command("/bin/busybox.static", "reboot").Run() },
		beforeInstall: func() error {
			b, err := os.ReadFile("/sys/devices/platform/amz_privacy/privacy_state")
			if err != nil || strings.TrimSpace(string(b)) != "1" {
				return errors.New("Mute the physical microphone before installing the update")
			}
			if m.Target == nativeab.Target {
				// The native store checks normal boot and confirms the running slot
				// against hardware boot control before staging anything.
				return nil
			}
			f, err := os.Open("/dev/mmcblk0p8")
			if err != nil {
				return errors.New("Cannot verify recovery boot selection; USB review required")
			}
			defer f.Close()
			var command [32]byte
			if _, err := io.ReadFull(f, command[:]); err != nil ||
				!bytes.Equal(command[:], append([]byte("boot-recovery"), make([]byte, 19)...)) {
				return errors.New("MISC does not select recovery; USB review required before updates")
			}
			return nil
		},
	}
	if boot.UpdateChannels {
		u.selectionPath = selectionPath
		if err := u.loadSelection("stable"); err != nil {
			// Keep the runtime healthy and default to Stable without replacing
			// a corrupt or unreadable preference.
			u.state.ReleaseSummary = err.Error()
		}
	}
	return u, nil
}

func (u *linuxUpdates) supportsChannelSelection() bool { return u.selectionPath != "" }

func (u *linuxUpdates) selectChannel(channel string) error {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.supportsChannelSelection() {
		return errors.New("installed recovery does not support channel selection")
	}
	if !linuxupdate.Channel(channel) {
		return errors.New("invalid update channel")
	}
	if u.activated {
		return errors.New("update already activated; reboot before changing channel")
	}
	if channel == u.channel {
		return nil
	}
	if u.generation == ^uint64(0) {
		return errors.New("update generation exhausted")
	}
	next := u.generation + 1
	b, _ := json.Marshal(updateSelection{Channel: channel, Generation: next})
	if err := writePrivate(u.selectionPath, b); err != nil {
		return err
	}
	u.channel, u.generation = channel, next
	if u.cancel != nil {
		u.cancel()
	}
	u.offer = nil
	if u.arm64 != nil {
		u.state.LatestVersion = u.state.CurrentVersion
	} else {
		u.state.LatestVersion = ""
		u.state.MissingState = true
	}
	u.state.InProgress = false
	u.state.ReleaseSummary = "Channel saved; check for updates (nothing installed)"
	return nil
}

func (u *linuxUpdates) request(install bool) {
	u.mu.Lock()
	defer u.mu.Unlock()
	if u.busy || u.activated {
		return
	}
	if u.arm64 != nil && install && u.offer == nil {
		return
	}
	u.busy = true
	u.state.InProgress = true
	if u.arm64 == nil {
		u.state.MissingState = true
		u.state.LatestVersion = ""
	} else if !install {
		u.state.LatestVersion = u.state.CurrentVersion
	}
	u.state.ReleaseSummary = "Checking signed selected-channel metadata"
	u.jobs <- updateRequest{channel: u.channel, generation: u.generation, install: install, offer: u.offer}
	u.offer = nil
}

func (u *linuxUpdates) states() []proto.Message {
	u.mu.Lock()
	defer u.mu.Unlock()
	if !u.supportsChannelSelection() {
		return []proto.Message{proto.Clone(&u.state)}
	}
	return []proto.Message{&api.SelectStateResponse{Key: updateChannelKey, State: u.channel, MissingState: !linuxupdate.Channel(u.channel)}, proto.Clone(&u.state)}
}
func (u *linuxUpdates) run(ctx context.Context, changed func() error) {
	tick := time.NewTicker(time.Hour)
	defer tick.Stop()
	if u.arm64 != nil {
		u.request(false)
	}
	for {
		select {
		case <-ctx.Done():
			return
		case <-tick.C:
			u.request(false)
		case job := <-u.jobs:
			child, cancel := context.WithCancel(ctx)
			u.mu.Lock()
			u.cancel = cancel
			stale := job.channel != u.channel || job.generation != u.generation
			u.mu.Unlock()
			if !stale {
				if u.arm64 != nil {
					u.executeARM64(child, job, changed)
				} else {
					u.execute(child, job, changed)
				}
			}
			cancel()
			u.mu.Lock()
			u.cancel = nil
			u.busy = false
			u.state.InProgress = false
			u.mu.Unlock()
			_ = changed()
		}
	}
}

func (u *linuxUpdates) executeARM64(ctx context.Context, job updateRequest, changed func() error) {
	status := func(offer *linuxupdate.Release, summary string) {
		u.mu.Lock()
		defer u.mu.Unlock()
		if job.channel != u.channel || job.generation != u.generation {
			return
		}
		u.offer = offer
		u.state.LatestVersion = u.state.CurrentVersion
		if offer != nil {
			u.state.LatestVersion = updateVersion(offer.CoreVersion, offer.Revision)
		}
		u.state.ReleaseSummary = summary
	}
	fail := func(err error) { status(nil, err.Error()) }
	m := u.client.Installed
	highest, err := u.arm64.InstalledHighWater(m)
	if err != nil {
		fail(err)
		return
	}
	r, err := u.client.Check(ctx, job.channel, highest)
	if err != nil {
		fail(err)
		return
	}
	if r == nil {
		status(nil, "No verified newer release")
		return
	}
	limit, payload := int64(linuxupdate.ARM64MaxArchive), "ARM64 rootfs"
	if m.Target == nativeab.Target {
		limit, payload = nativeab.MaxBundle, "native A/B bundle"
	}
	if r.Size > limit {
		fail(fmt.Errorf("Release exceeds %s size limit", payload))
		return
	}
	if !job.install || job.offer == nil || job.offer.Revision != r.Revision || job.offer.SHA256 != r.SHA256 {
		status(r, "Update available; confirm installation in Home Assistant")
		return
	}
	if err := u.beforeInstall(); err != nil {
		status(r, err.Error()) // user can correct privacy and confirm again
		return
	}
	status(nil, "Downloading verified "+payload)
	_ = changed()
	file, err := u.client.Download(ctx, *r)
	if err != nil {
		fail(err)
		return
	}
	defer os.Remove(file)
	if err := u.beforeInstall(); err != nil {
		status(r, err.Error())
		return
	}
	u.mu.Lock()
	if ctx.Err() != nil || job.channel != u.channel || job.generation != u.generation {
		u.mu.Unlock()
		return
	}
	// The channel cannot change between the final intent check and activation.
	if err := u.arm64.Install(m, u.client.Key, job.channel, r.Proof, file); err != nil {
		u.mu.Unlock()
		fail(err)
		return
	}
	u.activated = true
	u.mu.Unlock()
	status(nil, "Trial slot selected; reboot requested; health check pending")
	if err := u.reboot(); err != nil {
		fail(fmt.Errorf("Trial slot selected but reboot failed: %w", err))
	}
}

func (u *linuxUpdates) execute(ctx context.Context, job updateRequest, changed func() error) {
	status := func(latest, summary string, known, busy bool) {
		u.mu.Lock()
		if job.channel == u.channel && job.generation == u.generation {
			u.state.LatestVersion = latest
			u.state.ReleaseSummary = summary
			u.state.MissingState = !known
			u.state.InProgress = busy
		}
		u.mu.Unlock()
		if busy {
			_ = changed()
		}
	}
	fail := func(err error) { status("", err.Error(), false, false) }
	// Trust is a local operator-provisioned root, never a service argument/feed.
	b, err := os.ReadFile(u.trustPath)
	if err != nil {
		fail(errors.New("Provision local update-trust.json with HTTPS base_url and Ed25519 public_key"))
		return
	}
	var trust struct {
		BaseURL string `json:"base_url"`
		Key     string `json:"public_key"`
		RootCA  string `json:"root_ca_pem"`
	}
	if len(b) > 256*1024 || json.Unmarshal(b, &trust) != nil {
		fail(errors.New("Invalid local update trust"))
		return
	}
	key, err := base64.StdEncoding.DecodeString(trust.Key)
	if err != nil || len(key) != 32 {
		fail(errors.New("Invalid local Ed25519 update key"))
		return
	}
	u.client.Key = key
	u.client.BaseURL = trust.BaseURL
	u.client.HTTP = nil
	if trust.RootCA != "" {
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM([]byte(trust.RootCA)) {
			fail(errors.New("Invalid local HTTPS root CA"))
			return
		}
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.TLSClientConfig = &tls.Config{RootCAs: roots, MinVersion: tls.VersionTLS12}
		defer transport.CloseIdleConnections()
		u.client.HTTP = &http.Client{Transport: transport, Timeout: 5 * time.Minute}
	}
	i, highest, err := u.store.Inspect()
	if err != nil {
		fail(err)
		return
	}
	if i.Target != "biscuit" || i.Backend != "incubator" || i.Format != "encore-biscuit-v1" || i.CoreVersion != u.client.Installed.CoreVersion || i.Revision != u.client.Installed.Revision || i.BaseSHA256 != u.client.Installed.BaseSHA256 {
		fail(errors.New("Installed/running package identity mismatch"))
		return
	}
	r, err := u.client.Check(ctx, job.channel, highest)
	if err != nil {
		fail(err)
		return
	}
	if r == nil {
		status(updateVersion(i.CoreVersion, i.Revision), "No eligible newer core on selected channel; waiting", true, false)
		return
	}
	latest := updateVersion(r.CoreVersion, r.Revision)
	if !job.install {
		status(latest, "Update available; installation requires an explicit Update request", true, false)
		return
	}
	status(latest, "Downloading verified package (not activated)", true, true)
	file, err := u.client.Download(ctx, *r)
	if err != nil {
		fail(err)
		return
	}
	defer os.Remove(file)
	u.mu.Lock()
	stale := job.channel != u.channel || job.generation != u.generation
	u.mu.Unlock()
	if stale {
		return
	}
	status(latest, "Staging inactive application slot (not activated)", true, true)
	staged, err := u.store.Stage(ctx, *r, file)
	if err != nil {
		fail(err)
		return
	}
	defer staged.Close()
	// Selection and activation are serialized. A completed ABA switch cannot
	// authorize this older request, even though its channel text matches again.
	u.mu.Lock()
	if ctx.Err() != nil || job.channel != u.channel || job.generation != u.generation {
		u.mu.Unlock()
		return
	}
	// A storage fsync error can mean activation happened but durability is
	// uncertain. Do not accept further intent until the operator reboots/checks.
	u.activated = true
	err = staged.Activate()
	if err == nil {
		u.state.ReleaseSummary = "Trial slot activated; reboot requested; health acceptance pending"
		u.state.InProgress = false
		// The shared installer lock is persistent; release it before init can
		// terminate this process during an orderly reboot.
		err = staged.Close()
		if err == nil {
			err = u.reboot()
		}
	}
	if err != nil {
		u.state.ReleaseSummary = "Activation/reboot uncertain; inspect slots before retrying: " + err.Error()
		u.state.MissingState = true
		u.state.InProgress = false
	}
	u.mu.Unlock()
}
