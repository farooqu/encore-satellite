package biscuit

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/farooqu/encore-satellite/backends/bluetooth"
	"github.com/farooqu/encore-satellite/devices/biscuit/internal/nativeab"
	"github.com/farooqu/encore-satellite/platforms/linuxupdate"
	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/api"
)

func testLinuxUpdates(t *testing.T, dir string) *linuxUpdates {
	t.Helper()
	manifest := filepath.Join(dir, "installed.json")
	if err := os.WriteFile(manifest, []byte(`{"channel":"dev"}`), 0600); err != nil {
		t.Fatal(err)
	}
	u, err := newLinuxUpdates(filepath.Join(dir, "update-state.json"), manifest)
	if err != nil {
		t.Fatal(err)
	}
	return u
}

func TestARM64PublicUpdateRequiresConfirmationAndPreservesEnrollment(t *testing.T) {
	for _, channel := range []string{"stable", "dev"} {
		t.Run(channel, func(t *testing.T) { testARM64PublicUpdate(t, channel, false) })
	}
	t.Run("native A/B without physical mute", func(t *testing.T) { testARM64PublicUpdate(t, "dev", true) })
}

type updatePollStore struct {
	arm64UpdateStore
	checks         atomic.Int32
	resumePeriodic <-chan struct{}
}

func (s *updatePollStore) InstalledHighWater(linuxupdate.Manifest) (int64, error) {
	if s.checks.Add(1) == 2 {
		<-s.resumePeriodic
	}
	return 0, fmt.Errorf("test stop before HTTP")
}

func TestARM64ChecksAtStartupAndEverySixHours(t *testing.T) {
	selection := filepath.Join(t.TempDir(), "selection.json")
	synctest.Test(t, func(t *testing.T) {
		resume := make(chan struct{}, 1)
		defer close(resume)
		store := &updatePollStore{resumePeriodic: resume}
		u := &linuxUpdates{arm64: store, selectionPath: selection, channel: "stable", jobs: make(chan updateRequest, 1)}
		ctx, cancel := context.WithCancel(t.Context())
		defer cancel()
		go u.run(ctx, func() error { return nil })
		assertChecks := func(want int32) {
			t.Helper()
			synctest.Wait()
			if got := store.checks.Load(); got != want {
				t.Fatalf("checked %d times, want %d", got, want)
			}
		}
		assertChecks(1)
		time.Sleep(6*time.Hour - time.Second)
		assertChecks(1)
		time.Sleep(time.Second)
		assertChecks(2)
		if err := u.selectChannel("dev"); err != nil {
			t.Fatal(err)
		}
		resume <- struct{}{}
		assertChecks(3)
		if err := u.selectChannel("stable"); err != nil {
			t.Fatal(err)
		}
		assertChecks(4)
	})
}

