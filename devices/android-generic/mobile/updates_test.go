package mobile_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/devices/android-generic/mobile"
	"github.com/farooqu/encore-satellite/platforms/android/update"
)

func TestUpdatesCloseCancelsHTTPAndAllowsFreshWorker(t *testing.T) {
	for _, stage := range []string{"metadata", "apk"} {
		t.Run(stage, func(t *testing.T) {
			public, private, err := ed25519.GenerateKey(rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			apk := []byte("verified APK fixture")
			digest := sha256.Sum256(apk)
			blocked, disconnected, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
			var metadata string
			var requests atomic.Int32
			var stopping atomic.Bool
			stopping.Store(true)
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				isAPK := r.URL.Path == "/artifact.apk"
				if stopping.Load() && (stage == "apk") == isAPK {
					if isAPK {
						_, _ = w.Write(apk[:1])
						w.(http.Flusher).Flush()
					}
					close(blocked)
					select {
					case <-r.Context().Done():
						close(disconnected)
					case <-release:
					}
					return
				}
				if isAPK {
					_, _ = w.Write(apk)
				} else {
					_, _ = w.Write([]byte(metadata))
				}
			}))
			defer server.Close()
			defer close(release)
			// Exercise the public JNI wrapper over real TLS without changing its
			// provisioned trust or adding a production transport override.
			transport := http.DefaultTransport
			http.DefaultTransport = server.Client().Transport
			defer func() { http.DefaultTransport = transport }()
			now := time.Now().Unix()
			r := update.Release{Target: "android-generic", Backend: "incubator", Channel: "dev", ApplicationID: "com.encore.satellite.incubator.dev", Revision: 8, Version: "1.2.0", CoreVersion: "1.2.0", Source: strings.Repeat("a", 40), Toolchain: "fixture", MinSDK: 26, ABIs: []string{"arm64-v8a"}, URL: server.URL + "/artifact.apk", Size: int64(len(apk)), SHA256: hex.EncodeToString(digest[:]), CertificateSHA256: strings.Repeat("b", 64), Issued: now - 30, Expires: now + 3600}
			payload, err := json.Marshal(r)
			if err != nil {
				t.Fatal(err)
			}
			input := base64.RawURLEncoding.EncodeToString([]byte(update.Header)) + "." + base64.RawURLEncoding.EncodeToString(payload)
			metadata = input + "." + base64.RawURLEncoding.EncodeToString(ed25519.Sign(private, []byte(input)))
			directory := t.TempDir()
			newWorker := func() *mobile.Updates {
				u, err := mobile.NewUpdates(server.URL, base64.StdEncoding.EncodeToString(public), r.ApplicationID, r.CertificateSHA256, "arm64-v8a", directory, "1.1.0", 3, 33)
				if err != nil {
					t.Fatal(err)
				}
				return u
			}
			u := newWorker()
			done := make(chan error, 1)
			go func() { _, err := u.Fetch("dev", stage == "apk"); done <- err }()
			select {
			case <-blocked:
			case <-time.After(3 * time.Second):
				t.Fatal("HTTP request never reached blocking fixture")
			}
			u.Close()
			u.Close() // Repeated Stop/destruction is harmless.
			select {
			case <-disconnected:
			case <-time.After(3 * time.Second):
				t.Fatal("stopped worker left HTTP request alive")
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("stopped Fetch returned a successful result")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("stopped Fetch did not return")
			}
			if _, err := os.Stat(filepath.Join(directory, r.SHA256+".apk")); !os.IsNotExist(err) {
				t.Fatal("stopped download published an APK")
			}
			if _, err := os.Stat(filepath.Join(directory, r.SHA256+".apk.part")); !os.IsNotExist(err) {
				t.Fatal("stopped download retained partial bytes")
			}
			count := requests.Load()
			if _, err := u.Fetch("dev", false); err == nil || requests.Load() != count {
				t.Fatal("closed worker started another HTTP request")
			}
			stopping.Store(false)
			next := newWorker()
			defer next.Close()
			result, err := next.Fetch("dev", true)
			if err != nil {
				t.Fatalf("fresh worker could not download: %v", err)
			}
			var fetched struct {
				Release update.Release `json:"release"`
				Path    string         `json:"path"`
			}
			if err := json.Unmarshal([]byte(result), &fetched); err != nil {
				t.Fatal(err)
			}
			bytes, err := os.ReadFile(fetched.Path)
			if err != nil || string(bytes) != string(apk) || fetched.Release.Revision != 8 {
				t.Fatalf("fresh worker returned wrong verified download: %s, %v", result, err)
			}
		})
	}
}
