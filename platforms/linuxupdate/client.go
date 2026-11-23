// Package linuxupdate verifies signed Linux application-slot updates. It never
// migrates a kernel/base or fetches trust from the network.
package linuxupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/farooqu/encore-satellite/core/updateverify"
	"golang.org/x/mod/semver"
)

const Header = `{"alg":"EdDSA","typ":"encore-linux-update+jws"}`
const MaxBundle = 256 << 20

type Manifest struct {
	Schema      int               `json:"schema"`
	Target      string            `json:"target"`
	Backend     string            `json:"backend"`
	Format      string            `json:"format"`
	Channel     string            `json:"channel"`
	CoreVersion string            `json:"core_version"`
	Revision    int64             `json:"package_revision"`
	BaseSHA256  string            `json:"base_sha256"`
	Base        map[string]string `json:"base"`
	Files       map[string]string `json:"files"`
}

type Release struct {
	Target         string `json:"target"`
	Backend        string `json:"backend"`
	Format         string `json:"format"`
	Channel        string `json:"channel"`
	CoreVersion    string `json:"core_version"`
	Revision       int64  `json:"revision"`
	BaseSHA256     string `json:"base_sha256"`
	URL            string `json:"url"`
	ReleaseSummary string `json:"release_summary,omitempty"`
	Size           int64  `json:"size"`
	SHA256         string `json:"sha256"`
	Issued         int64  `json:"issued"`
	Expires        int64  `json:"expires"`
	Proof          string `json:"-"` // authenticated compact JWS retained for boot verification
}

type checkpoint struct {
	Revision int64  `json:"revision"`
	Issued   int64  `json:"issued"`
	Identity string `json:"identity"`
}

// Client is used serially. Directory is trusted private persistent storage.
// Installed provenance is immutable; desired channel is supplied separately.
type Client struct {
	BaseURL, Directory string
	Key                ed25519.PublicKey
	Installed          Manifest
	HTTP               *http.Client
}

var digest = regexp.MustCompile(`^[0-9a-f]{64}$`)
var slug = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func Channel(s string) bool { return s == "dev" || s == "stable" }
func validCore(s string) bool {
	return strings.Count(strings.SplitN(strings.SplitN(s, "+", 2)[0], "-", 2)[0], ".") == 2 && semver.IsValid("v"+s)
}
func Newer(candidate, installed string) bool {
	return validCore(candidate) && validCore(installed) && semver.Compare("v"+candidate, "v"+installed) > 0
}

// Check authenticates and records metadata even when the selected feed is behind
// installed. There is no switch-time publication watermark or channel fallback.
func (c *Client) Check(ctx context.Context, channel string, highest int64) (*Release, error) {
	i := c.Installed
	if !Channel(channel) || !slug.MatchString(i.Target) || !slug.MatchString(i.Backend) || !validCore(i.CoreVersion) || !digest.MatchString(i.BaseSHA256) || i.Schema != 1 {
		return nil, errors.New("invalid installed identity or desired channel")
	}
	if len(c.Key) != ed25519.PublicKeySize {
		return nil, errors.New("update trust not provisioned")
	}
	metadata, err := updateverify.FetchMetadata(ctx, c.HTTP, strings.TrimRight(c.BaseURL, "/")+"/"+i.Target+"/"+i.Backend+"/"+channel+".jws")
	if err != nil {
		return nil, err
	}
	r, err := VerifyRelease(metadata, c.Key, i, channel)
	if err != nil {
		return nil, err
	}
	if err := r.Fresh(); err != nil {
		return nil, err
	}
	// Bind immutable identity, not expiry/issue time: renewing a feed is allowed,
	// replacing an observed revision's bytes/core/base is not.
	identity, _ := json.Marshal([]any{r.Target, r.Backend, r.Format, r.CoreVersion, r.BaseSHA256, r.SHA256, r.Size})
	sum := sha256.Sum256(identity)
	current := checkpoint{r.Revision, r.Issued, hex.EncodeToString(sum[:])}
	path := filepath.Join(c.Directory, channel+".json")
	var old checkpoint
	b, err := os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(b, &old); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if r.Revision < old.Revision || r.Issued < old.Issued || (r.Revision == old.Revision && old.Identity != current.Identity) {
		return nil, errors.New("replayed or replaced release")
	}
	if err = os.MkdirAll(c.Directory, 0700); err != nil {
		return nil, err
	}
	b, _ = json.Marshal(current)
	if err = atomicWrite(path, b); err != nil {
		return nil, err
	}
	if r.Revision <= highest || r.Revision <= i.Revision || !Newer(r.CoreVersion, i.CoreVersion) {
		return nil, nil
	}
	return &r, nil
}