func testARM64PublicUpdate(t *testing.T, channel string, native bool) {
	dir := t.TempDir()
	// Use a filesystem-backed store in place of the real boot partitions.
	store := linuxupdate.ARM64Store{Directory: filepath.Join(dir, "cache/encore"), Booted: filepath.Join(dir, "run/slot")}
	m := linuxupdate.BiscuitARM64Identity("0.1.1-dev.5", 22)
	if native {
		m.Target, m.Format, m.BaseSHA256 = nativeab.Target, nativeab.Format, nativeab.LayoutID
	}
	seed := []byte("private baseline archive fixture")
	update := []byte("different public archive fixture")
	write := func(path string, data []byte) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0600); err != nil {
			t.Fatal(err)
		}
	}
	assertFile := func(path, expected string) {
		t.Helper()
		b, err := os.ReadFile(path)
		if err != nil || string(b) != expected {
			t.Fatalf("%s: got %q, want %q: %v", path, b, expected, err)
		}
	}
	for path, value := range map[string]string{"active": "a\n", "highest-revision": "22\n", "slots/a.state": "good\n", "slots/b.state": "held\n",
		"slots/a/rootfs.tar.gz": string(seed), "slots/b/rootfs.tar.gz": string(seed)} {
		write(filepath.Join(store.Directory, path), []byte(value))
	}
	write(store.Booted, []byte("a\n"))
	stateDir := filepath.Join(dir, "data/encore")
	values := map[string]string{"config.json": `{"schema":1,"name":"fresh-dot","bluetooth_proxy":true}`, "noise.psk": "test-psk",
		"wpa_supplicant.conf": "test-wifi", "bluetooth/AA:BB:CC:DD:EE:FF/peer/info": "test-bond"}
	for path, value := range values {
		write(filepath.Join(stateDir, path), []byte(value))
	}
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	digest := func(b []byte) string { return fmt.Sprintf("%x", sha256.Sum256(b)) }
	const changelog = "## Biscuit\n\n- Improve Wi-Fi recovery"
	var metadata, downloads atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/" + m.Target + "/incubator/" + channel + ".jws":
			mode := metadata.Load()
			if mode == 0 {
				http.NotFound(w, r)
				return
			}
			rel := linuxupdate.Release{Target: m.Target, Format: m.Format, Channel: channel, Backend: m.Backend,
				CoreVersion: "0.1.1", Revision: 23, BaseSHA256: m.BaseSHA256, Size: int64(len(update)), SHA256: digest(update),
				ReleaseSummary: changelog,
				URL:            "https://github.com/farooqu/encore-satellite/releases/download/biscuit-0.1.1/rootfs.tar.gz", Issued: time.Now().Unix() - 300, Expires: time.Now().Unix() + 300}
			if mode >= 3 {
				rel.CoreVersion, rel.Revision = "0.1.2", 24
				rel.URL = "https://" + r.Host + "/asset"
				rel.ReleaseSummary = ""
			}
			b, _ := json.Marshal(rel)
			message := base64.RawURLEncoding.EncodeToString([]byte(linuxupdate.Header)) + "." + base64.RawURLEncoding.EncodeToString(b)
			sig := ed25519.Sign(key, []byte(message))
			if mode == 1 {
				sig[0] ^= 1
			}
			fmt.Fprint(w, message+"."+base64.RawURLEncoding.EncodeToString(sig))
		case "/asset":
			downloads.Add(1)
			if metadata.Load() == 4 {
				w.Header().Set("Content-Length", fmt.Sprint(len(update)))
				_, _ = w.Write(update[:len(update)/2])
				return
			}
			_, _ = w.Write(update)
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	var reboots int
	bootIdentity := filepath.Join(dir, "installed.json")
	selection := filepath.Join(stateDir, "arm64-update-channel.json")
	write(bootIdentity, []byte(`{"core_version":"0.1.1-dev.5","package_revision":22,"channel":"stable"}`))
	legacy, err := newARM64Updates(selection, bootIdentity)
	if err != nil || legacy.supportsChannelSelection() || legacy.selectChannel("dev") == nil {
		t.Fatal("old recovery can select a Dev slot it cannot boot", err)
	}
	manifest, err := json.Marshal(struct {
		linuxupdate.Manifest
		UpdateChannels bool `json:"update_channels"`
	}{m, true})
	if err != nil {
		t.Fatal(err)
	}
	write(bootIdentity, manifest)
	u, err := newARM64Updates(selection, bootIdentity)
	if err != nil || len(u.states()) != 2 || u.states()[0].(*api.SelectStateResponse).State != "stable" {
		t.Fatal("new recovery did not expose Stable by default", err)
	}
	if s := u.states()[1].(*api.UpdateStateResponse); s.Title != "Encore Satellite" || s.CurrentVersion != "0.1.1-dev.5" || s.LatestVersion != s.CurrentVersion {
		t.Fatal("incorrect firmware update state for the booted image", s)
	}
	if err := u.selectChannel(channel); err != nil {
		t.Fatal(err)
	}
	u, err = newARM64Updates(selection, bootIdentity)
	if err != nil || u.states()[0].(*api.SelectStateResponse).State != channel {
		t.Fatal("selection did not survive restart", err)
	}
	u.arm64 = &store
	u.client = linuxupdate.Client{Installed: m, Key: pub, HTTP: server.Client(), BaseURL: server.URL, Directory: filepath.Join(stateDir, "updates")}
	if !native {
		u.beforeInstall = func() error { return nil } // No recovery MISC partition in the test.
	}
	u.reboot = func() error { reboots++; return nil }
	var progress []float32
	job := func(install bool) {
		t.Helper()
		u.request(install)
		select {
		case request := <-u.jobs:
			u.executeARM64(t.Context(), request, func() error {
				if s := u.states()[1].(*api.UpdateStateResponse); s.InProgress && s.HasProgress && s.LatestVersion == "0.1.2" {
					progress = append(progress, s.Progress)
				}
				return nil
			})
			u.busy, u.state.InProgress = false, false
		default:
			t.Fatal("no update job queued")
		}
	}
	u.request(true)
	if len(u.jobs) != 0 {
		t.Fatal("install accepted before showing a verified offer")
	}
	for _, mode := range []int32{0, 1} {
		metadata.Store(mode)
		job(false)
		if s := u.states()[1].(*api.UpdateStateResponse); s.MissingState || s.LatestVersion != s.CurrentVersion || strings.Contains(s.ReleaseSummary, changelog) {
			t.Fatalf("unexpected update offer: %v", s)
		}
	}
	metadata.Store(2)
	job(false)
	if got := u.states()[1].(*api.UpdateStateResponse); got.LatestVersion != "0.1.1" || !strings.HasSuffix(got.ReleaseSummary, "\n\n"+changelog) || got.ReleaseUrl != "https://github.com/farooqu/encore-satellite/releases/tag/biscuit-0.1.1" {
		t.Fatalf("missing verified offer or changelog: %v", got)
	}
	metadata.Store(3)
	job(true) // A release swap between offer and confirm requires new confirmation.
	if got := u.states()[1].(*api.UpdateStateResponse); got.LatestVersion != "0.1.2" || got.ReleaseUrl != "" || strings.Contains(got.ReleaseSummary, changelog) {
		t.Fatalf("replacement offer retained previous changelog: %v", got)
	}
	assertFile(filepath.Join(store.Directory, "active"), "a\n")
	if downloads.Load() != 0 || reboots != 0 {
		t.Fatal("a check or moved feed initiated installation")
	}
	if channel == "dev" && !native {
		guards := 0
		u.beforeInstall = func() error {
			guards++
			if guards == 2 { // Download finished, but activation has not started.
				if err := u.selectChannel("stable"); err != nil {
					return err
				}
				return u.selectChannel("dev")
			}
			return nil
		}
		job(true)
		assertFile(filepath.Join(store.Directory, "active"), "a\n")
		assertFile(filepath.Join(store.Directory, "highest-revision"), "22\n")
		if guards != 2 || downloads.Load() != 1 || reboots != 0 || u.offer != nil {
			t.Fatal("channel switch did not invalidate the downloaded offer")
		}
		u.beforeInstall = func() error { return nil }
		job(false)
	}
	if native {
		metadata.Store(4)
		job(true)
		s := u.states()[1].(*api.UpdateStateResponse)
		wantSummary := fmt.Sprintf("Download failed: %d of %d bytes received: unexpected EOF. Select Install to retry.", len(update)/2, len(update))
		if s.CurrentVersion != m.CoreVersion || s.LatestVersion != "0.1.2" || s.InProgress || s.HasProgress || s.ReleaseSummary != wantSummary {
			t.Fatalf("failed download hid the retry or reported progress: %v", s)
		}
		wantProgress := []float32{0, float32((len(update) / 2) * 100 / len(update))}
		if !reflect.DeepEqual(progress, wantProgress) {
			t.Fatalf("download progress = %v, want %v", progress, wantProgress)
		}
		assertFile(filepath.Join(store.Directory, "active"), "a\n")
		assertFile(filepath.Join(store.Directory, "highest-revision"), "22\n")
		if reboots != 0 {
			t.Fatal("failed download rebooted")
		}
		metadata.Store(3) // Retry explicitly without a separate check.
	}
	beforeDownload := downloads.Load()
	job(true)
	if downloads.Load() != beforeDownload+1 || reboots != 1 {
		t.Fatalf("downloads=%d reboots=%d: %s", downloads.Load(), reboots, u.state.ReleaseSummary)
	}
	assertFile(filepath.Join(store.Directory, "active"), "b\n")
	assertFile(filepath.Join(store.Directory, "slots/b.state"), "update\n")
	assertFile(filepath.Join(store.Directory, "highest-revision"), "24\n")
	assertFile(filepath.Join(store.Directory, "slots/a/rootfs.tar.gz"), string(seed))
	if got, err := store.VerifyBoot("b", digest(seed), m, pub); err != nil || got.Revision != 24 || got.CoreVersion != "0.1.2" {
		t.Fatalf("signed staged archive cannot boot: %+v %v", got, err)
	}
	if _, err := store.VerifyBoot("b", digest(seed), m, linuxupdate.BiscuitARM64Root()); err == nil {
		t.Fatal("production key accepted an isolated test signature")
	}
	write(filepath.Join(store.Directory, "slots/b/rootfs.tar.gz"), bytes.Repeat([]byte{'x'}, len(update)))
	if _, err := store.VerifyBoot("b", digest(seed), m, pub); err == nil {
		t.Fatal("signed receipt accepted tampered archive bytes")
	}
	if got, err := store.VerifyBoot("a", digest(seed), m, pub); err != nil || got.Revision != 22 {
		t.Fatalf("old good slot cannot roll back beneath high-water: %+v %v", got, err)
	}
	for path, value := range values {
		assertFile(filepath.Join(stateDir, path), value)
	}
}

