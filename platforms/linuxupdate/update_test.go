package linuxupdate_test

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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	update "github.com/farooqu/encore-satellite/platforms/linuxupdate"
)

func sha(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }
func write(t *testing.T, p string, b []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(p), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(p, b, 0600); err != nil {
		t.Fatal(err)
	}
}
func read(t *testing.T, p string) string {
	t.Helper()
	b, e := os.ReadFile(p)
	if e != nil {
		t.Fatal(e)
	}
	return strings.TrimSpace(string(b))
}
func signed(t *testing.T, k ed25519.PrivateKey, r update.Release) []byte {
	t.Helper()
	b, e := json.Marshal(r)
	if e != nil {
		t.Fatal(e)
	}
	// Independent RFC 7515 construction, deliberately not a production helper.
	input := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"EdDSA","typ":"encore-linux-update+jws"}`)) + "." + base64.RawURLEncoding.EncodeToString(b)
	return []byte(input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(k, []byte(input))))
}

func fixture(t *testing.T, malicious string) (update.Store, update.Manifest, update.Release, []byte) {
	t.Helper()
	d := t.TempDir()
	store := update.Store{Directory: filepath.Join(d, "store"), Booted: filepath.Join(d, "booted"), Recovery: filepath.Join(d, "recovery"), Misc: filepath.Join(d, "misc")}
	base := map[string]string{"userspace_sha256": sha([]byte("userspace")), "kernel_inputs_sha256": sha([]byte("kernel inputs")), "kernel_sha256": sha([]byte("kernel")), "initramfs_sha256": sha([]byte("initramfs"))}
	baseJSON := fmt.Sprintf(`{"initramfs_sha256": %q, "kernel_inputs_sha256": %q, "kernel_sha256": %q, "userspace_sha256": %q}`, base["initramfs_sha256"], base["kernel_inputs_sha256"], base["kernel_sha256"], base["userspace_sha256"])
	m := update.Manifest{Schema: 1, Target: "echo-dot-2", Backend: "incubator", Format: "encore-dot-2-v1", Channel: "dev", CoreVersion: "1.9.0-dev.9", Revision: 11, BaseSHA256: sha([]byte(baseJSON)), Base: base}
	b, _ := json.Marshal(m)
	write(t, filepath.Join(store.Directory, "slots/a/etc/encore-package.json"), b)
	write(t, filepath.Join(store.Directory, "slots/a/sbin/init"), []byte("#!/bin/sh\n"))
	if err := os.Chmod(filepath.Join(store.Directory, "slots/a/sbin/init"), 0755); err != nil {
		t.Fatal(err)
	}
	for p, v := range map[string]string{"format": m.Format, "active": "a", "highest-revision": "13", "slots/a.state": "good", "slots/b.state": "good", "slots/b/old-fallback": "keep"} {
		write(t, filepath.Join(store.Directory, p), []byte(v+"\n"))
	}
	write(t, store.Booted, []byte("a\n"))
	write(t, store.Recovery, []byte("saved Linux recovery"))
	write(t, store.Misc, append([]byte("boot-recovery"), make([]byte, 19)...))
	for _, name := range []string{"linux", "twrp"} {
		v := []byte("saved Linux recovery")
		if name == "twrp" {
			v = []byte("saved original TWRP")
		}
		write(t, filepath.Join(store.Directory, name+".img"), v)
		write(t, filepath.Join(store.Directory, name+".img.sha256"), []byte(sha(v)+"  "+name+".img\n"))
	}
	var root bytes.Buffer
	gz := gzip.NewWriter(&root)
	tw := tar.NewWriter(gz)
	for _, n := range []string{"etc", "sbin", "usr", "usr/local", "usr/local/bin"} {
		if e := tw.WriteHeader(&tar.Header{Name: n + "/", Mode: 0755, Typeflag: tar.TypeDir}); e != nil {
			t.Fatal(e)
		}
	}
	for _, entry := range []struct {
		n, v string
		mode int64
	}{{"etc/encore-base", m.Format + "\n", 0644}, {"etc/inittab", "init\n", 0644}, {"sbin/init", "#!/bin/sh\n", 0755}, {"usr/local/bin/encore-satellite", "new application\n", 0755}} {
		if e := tw.WriteHeader(&tar.Header{Name: entry.n, Mode: entry.mode, Size: int64(len(entry.v)), Typeflag: tar.TypeReg}); e != nil {
			t.Fatal(e)
		}
		tw.Write([]byte(entry.v))
	}
	if malicious != "" {
		if malicious == "symlink" {
			tw.WriteHeader(&tar.Header{Name: "escape", Typeflag: tar.TypeSymlink, Linkname: t.TempDir(), Mode: 0777})
			malicious = "escape/outside"
		}
		switch malicious {
		case "directory-alias", "directory-traversal", "directory-double-slash":
			n := map[string]string{"directory-alias": "etc", "directory-traversal": "../escape/", "directory-double-slash": "extra//"}[malicious]
			tw.WriteHeader(&tar.Header{Name: n, Mode: 0755, Typeflag: tar.TypeDir})
		default:
			tw.WriteHeader(&tar.Header{Name: malicious, Mode: 0600, Size: 4, Typeflag: tar.TypeReg})
			tw.Write([]byte("evil"))
		}
	}
	tw.Close()
	gz.Write(make([]byte, 8192))
	gz.Close() // Python tarfile-like record padding.
	payload := map[string][]byte{"rootfs.tar.gz": root.Bytes(), "kernel.zImage-dtb": []byte("kernel"), "initramfs.gz": []byte("initramfs"), "kernel.json": []byte(`{"inputs_sha256":"fixture"}`)}
	m.CoreVersion = "1.10.0"
	m.Revision = 14
	m.Files = map[string]string{}
	for n, b := range payload {
		m.Files[n] = sha(b)
	}
	manifest, _ := json.Marshal(m)
	var bundle bytes.Buffer
	zw := zip.NewWriter(&bundle)
	for n, b := range payload {
		f, e := zw.Create(n)
		if e != nil {
			t.Fatal(e)
		}
		f.Write(b)
	}
	f, _ := zw.Create("manifest.json")
	f.Write(manifest)
	zw.Close()
	now := time.Now().Unix()
	r := update.Release{Target: m.Target, Backend: m.Backend, Format: m.Format, Channel: "stable", CoreVersion: m.CoreVersion, Revision: m.Revision, BaseSHA256: m.BaseSHA256, Size: int64(bundle.Len()), SHA256: sha(bundle.Bytes()), Issued: now - 3600, Expires: now + 3600}
	installed := m
	installed.CoreVersion = "1.9.0-dev.9"
	installed.Revision = 11
	return store, installed, r, bundle.Bytes()
}

func TestStagePythonBuilderOutput(t *testing.T) {
	file := filepath.Join(t.TempDir(), "bundle.zip")
	// Reuse the builder's existing synthetic kernel/userspace inputs, but
	// exercise its real archive writer and pass its untouched ZIP to Stage.
	cmd := exec.Command("python3", "-c", `