// VerifyRelease verifies immutable release identity. Feed freshness is checked
// at admission, not at boot: installed software must survive feed expiry and
// an unset RTC without needing to contact the network.
func VerifyRelease(b []byte, key ed25519.PublicKey, i Manifest, channel string) (Release, error) {
	var r Release
	payload, proof, err := updateverify.VerifyJWS(b, key, Header)
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal(payload, &r); err != nil {
		return r, err
	}
	if r.Target != i.Target || r.Backend != i.Backend || r.Format != i.Format || r.Channel != channel || r.BaseSHA256 != i.BaseSHA256 {
		return r, errors.New("incompatible target/backend/shared base; base migration unsupported")
	}
	if !validCore(r.CoreVersion) || r.Revision <= 0 || !digest.MatchString(r.SHA256) || r.Size <= 0 || r.Size > MaxBundle || !updateverify.SecureURL(r.URL) {
		return r, errors.New("invalid release metadata")
	}
	if channel == "stable" && semver.Prerelease("v"+r.CoreVersion) != "" {
		return r, errors.New("stable core must not be a prerelease")
	}
	r.Proof = proof
	return r, nil
}

func (r Release) Fresh() error {
	if !updateverify.Fresh(time.Now().Unix(), r.Issued, r.Expires) {
		return errors.New("expired or invalid metadata lifetime; check device clock")
	}
	return nil
}

// Download only returns a completely verified, fsynced private file. Callers
// recheck selection and slot eligibility before activation. If progress is
// non-nil, it receives cumulative bytes read, not a verification result.
func (c *Client) Download(ctx context.Context, r Release, progress func(int64)) (string, error) {
	if err := r.Fresh(); err != nil {
		return "", err
	}
	// The metadata timeout is too short for a large bundle on a slow link.
	// Keep a separate total budget and stop promptly on a stalled read.
	ctx, stop := context.WithTimeoutCause(ctx, 30*time.Minute, errors.New("firmware download exceeded 30 minutes"))
	defer stop()
	ctx, cancel := context.WithCancelCause(ctx)
	defer cancel(nil)
	var httpClient http.Client
	if c.HTTP != nil {
		httpClient = *c.HTTP
		httpClient.Timeout = 0
	}
	noResponse := errors.New("no firmware response within 30 seconds")
	timer := time.AfterFunc(downloadIdleTimeout, func() { cancel(noResponse) })
	resp, err := updateverify.Get(ctx, &httpClient, r.URL)
	if !timer.Stop() {
		cancel(noResponse)
	}
	if err != nil {
		if cause := context.Cause(ctx); cause != nil {
			return "", cause
		}
		// Do not expose a redirected signed URL in the device's update status.
		var requestErr *url.Error
		if errors.As(err, &requestErr) {
			err = requestErr.Err
		}
		return "", err
	}
	defer resp.Body.Close()
	f, err := os.CreateTemp(c.Directory, ".download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	body := downloadBody{Reader: resp.Body, cancel: cancel, progress: progress}
	match, e := updateverify.CopyAndMatch(f, &body, r.Size, r.SHA256)
	if e == nil && match && ctx.Err() == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if cause := context.Cause(ctx); cause != nil {
		e = cause
	}
	if e != nil {
		return "", fmt.Errorf("%d of %d bytes received: %w", body.received, r.Size, e)
	}
	if closeErr != nil {
		return "", closeErr
	}
	if !match {
		return "", errors.New("bundle size or digest mismatch")
	}
	path := filepath.Join(c.Directory, r.SHA256+".zip")
	if err = os.Rename(f.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}

const downloadIdleTimeout = 30 * time.Second

var errDownloadIdle = errors.New("no firmware bytes received for 30 seconds")

type downloadBody struct {
	io.Reader
	cancel   context.CancelCauseFunc
	progress func(int64)
	received int64
}

func (b *downloadBody) Read(p []byte) (int, error) {
	timer := time.AfterFunc(downloadIdleTimeout, func() { b.cancel(errDownloadIdle) })
	n, err := b.Reader.Read(p)
	if !timer.Stop() {
		b.cancel(errDownloadIdle)
	}
	if n > 0 {
		b.received += int64(n)
		if b.progress != nil {
			b.progress(b.received)
		}
	}
	return n, err
}

func atomicWrite(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".update-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	_, err = f.Write(b)
	if err == nil {
		err = f.Sync()
	}
	ce := f.Close()
	if err != nil {
		return err
	}
	if ce != nil {
		return ce
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(path))
}
func syncDir(path string) error {
	d, err := os.Open(path)
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