func TestPrivateUpdateSelectionInitialization(t *testing.T) {
	dir := t.TempDir()
	manifest, selection := filepath.Join(dir, "installed.json"), filepath.Join(dir, "update-state.json")
	if err := os.WriteFile(manifest, []byte(`{"channel":"stable"}`), 0600); err != nil {
		t.Fatal(err)
	}
	u, err := newLinuxUpdates(selection, manifest)
	if err != nil {
		t.Fatal(err)
	}
	if s := u.states()[0].(*api.SelectStateResponse); s.State != "stable" {
		t.Fatal("did not initialize from installed provenance", s)
	}
	if err := u.selectChannel("dev"); err != nil {
		t.Fatal(err)
	}
	u, err = newLinuxUpdates(selection, manifest)
	if err != nil || u.channel != "dev" || u.generation != 1 {
		t.Fatalf("installed provenance replaced persisted intent: %+v %v", u, err)
	}
	for _, broken := range []string{`{`, `{"channel":"nightly","generation":99}`} {
		if err := os.WriteFile(selection, []byte(broken), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := newLinuxUpdates(selection, manifest); err == nil {
			t.Fatal("invalid selection silently reinitialized")
		}
		if b, err := os.ReadFile(selection); err != nil || string(b) != broken {
			t.Fatal("invalid saved selection overwritten")
		}
	}
}

// Exercise the real Noise endpoint. Selection is durable before its state
// arrives; neither selection nor CHECK can become an installation request.
func TestLinuxUpdateAndRingControlsEncryptedAPI(t *testing.T) {
	c := Config{Schema: 1, Name: "update-test"}
	s := &settings{path: filepath.Join(t.TempDir(), "config.json"), config: c}
	if err := SaveConfig(s.path, c); err != nil {
		t.Fatal(err)
	}
	// This exact shared config remains readable by the old good runtime,
	// which rejects unknown fields. Neither initialization nor selection may
	// add updater fields, even before the new slot has passed health checks.
	legacyConfig := `{"schema":1,"name":"update-test"}`
	manifest := filepath.Join(filepath.Dir(s.path), "installed.json")
	if err := os.WriteFile(manifest, []byte(`{"channel":"dev","core_version":"1.3.0-dev.4","package_revision":42}`), 0600); err != nil {
		t.Fatal(err)
	}
	u, err := newLinuxUpdates(filepath.Join(filepath.Dir(s.path), "update-state.json"), manifest)
	if err != nil {
		t.Fatal(err)
	}
	u.trustPath = filepath.Join(t.TempDir(), "absent-trust.json")
	p := newSpeaker(&speakerPCM{})
	defer p.Close()
	ring, err := loadRingSettings(filepath.Join(filepath.Dir(s.path), "ring.json"))
	if err != nil {
		t.Fatal(err)
	}
	d := &biscuitControls{Proxy: bluetooth.New(managementRadio{}, bluetooth.Options{}), player: p, music: newMusic(c, s, p), updates: u, ring: ring}
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
	go func() {
		done <- d.Serve(ctx, l, d.controlInfo(c.Name, net.HardwareAddr{0x02, 0x13, 0x24, 0x35, 0x0a, 0xb2}), key)
	}()
	t.Cleanup(func() { cancel(); l.Close(); <-done })
	peer := newManagementPeer(t, l.Addr().String(), key)
	peer.send(&api.HelloRequest{ClientInfo: "linux-update-test", ApiVersionMajor: 1, ApiVersionMinor: 12})
	peer.receive()
	peer.send(&api.DeviceInfoRequest{})
	device, ok := peer.receive().(*api.DeviceInfoResponse)
	if !ok || device.Name != "update-test" || device.MacAddress != "02:13:24:35:0a:b2" {
		t.Fatal("product metadata replaced per-unit identity", device)
	}
	if device.FriendlyName != "Encore Echo Dot · 0AB2" || device.Manufacturer != "Amazon" || device.Model != "Echo Dot (2nd gen)" ||
		device.ProjectName != "Encore Satellite.Amazon Echo Dot (2nd gen)" || device.ProjectVersion != "1.3.0-dev.4" ||
		device.EsphomeVersion != esphome.DefaultESPHomeVersion {
		t.Fatal("incorrect product metadata", device)
	}
	peer.send(&api.ListEntitiesRequest{})
	var channel, check, update, brightness, motion uint32
listing:
	for {
		switch v := peer.receive().(type) {
		case *api.ListEntitiesNumberResponse:
			if v.ObjectId == "ring_brightness" {
				brightness = v.Key
				if v.MinValue != 1 || v.MaxValue != 100 || v.Step != 1 || v.EntityCategory != esphome.CategoryConfig {
					t.Fatal(v)
				}
			}
		case *api.ListEntitiesSwitchResponse:
			if v.ObjectId == "ring_reduced_motion" {
				motion = v.Key
			}
		case *api.ListEntitiesSelectResponse:
			if v.ObjectId == "update_channel" {
				channel = v.Key
				if !reflect.DeepEqual(v.Options, []string{"dev", "stable"}) || v.EntityCategory != esphome.CategoryConfig {
					t.Fatal(v)
				}
			}
		case *api.ListEntitiesButtonResponse:
			if v.ObjectId == "check_updates" {
				check = v.Key
			}
		case *api.ListEntitiesUpdateResponse:
			if v.ObjectId == "linux_update" {
				update = v.Key
				// Pin the pre-rename wire key, not a value derived by the implementation.
				if v.Name != "Firmware update" || v.Key != 4164145515 {
					t.Fatal("incorrect firmware entity name or changed command key", v)
				}
			}
		case *api.ListEntitiesDoneResponse:
			break listing
		}
	}
	if channel == 0 || check == 0 || update == 0 || brightness == 0 || motion == 0 {
		t.Fatal("incomplete update/ring discovery")
	}
	peer.send(&api.SubscribeStatesRequest{})
	for {
		if v, ok := peer.receive().(*api.SelectStateResponse); ok && v.Key == channel {
			if v.State != "dev" {
				t.Fatal(v)
			}
			break
		}
	}
	peer.send(&api.NumberCommandRequest{Key: brightness, State: 34})
	for {
		if v, ok := peer.receive().(*api.NumberStateResponse); ok && v.Key == brightness && v.State == 34 {
			break
		}
	}
	peer.send(&api.SwitchCommandRequest{Key: motion, State: true})
	for {
		if v, ok := peer.receive().(*api.SwitchStateResponse); ok && v.Key == motion && v.State {
			break
		}
	}
	loadedRing, err := loadRingSettings(ring.path)
	if err != nil || loadedRing.snapshot() != (RingOptions{Brightness: 34, ReducedMotion: true}) {
		t.Fatal("ring state acknowledged before durable persistence", err)
	}
	if b, err := os.ReadFile(s.path); err != nil || string(b) != legacyConfig {
		t.Fatal("ring policy changed the old good slot's config", err)
	}
	for _, value := range []string{"stable", "dev"} {
		peer.send(&api.SelectCommandRequest{Key: channel, State: value})
		for {
			if v, ok := peer.receive().(*api.SelectStateResponse); ok && v.Key == channel && v.State == value {
				break
			}
		}
		var saved updateSelection
		b, err := os.ReadFile(u.selectionPath)
		if err != nil || json.Unmarshal(b, &saved) != nil || saved.Channel != value {
			t.Fatalf("state acknowledged before persistence: %+v %v", saved, err)
		}
		if b, err := os.ReadFile(s.path); err != nil || string(b) != legacyConfig {
			t.Fatalf("old good slot config changed: %s %v", b, err)
		}
	}
	info, err := os.Stat(u.selectionPath)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("selection is not private: %v %v", info, err)
	}
	// Old-runtime settings rewrites must not erase the separate ABA fence.
	if err := s.update(func(c *Config) { c.BluetoothProxy = true }); err != nil {
		t.Fatal(err)
	}
	// LoadConfig is unchanged from the maintenance good slot, including its
	// strict unknown-field rejection. Exercise it on the persisted result.
	if saved, err := LoadConfig(s.path); err != nil || saved != (Config{Schema: 1, Name: "update-test", BluetoothProxy: true}) {
		t.Fatalf("fallback cannot load shared settings: %+v %v", saved, err)
	}
	restarted, err := newLinuxUpdates(u.selectionPath, filepath.Join(filepath.Dir(s.path), "installed.json"))
	if err != nil {
		t.Fatal(err)
	}
	if v := restarted.states()[0].(*api.SelectStateResponse); v.State != "dev" || restarted.generation != 2 {
		t.Fatal("lost desired channel after restart")
	}
	// A CHECK and explicit UPDATE both reach the worker but fail closed when
	// trust is absent. Unknown state carries an actionable reason, not current=latest.
	for _, command := range []api.UpdateCommand{api.UpdateCommand_UPDATE_COMMAND_CHECK, api.UpdateCommand_UPDATE_COMMAND_UPDATE} {
		peer.send(&api.UpdateCommandRequest{Key: update, Command: command})
		for {
			if v, ok := peer.receive().(*api.UpdateStateResponse); ok && !v.InProgress && strings.Contains(v.ReleaseSummary, "Provision local") {
				if v.Title != "Encore Satellite" || v.Key != update || v.CurrentVersion != "1.3.0-dev.4" {
					t.Fatal("incorrect title, version or update association", v)
				}
				if !v.MissingState || v.LatestVersion != "" {
					t.Fatal("untrusted feed reported up-to-date", v)
				}
				break
			}
		}
	}
	// Invalid selection and failed persistence retain the successfully saved
	// intent, including generation. This is the same handler invoked by Noise.
	if err = d.Handle(ctx, nil, &api.SelectCommandRequest{Key: channel, State: "nightly"}); err == nil {
		t.Fatal("invalid channel accepted")
	}
	u.selectionPath = filepath.Join(t.TempDir(), "missing-parent/update-state.json")
	if err = d.Handle(ctx, nil, &api.SelectCommandRequest{Key: channel, State: "stable"}); err == nil {
		t.Fatal("failed save accepted")
	}
	b, _ := json.Marshal(u.states()[0])
	if !strings.Contains(string(b), `"state":"dev"`) {
		t.Fatal("failed save changed selection")
	}
	if _, err = os.Stat(u.selectionPath); !os.IsNotExist(err) {
		t.Fatal("unexpected update-state write")
	}
}