import shutil,sys
from pathlib import Path
sys.path.insert(0,str(Path.cwd()/"../../devices/echo-dot-2/packaging"))
import package,test_package
original=package.build
class Captured(Exception): pass
def capture(*args):
    original(*args)
    shutil.copyfile(args[5],sys.argv[1])
    raise Captured()
package.build=capture
try:
    test_package.BundleTests().test_build_embeds_verified_project_kernel_and_rejects_damaged_or_wrong_inputs()
except Captured:
    pass
`, file)
	if b, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("Python builder: %s %v", b, err)
	}
	b, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	z, err := zip.OpenReader(file)
	if err != nil {
		t.Fatal(err)
	}
	defer z.Close()
	var m update.Manifest
	for _, f := range z.File {
		if f.Name == "manifest.json" {
			h, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			err = json.NewDecoder(h).Decode(&m)
			h.Close()
			if err != nil {
				t.Fatal(err)
			}
		}
	}
	s, _, _, _ := fixture(t, "")
	r := update.Release{Target: m.Target, Backend: m.Backend, Format: m.Format, Channel: "dev", CoreVersion: m.CoreVersion, Revision: m.Revision, BaseSHA256: m.BaseSHA256, Size: int64(len(b)), SHA256: sha(b), Issued: time.Now().Unix() - 1, Expires: time.Now().Unix() + 3600}
	m.CoreVersion, m.Revision = "0.0.0", 1
	installed, _ := json.Marshal(m)
	write(t, filepath.Join(s.Directory, "slots/a/etc/encore-package.json"), installed)
	write(t, filepath.Join(s.Directory, "highest-revision"), []byte("1"))
	staged, err := s.Stage(t.Context(), r, file)
	if err != nil {
		t.Fatal(err)
	}
	defer staged.Close()
	if err := staged.Activate(); err != nil {
		t.Fatal(err)
	}
	if read(t, filepath.Join(s.Directory, "active")) != "b" || read(t, filepath.Join(s.Directory, "slots/b.state")) != "trial 2" {
		t.Fatal("Python-built package did not activate as a trial")
	}
}

func TestSignedDiscoveryDownloadStageAndRollback(t *testing.T) {
	s, i, r, bundle := fixture(t, "")
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	var metadata atomic.Value
	metadata.Store([]byte{})
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		switch req.URL.Path {
		case "/echo-dot-2/incubator/stable.jws":
			w.Write(metadata.Load().([]byte))
		case "/bundle.zip":
			w.Write(bundle)
		default:
			http.NotFound(w, req)
		}
	}))
	defer server.Close()
	r.URL = server.URL + "/bundle.zip"
	metadata.Store(signed(t, private, r))
	c := update.Client{BaseURL: server.URL, Directory: t.TempDir(), Installed: i, Key: public, HTTP: server.Client()}
	got, e := c.Check(t.Context(), "stable", 13)
	if e != nil || got == nil {
		t.Fatalf("already-published newer stable: %v %v", got, e)
	}
	file, e := c.Download(t.Context(), *got)
	if e != nil {
		t.Fatal(e)
	}
	staged, e := s.Stage(t.Context(), *got, file)
	if e != nil {
		t.Fatal(e)
	}
	if read(t, filepath.Join(s.Directory, "active")) != "a" || read(t, filepath.Join(s.Directory, "slots/a.state")) != "good" || read(t, filepath.Join(s.Directory, "slots/b.state")) != "bad" {
		t.Fatal("staging activated or damaged active")
	}
	if read(t, filepath.Join(s.Directory, "highest-revision")) != "14" {
		t.Fatal("attempt not reserved")
	}
	if e = staged.Activate(); e != nil {
		t.Fatal(e)
	}
	if e = staged.Close(); e != nil {
		t.Fatal(e)
	}
	if read(t, filepath.Join(s.Directory, "active")) != "b" || read(t, filepath.Join(s.Directory, "slots/b.state")) != "trial 2" {
		t.Fatal("activation not a two-boot trial")
	}
	// Run the real unchanged boot-slot selector, not a simulated rollback model.
	for n, want := range []string{"b", "b", "a"} {
		cmd := exec.Command("sh", "../../devices/echo-dot-2/packaging/slotctl", "next")
		cmd.Env = append(os.Environ(), "STORE="+s.Directory, "BOOTED="+s.Booted)
		b, e := cmd.CombinedOutput()
		if e != nil || strings.TrimSpace(string(b)) != want {
			t.Fatalf("boot %d: %s %v", n, b, e)
		}
	}
	if _, e = s.Stage(t.Context(), r, file); e == nil {
		t.Fatal("failed trial revision reused after rollback")
	}
	if read(t, s.Recovery) != "saved Linux recovery" || read(t, filepath.Join(s.Directory, "twrp.img")) != "saved original TWRP" {
		t.Fatal("recovery was changed")
	}
}

func TestFeedEligibilityAndTrust(t *testing.T) {
	_, installed, release, _ := fixture(t, "")
	for _, tc := range []struct {
		name, core string
		revision   int64
		offer      bool
	}{
		{"numeric-not-lexical", "1.10.0", 14, true}, {"older-channel", "1.8.0", 99, false}, {"equal-core", "1.9.0-dev.9", 99, false}, {"build-metadata-only", "1.9.0-dev.9+rebuilt", 99, false}, {"older-prerelease-number", "1.9.0-dev.8", 99, false}, {"newer-prerelease-number", "1.9.0-dev.10", 14, true}, {"equal-attempt", "1.10.0", 13, false}, {"behind-attempt", "1.10.0", 12, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			pub, key, _ := ed25519.GenerateKey(rand.Reader)
			r := release
			r.Channel = "dev"
			r.CoreVersion = tc.core
			r.Revision = tc.revision
			var body atomic.Value
			body.Store([]byte{})
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				if q.URL.Path != "/echo-dot-2/incubator/dev.jws" {
					t.Errorf("wrong selected path %s", q.URL.Path)
				}
				w.Write(body.Load().([]byte))
			}))
			defer srv.Close()
			r.URL = srv.URL + "/bundle.zip"
			body.Store(signed(t, key, r))
			c := update.Client{BaseURL: srv.URL, Directory: t.TempDir(), Key: pub, Installed: installed, HTTP: srv.Client()}
			v, e := c.Check(t.Context(), "dev", 13)
			if e != nil || (v != nil) != tc.offer {
				t.Fatalf("offer=%v want=%v err=%v", v, tc.offer, e)
			}
		})
	}
	for _, name := range []string{"signature", "expired", "future", "long-lifetime", "base", "backend", "target", "channel", "stable-prerelease", "http", "https-redirect-to-http", "changed-revision", "replay", "corrupt-checkpoint", "invalid-core"} {
		t.Run(name, func(t *testing.T) {
			pub, key, _ := ed25519.GenerateKey(rand.Reader)
			r := release
			var body atomic.Value
			body.Store([]byte{})
			var redirect atomic.Bool
			srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				if redirect.Load() {
					http.Redirect(w, q, "http://example.invalid/unsafe", 302)
					return
				}
				w.Write(body.Load().([]byte))
			}))
			defer srv.Close()
			r.URL = srv.URL + "/bundle.zip"
			c := update.Client{BaseURL: srv.URL, Directory: t.TempDir(), Key: pub, Installed: installed, HTTP: srv.Client()}
			if name == "changed-revision" || name == "replay" {
				body.Store(signed(t, key, r))
				if _, e := c.Check(t.Context(), "stable", 13); e != nil {
					t.Fatal(e)
				}
			}
			switch name {
			case "expired":
				r.Expires = time.Now().Unix() - 1
			case "future":
				r.Issued = time.Now().Unix() + 600
				r.Expires = r.Issued + 3600
			case "long-lifetime":
				r.Expires = r.Issued + 7*86400 + 1
			case "base":
				r.BaseSHA256 = strings.Repeat("f", 64)
			case "backend":
				r.Backend = "assist"
			case "target":
				r.Target = "other-linux"
			case "channel":
				r.Channel = "dev"
			case "stable-prerelease":
				r.CoreVersion = "1.10.0-rc.1"
			case "http":
				c.BaseURL = "http://example.invalid"
			case "https-redirect-to-http":
				redirect.Store(true)
			case "changed-revision":
				r.SHA256 = strings.Repeat("e", 64)
			case "replay":
				r.Issued--
			case "corrupt-checkpoint":
				write(t, filepath.Join(c.Directory, "stable.json"), []byte("broken"))
			case "invalid-core":
				r.CoreVersion = "1.10"
			}
			b := signed(t, key, r)
			if name == "signature" {
				_, other, _ := ed25519.GenerateKey(rand.Reader)
				b = signed(t, other, r)
			}
			body.Store(b)
			if _, e := c.Check(t.Context(), "stable", 13); e == nil {
				t.Fatal("unsafe feed accepted")
			}
		})
	}
}

func TestStagingFailsClosed(t *testing.T) {
	for _, name := range []string{"highest", "unhealthy", "wrong-running-slot", "saved-linux", "saved-twrp", "live-recovery", "misc", "bundle", "semver", "base", "locked", "traversal", "symlink", "directory-alias", "directory-traversal", "directory-double-slash", "canceled"} {
		t.Run(name, func(t *testing.T) {
			malicious := ""
			if name == "traversal" {
				malicious = "../escaped"
			}
			if name == "symlink" {
				malicious = "symlink"
			}
			if strings.HasPrefix(name, "directory-") {
				malicious = name
			}
			s, _, r, b := fixture(t, malicious)
			file := filepath.Join(t.TempDir(), "bundle.zip")
			write(t, file, b)
			switch name {
			case "highest":
				write(t, filepath.Join(s.Directory, "highest-revision"), []byte("14"))
			case "unhealthy":
				write(t, filepath.Join(s.Directory, "slots/a.state"), []byte("trial 1"))
			case "wrong-running-slot":
				write(t, s.Booted, []byte("b"))
			case "saved-linux":
				write(t, filepath.Join(s.Directory, "linux.img"), []byte("bad"))
			case "saved-twrp":
				write(t, filepath.Join(s.Directory, "twrp.img"), []byte("bad"))
			case "live-recovery":
				write(t, s.Recovery, []byte("bad"))
			case "misc":
				write(t, s.Misc, make([]byte, 32))
			case "bundle":
				write(t, file, append(b, 'x'))
			case "semver":
				r.CoreVersion = "1.8.0"
			case "base":
				r.BaseSHA256 = strings.Repeat("e", 64)
			case "locked":
				os.Mkdir(filepath.Join(s.Directory, ".install-lock"), 0700)
			}
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			if name == "canceled" {
				cancel()
			}
			if staged, e := s.Stage(ctx, r, file); e == nil {
				staged.Close()
				t.Fatal("unsafe staging accepted")
			}
			if read(t, filepath.Join(s.Directory, "active")) != "a" || read(t, filepath.Join(s.Directory, "slots/b/old-fallback")) != "keep" {
				t.Fatal("failed validation mutated slots")
			}
		})
	}
}

func TestDownloadHTTPSQueryRedirect(t *testing.T) {
	for _, name := range []string{"valid", "corrupt", "truncated", "http", "credentials", "fragment", "loop"} {
		t.Run(name, func(t *testing.T) {
			payload := []byte("release bundle bytes")
			asset := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				if q.URL.Query().Get("token") != "temporary" {
					t.Error("redirect query was lost")
				}
				switch name {
				case "corrupt":
					w.Write(bytes.Repeat([]byte("x"), len(payload)))
				case "truncated":
					w.Write(payload[:len(payload)-1])
				default:
					w.Write(payload)
				}
			}))
			defer asset.Close()
			origin := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
				target := asset.URL + "/bundle.zip?token=temporary"
				switch name {
				case "http":
					target = strings.Replace(target, "https://", "http://", 1)
				case "credentials":
					target = strings.Replace(target, "https://", "https://user:password@", 1)
				case "fragment":
					target += "#fragment"
				case "loop":
					target = "/bundle.zip?token=temporary"
				}
				http.Redirect(w, q, target, http.StatusFound)
			}))
			defer origin.Close()
			now := time.Now().Unix()
			r := update.Release{URL: origin.URL + "/bundle.zip", Size: int64(len(payload)), SHA256: sha(payload), Issued: now - 60, Expires: now + 3600}
			c := update.Client{Directory: t.TempDir(), HTTP: origin.Client()}
			path, err := c.Download(t.Context(), r)
			if name == "valid" {
				if err != nil {
					t.Fatal(err)
				}
				if got := read(t, path); got != string(payload) {
					t.Fatalf("download = %q, want %q", got, payload)
				}
			} else {
				if err == nil {
					t.Fatal("unsafe or invalid download accepted")
				}
				files, err := os.ReadDir(c.Directory)
				if err != nil || len(files) != 0 {
					t.Fatalf("failed download retained files: %v, err=%v", files, err)
				}
			}
		})
	}
}

func TestFailedDownloadRemovesPartial(t *testing.T) {
	_, i, r, b := fixture(t, "")
	pub, key, _ := ed25519.GenerateKey(rand.Reader)
	var metadata []byte
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, q *http.Request) {
		if strings.HasSuffix(q.URL.Path, ".jws") {
			w.Write(metadata)
		} else {
			w.Write(b[:len(b)-1])
		}
	}))
	defer srv.Close()
	r.URL = srv.URL + "/bundle.zip"
	metadata = signed(t, key, r)
	c := update.Client{BaseURL: srv.URL, Directory: t.TempDir(), Key: pub, Installed: i, HTTP: srv.Client()}
	got, e := c.Check(t.Context(), "stable", 13)
	if e != nil {
		t.Fatal(e)
	}
	if _, e = c.Download(t.Context(), *got); e == nil {
		t.Fatal("truncated download accepted")
	}
	files, _ := os.ReadDir(c.Directory)
	if len(files) != 1 || files[0].Name() != "stable.json" {
		t.Fatalf("partial artifacts retained: %v", files)
	}
}
