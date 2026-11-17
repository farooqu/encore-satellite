package dot

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
	"time"

	"github.com/farooqu/encore-satellite/backends/bluetooth"
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
func TestLinuxUpdateControlsEncryptedAPI(t *testing.T) {
	c := Config{Schema: 1, Name: "update-test"}
	s := &settings{path: filepath.Join(t.TempDir(), "config.json"), config: c}
	if err := SaveConfig(s.path, c); err != nil {
		t.Fatal(err)
	}
	// This exact shared config remains readable by the old good runtime,
	// which rejects unknown fields. Neither initialization nor selection may
	// add updater fields, even before the new slot has passed health checks.
	legacyConfig := `{"schema":1,"name":"update-test"}`
	u := testLinuxUpdates(t, filepath.Dir(s.path))
	u.trustPath = filepath.Join(t.TempDir(), "absent-trust.json")
	p := newSpeaker(&speakerPCM{})
	defer p.Close()
	d := &dotControls{Proxy: bluetooth.New(managementRadio{}, bluetooth.Options{}), music: newMusic(c, s, p), updates: u}
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
	go func() { done <- d.Serve(ctx, l, esphome.Info{Name: c.Name}, key) }()
	t.Cleanup(func() { cancel(); l.Close(); <-done })
	peer := newManagementPeer(t, l.Addr().String(), key)
	peer.send(&api.HelloRequest{ClientInfo: "linux-update-test", ApiVersionMajor: 1, ApiVersionMinor: 12})
	peer.receive()
	peer.send(&api.ListEntitiesRequest{})
	var channel, check, update uint32
listing:
	for {
		switch v := peer.receive().(type) {
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
			}
		case *api.ListEntitiesDoneResponse:
			break listing
		}
	}
	if channel == 0 || check == 0 || update == 0 {
		t.Fatal("incomplete update discovery")
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
	d := &dotControls{updates: u}
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
	u.client.Installed = linuxupdate.Manifest{Schema: 1, Target: "echo-dot-2", Backend: "incubator", Format: "encore-dot-2-v1", Channel: "dev", Revision: 20, CoreVersion: "1.3.0-dev.4", BaseSHA256: strings.Repeat("a", 64)}
	u.state.CurrentVersion = "1.3.0-dev.4 (revision 20)"
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
	for name, data := range map[string]string{"sbin/init": "#!/bin/sh\n", "usr/local/bin/encore-satellite": "new application", "etc/inittab": "init", "etc/encore-base": "encore-dot-2-v1\n"} {
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
	for p, b := range map[string][]byte{"format": []byte("encore-dot-2-v1"), "active": []byte("a"), "slots/a.state": []byte("good"), "highest-revision": []byte("21"), "slots/a/etc/encore-package.json": manifest, "linux.img": []byte("linux recovery"), "twrp.img": []byte("original twrp"), "linux.img.sha256": []byte(sha([]byte("linux recovery"))), "twrp.img.sha256": []byte(sha([]byte("original twrp")))} {
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
		case "/echo-dot-2/incubator/dev.jws":
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
	r := linuxupdate.Release{Target: "echo-dot-2", Backend: "incubator", Format: "encore-dot-2-v1", Channel: "dev", CoreVersion: "1.4.0-dev.1", Revision: 22, BaseSHA256: u.client.Installed.BaseSHA256, URL: server.URL + "/bundle.zip", Size: int64(bundle.Len()), SHA256: sha(bundle.Bytes()), Issued: time.Now().Unix() - 3600, Expires: time.Now().Unix() + 3600}
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
	d := &dotControls{updates: u}
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
		return !s.InProgress && !s.MissingState && s.LatestVersion == "1.4.0-dev.1 (revision 22)"
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
	if !rebooted.Load() || state.MissingState || state.CurrentVersion != "1.3.0-dev.4 (revision 20)" {
		t.Fatal("trial confused with running/healthy version", state)
	}
	active, _ = os.ReadFile(filepath.Join(u.store.Directory, "active"))
	highest, _ = os.ReadFile(filepath.Join(u.store.Directory, "highest-revision"))
	trial, _ := os.ReadFile(filepath.Join(u.store.Directory, "slots/b.state"))
	if string(active) != "b\n" || string(highest) != "22\n" || string(trial) != "trial 2\n" {
		t.Fatal("explicit update did not activate reserved trial")
	}
}