func TestOldUpdateRequestCannotSurviveABASelection(t *testing.T) {
	u := testLinuxUpdates(t, t.TempDir())
	u.trustPath = filepath.Join(t.TempDir(), "absent-trust")
	d := &biscuitControls{updates: u}
	// Queue UPDATE through the real control handler, then switch away/back
	// before its worker can run. Channel equality alone would execute the stale
	// request and replace the saved-channel status with a trust failure.
	if err := d.Handle(t.Context(), nil, &api.UpdateCommandRequest{Key: linuxUpdateKey, Command: api.UpdateCommand_UPDATE_COMMAND_UPDATE}); err != nil {
		t.Fatal(err)
	}
	for _, channel := range []string{"stable", "dev"} {
		if err := d.Handle(t.Context(), nil, &api.SelectCommandRequest{Key: updateChannelKey, State: channel}); err != nil {
			t.Fatal(err)
		}
	}
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	finished := make(chan struct{}, 1)
	done := make(chan struct{})
	go func() { defer close(done); u.run(ctx, func() error { finished <- struct{}{}; return nil }) }()
	select {
	case <-finished:
	case <-time.After(2 * time.Second):
		t.Fatal("stale request did not finish")
	}
	cancel()
	<-done
	state := u.states()[1].(*api.UpdateStateResponse)
	if !strings.Contains(state.ReleaseSummary, "Channel saved") || state.LatestVersion != "" || state.InProgress {
		t.Fatal("ABA request reused old authorization", state)
	}
}

