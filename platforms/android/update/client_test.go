package update_test

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/platforms/android/update"
)

func TestSignedChannelDownloadAndRejections(t *testing.T) {
	public, private, _ := ed25519.GenerateKey(rand.Reader)
	apk := []byte("a signed APK is parsed independently by Android")
	hash := sha256.Sum256(apk)
	var metadata string
	body := apk
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/artifact.apk" {
			_, _ = w.Write(body)
			return
		}
		if r.URL.Path != "/android-generic/incubator/dev.jws" && r.URL.Path != "/android-generic/incubator/stable.jws" {
			t.Errorf("wrong discovery path: %s", r.URL.Path)
		}
		_, _ = w.Write([]byte(metadata))
	}))
	defer server.Close()
	now := time.Now().Unix()
	r := update.Release{Target: "android-generic", Backend: "incubator", Channel: "dev", ApplicationID: "com.encore.satellite.incubator.dev", Revision: 8, Version: "1.2.0", CoreVersion: "1.2.0", Source: strings.Repeat("a", 40), Toolchain: "fixture", MinSDK: 26, ABIs: []string{"arm64-v8a"}, URL: server.URL + "/artifact.apk", Size: int64(len(apk)), SHA256: hex.EncodeToString(hash[:]), CertificateSHA256: strings.Repeat("b", 64), Issued: now - 30, Expires: now + 3600}
	sign := func(release update.Release) string {
		b, _ := json.Marshal(release)
		input := base64.RawURLEncoding.EncodeToString([]byte(update.Header)) + "." + base64.RawURLEncoding.EncodeToString(b)
		return input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(input)))
	}
	client := func() *update.Client {
		return &update.Client{BaseURL: server.URL, Key: public, ApplicationID: r.ApplicationID, CertificateSHA256: r.CertificateSHA256, ABI: "arm64-v8a", Directory: t.TempDir(), Installed: 3, InstalledCore: "1.1.0", SDK: 33, HTTP: server.Client()}
	}
	metadata = sign(r)
	c := client()
	release, path, err := c.Download(context.Background(), "dev")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(path)
	if string(b) != string(apk) || release.Revision != 8 {
		t.Fatalf("wrong download: %q %#v", b, release)
	}
	// A process restart must not lose anti-replay state.
	restarted := *c
	older := r
	older.Revision--
	metadata = sign(older)
	if _, err = restarted.Check(context.Background(), "dev"); err == nil {
		t.Fatal("accepted older observed revision after restart")
	}
	for name, mutate := range map[string]func(*update.Release){
		"backend":       func(r *update.Release) { r.Backend = "assist" },
		"target":        func(r *update.Release) { r.Target = "biscuit" },
		"channel":       func(r *update.Release) { r.Channel = "stable" },
		"package":       func(r *update.Release) { r.ApplicationID += ".other" },
		"certificate":   func(r *update.Release) { r.CertificateSHA256 = strings.Repeat("c", 64) },
		"SDK":           func(r *update.Release) { r.MinSDK = 34 },
		"ABI":           func(r *update.Release) { r.ABIs = []string{"x86_64"} },
		"expired":       func(r *update.Release) { r.Expires = now - 1 },
		"future":        func(r *update.Release) { r.Issued = now + 1000 },
		"long lifetime": func(r *update.Release) { r.Expires = now + 8*24*3600 },
		"HTTP":          func(r *update.Release) { r.URL = "http://example.com/update.apk" },
		"oversize":      func(r *update.Release) { r.Size = update.MaxAPK + 1 },
		"invalid core":  func(r *update.Release) { r.CoreVersion = "1.02.3" },
	} {
		t.Run(name, func(t *testing.T) {
			bad := r
			mutate(&bad)
			metadata = sign(bad)
			if _, err := client().Check(context.Background(), "dev"); err == nil {
				t.Fatal("unsafe release accepted")
			}
		})
	}
	metadata = sign(r)
	if _, err := client().Check(context.Background(), "stable"); err == nil {
		t.Fatal("dev entry leaked into stable")
	}
	stable := r
	stable.Channel = "stable"
	stable.CoreVersion = "1.2.0-dev.1"
	metadata = sign(stable)
	if _, err := client().Check(context.Background(), "stable"); err == nil {
		t.Fatal("stable accepted prerelease core")
	}
	stable.CoreVersion = "1.2.0+build-a"
	metadata = sign(stable)
	if _, err := client().Check(context.Background(), "stable"); err != nil {
		t.Fatal(err)
	}
	metadata = sign(r)
	installed := client()
	installed.Installed = 9
	if found, err := installed.Check(context.Background(), "dev"); err != nil || found != nil {
		t.Fatalf("downgrade offered: %#v %v", found, err)
	}
	for _, test := range []struct {
		installed, candidate string
		offered              bool
	}{
		{"1.9.0", "1.10.0", true},
		{"1.10.0", "1.9.0", false},
		{"1.2.0", "1.2.0", false},
		{"1.2.0+old", "1.2.0+new", false},
		{"1.2.0", "1.2.0-dev.11", false},
		{"1.2.0-dev.9", "1.2.0-dev.10", true},
		{"1.2.0-dev.10", "1.2.0", true},
	} {
		t.Run(test.installed+" to "+test.candidate, func(t *testing.T) {
			candidate := r
			candidate.CoreVersion = test.candidate
			metadata = sign(candidate)
			device := client()
			device.InstalledCore = test.installed
			found, err := device.Check(context.Background(), "dev")
			if err != nil || (found != nil) != test.offered {
				t.Fatalf("offered=%v, want=%v, err=%v", found != nil, test.offered, err)
			}
		})
	}
	metadata = sign(r)
	for _, corrupt := range [][]byte{[]byte("tampered"), append(append([]byte{}, apk...), 0)} {
		body = corrupt
		if _, _, err := c.Download(context.Background(), "dev"); err == nil {
			t.Fatal("tampered APK accepted")
		}
		retained, err := os.ReadFile(path)
		if err != nil || string(retained) != string(apk) {
			t.Fatal("failed download damaged verified APK")
		}
		if _, err := os.Stat(path + ".part"); !os.IsNotExist(err) {
			t.Fatal("partial APK retained")
		}
	}
	body = apk
	metadata = sign(r)
	parts := strings.Split(metadata, ".")
	parts[2] = base64.RawURLEncoding.EncodeToString(make([]byte, 64))
	metadata = strings.Join(parts, ".")
	if _, err := client().Check(context.Background(), "dev"); err == nil {
		t.Fatal("bad signature accepted")
	}
	metadata = sign(r)
	replaced := r
	replaced.SHA256 = strings.Repeat("d", 64)
	metadata = sign(replaced)
	if _, err := c.Check(context.Background(), "dev"); err == nil {
		t.Fatal("revision bytes replaced")
	}
}