func TestSelectedChannelCheckAndInFlightABADownload(t *testing.T) {
	dir := t.TempDir()
	u := testLinuxUpdates(t, dir)
	u.store = linuxupdate.Store{Directory: filepath.Join(dir, "store"), Booted: filepath.Join(dir, "booted"), Recovery: filepath.Join(dir, "recovery"), Misc: filepath.Join(dir, "misc")}
	u.client.Directory = filepath.Join(dir, "downloads")
	u.client.Installed = linuxupdate.Manifest{Schema: 1, Target: "biscuit", Backend: "incubator", Format: "encore-biscuit-v1", Channel: "dev", Revision: 20, CoreVersion: "1.3.0-dev.4", BaseSHA256: strings.Repeat("a", 64)}
	u.state.CurrentVersion = "1.3.0-dev.4"
	put := func(p string, b []byte) {
		t.Helper()
		if e := os.MkdirAll(filepath.Dir(p), 0700); e != nil {
			t.Fatal(e)
		}
		if e := os.WriteFile(p, b, 0600); e != nil {
			t.Fatal(e)
		}
	}
	sha := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
	base := map[string]string{"userspace_sha256": sha([]byte("base")), "kernel_inputs_sha256": sha([]byte("inputs")), "kernel_sha256": sha([]byte("kernel")), "initramfs_sha256": sha([]byte("initramfs"))}
	baseJSON := fmt.Sprintf(`{"initramfs_sha256": %q, "kernel_inputs_sha256": %q, "kernel_sha256": %q, "userspace_sha256": %q}`, base["initramfs_sha256"], base["kernel_inputs_sha256"], base["kernel_sha256"], base["userspace_sha256"])
	u.client.Installed.BaseSHA256 = sha([]byte(baseJSON))
	u.client.Installed.Base = base
	var root bytes.Buffer
	gz := gzip.NewWriter(&root)
	tarball := tar.NewWriter(gz)
	for name, data := range map[string]string{"sbin/init": "#!/bin/sh\n", "usr/local/bin/encore-satellite": "new application", "etc/inittab": "init", "etc/encore-base": "encore-biscuit-v1\n"} {
		if err := tarball.WriteHeader(&tar.Header{Name: name, Typeflag: tar.TypeReg, Mode: 0755, Size: int64(len(data))}); err != nil {
			t.Fatal(err)
		}
		if _, err := tarball.Write([]byte(data)); err != nil {
			t.Fatal(err)
		}
	}
	if err := tarball.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	payloads := map[string][]byte{"rootfs.tar.gz": root.Bytes(), "kernel.zImage-dtb": []byte("kernel"), "initramfs.gz": []byte("initramfs"), "kernel.json": []byte("kernel provenance")}
	next := u.client.Installed
	next.CoreVersion, next.Revision = "1.4.0-dev.1", 22
	next.Files = map[string]string{}
	for name, data := range payloads {
		next.Files[name] = sha(data)
	}
	newManifest, _ := json.Marshal(next)
	payloads["manifest.json"] = newManifest
	var bundle bytes.Buffer
	zipper := zip.NewWriter(&bundle)
	for name, data := range payloads {
		f, err := zipper.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = f.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zipper.Close(); err != nil {
		t.Fatal(err)
	}
	manifest, _ := json.Marshal(u.client.Installed)
	for p, b := range map[string][]byte{"format": []byte("encore-biscuit-v1"), "active": []byte("a"), "slots/a.state": []byte("good"), "highest-revision": []byte("21"), "slots/a/etc/encore-package.json": manifest, "linux.img": []byte("linux recovery"), "twrp.img": []byte("original twrp"), "linux.img.sha256": []byte(sha([]byte("linux recovery"))), "twrp.img.sha256": []byte(sha([]byte("original twrp")))} {
		put(filepath.Join(u.store.Directory, p), b)
	}
	put(u.store.Booted, []byte("a"))
	put(u.store.Recovery, []byte("linux recovery"))
	put(u.store.Misc, append([]byte("boot-recovery"), make([]byte, 19)...))
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	var metadata atomic.Value
	metadata.Store([]byte{})
	started, release := make(chan struct{}, 1), make(chan struct{})
	var downloads atomic.Int32
	var rebooted atomic.Bool
	u.reboot = func() error {
		if _, err := os.Stat(filepath.Join(u.store.Directory, ".install-lock")); !os.IsNotExist(err) {
			return fmt.Errorf("installer lock retained at reboot: %v", err)
		}
		rebooted.Store(true)
		return nil
	}
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/biscuit/incubator/dev.jws":
			w.Write(metadata.Load().([]byte))
		case "/bundle.zip":
			downloads.Add(1)
			started <- struct{}{}
			<-release
			w.Write(bundle.Bytes())
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	unblocked := false
	defer func() {
		if !unblocked {
			close(release)
		}
	}()
	r := linuxupdate.Release{Target: "biscuit", Backend: "incubator", Format: "encore-biscuit-v1", Channel: "dev", CoreVersion: "1.4.0-dev.1", Revision: 22, BaseSHA256: u.client.Installed.BaseSHA256, URL: server.URL + "/bundle.zip", Size: int64(bundle.Len()), SHA256: sha(bundle.Bytes()), Issued: time.Now().Unix() - 3600, Expires: time.Now().Unix() + 3600}
	b, _ := json.Marshal(r)
	input := base64.RawURLEncoding.EncodeToString([]byte(linuxupdate.Header)) + "." + base64.RawURLEncoding.EncodeToString(b)
	metadata.Store([]byte(input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(key, []byte(input)))))
	trust, _ := json.Marshal(map[string]string{"base_url": server.URL, "public_key": base64.StdEncoding.EncodeToString(pub), "root_ca_pem": string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}))})
	u.trustPath = filepath.Join(dir, "trust.json")
	put(u.trustPath, trust)
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
	changed, done := make(chan struct{}, 32), make(chan struct{})
	go func() { defer close(done); u.run(ctx, func() error { changed <- struct{}{}; return nil }) }()
	defer func() { cancel(); <-done }()
	d := &biscuitControls{updates: u}
	wait := func(predicate func(*api.UpdateStateResponse) bool) *api.UpdateStateResponse {
		t.Helper()
		timeout := time.After(3 * time.Second)
		for {
			s := u.states()[1].(*api.UpdateStateResponse)
			if predicate(s) {
				return s
			}
			select {
			case <-changed:
			case <-timeout:
				t.Fatal("update state timed out:", s)
			}
		}
	}
	if err := d.Handle(ctx, nil, &api.ButtonCommandRequest{Key: checkUpdatesKey}); err != nil {
		t.Fatal(err)
	}
	wait(func(s *api.UpdateStateResponse) bool {
		return !s.InProgress && !s.MissingState && s.LatestVersion == "1.4.0-dev.1"
	})
	if downloads.Load() != 0 {
		t.Fatal("CHECK downloaded a package")
	}
	if err := d.Handle(ctx, nil, &api.UpdateCommandRequest{Key: linuxUpdateKey, Command: api.UpdateCommand_UPDATE_COMMAND_UPDATE}); err != nil {
		t.Fatal(err)
	}
	select {
	case <-started:
	case <-time.After(3 * time.Second):
		t.Fatal("explicit UPDATE did not download")
	}
	for _, channel := range []string{"stable", "dev"} {
		if err := d.Handle(ctx, nil, &api.SelectCommandRequest{Key: updateChannelKey, State: channel}); err != nil {
			t.Fatal(err)
		}
	}
	state := wait(func(s *api.UpdateStateResponse) bool { u.mu.Lock(); defer u.mu.Unlock(); return !u.busy })
	if !strings.Contains(state.ReleaseSummary, "Channel saved") || state.LatestVersion != "" || !state.MissingState {
		t.Fatal("in-flight ABA result resurfaced", state)
	}
	active, _ := os.ReadFile(filepath.Join(u.store.Directory, "active"))
	highest, _ := os.ReadFile(filepath.Join(u.store.Directory, "highest-revision"))
	if string(active) != "a" || string(highest) != "21" {
		t.Fatal("canceled download touched slot state")
	}
	files, _ := os.ReadDir(u.client.Directory)
	for _, f := range files {
		if f.Name() != "dev.json" {
			t.Fatal("canceled download left partial package", f.Name())
		}
	}
	if rebooted.Load() {
		t.Fatal("channel switch rebooted")
	}
	// A fresh explicit UPDATE after cancellation may now install the same
	// already-published candidate. The canceled request conferred no authority.
	close(release)
	unblocked = true
	if err := d.Handle(ctx, nil, &api.UpdateCommandRequest{Key: linuxUpdateKey, Command: api.UpdateCommand_UPDATE_COMMAND_UPDATE}); err != nil {
		t.Fatal(err)
	}
	state = wait(func(s *api.UpdateStateResponse) bool {
		return !s.InProgress && strings.Contains(s.ReleaseSummary, "Trial slot activated")
	})
	if !rebooted.Load() || state.MissingState || state.CurrentVersion != "1.3.0-dev.4" {
		t.Fatal("trial confused with running/healthy version", state)
	}
	active, _ = os.ReadFile(filepath.Join(u.store.Directory, "active"))
	highest, _ = os.ReadFile(filepath.Join(u.store.Directory, "highest-revision"))
	trial, _ := os.ReadFile(filepath.Join(u.store.Directory, "slots/b.state"))
	if string(active) != "b\n" || string(highest) != "22\n" || string(trial) != "trial 2\n" {
		t.Fatal("explicit update did not activate reserved trial")
	}
}
