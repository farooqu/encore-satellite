// Package linuxupdate verifies signed Linux application-slot updates. It never
// migrates a kernel/base or fetches trust from the network.
package linuxupdate

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
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
	Target      string `json:"target"`
	Backend     string `json:"backend"`
	Format      string `json:"format"`
	Channel     string `json:"channel"`
	CoreVersion string `json:"core_version"`
	Revision    int64  `json:"revision"`
	BaseSHA256  string `json:"base_sha256"`
	URL         string `json:"url"`
	Size        int64  `json:"size"`
	SHA256      string `json:"sha256"`
	Issued      int64  `json:"issued"`
	Expires     int64  `json:"expires"`
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
func secureURL(s string) bool {
	u, err := url.Parse(s)
	// Release hosts may redirect to temporary, query-signed HTTPS URLs.
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}
func (c *Client) get(ctx context.Context, address string) (*http.Response, error) {
	if !secureURL(address) {
		return nil, errors.New("updates require HTTPS without credentials or fragment")
	}
	h := http.Client{Timeout: 5 * time.Minute}
	if c.HTTP != nil {
		h = *c.HTTP
	}
	h.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !secureURL(r.URL.String()) {
			return errors.New("unsafe update redirect")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, "GET", address, nil)
	if err != nil {
		return nil, err
	}
	resp, err := h.Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("update HTTP %d", resp.StatusCode)
	}
	return resp, nil
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
	resp, err := c.get(ctx, strings.TrimRight(c.BaseURL, "/")+"/"+i.Target+"/"+i.Backend+"/"+channel+".jws")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32769))
	if err != nil || len(b) > 32768 {
		return nil, errors.New("invalid metadata size")
	}
	parts := strings.Split(strings.TrimSpace(string(b)), ".")
	if len(parts) != 3 {
		return nil, errors.New("invalid JWS")
	}
	header, e1 := base64.RawURLEncoding.DecodeString(parts[0])
	payload, e2 := base64.RawURLEncoding.DecodeString(parts[1])
	sig, e3 := base64.RawURLEncoding.DecodeString(parts[2])
	if e1 != nil || e2 != nil || e3 != nil || string(header) != Header || !ed25519.Verify(c.Key, []byte(parts[0]+"."+parts[1]), sig) {
		return nil, errors.New("invalid update signature")
	}
	var r Release
	if err := json.Unmarshal(payload, &r); err != nil {
		return nil, err
	}
	if r.Target != i.Target || r.Backend != i.Backend || r.Format != i.Format || r.Channel != channel || r.BaseSHA256 != i.BaseSHA256 {
		return nil, errors.New("incompatible target/backend/shared base; base migration unsupported")
	}
	if !validCore(r.CoreVersion) || r.Revision <= 0 || !digest.MatchString(r.SHA256) || r.Size <= 0 || r.Size > MaxBundle || !secureURL(r.URL) {
		return nil, errors.New("invalid release metadata")
	}
	if channel == "stable" && semver.Prerelease("v"+r.CoreVersion) != "" {
		return nil, errors.New("stable core must not be a prerelease")
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
	b, err = os.ReadFile(path)
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

func (r Release) Fresh() error {
	now := time.Now().Unix()
	if r.Issued <= 0 || r.Issued > now+300 || r.Expires <= now || r.Expires <= r.Issued || r.Expires-r.Issued > 7*24*3600 {
		return errors.New("expired or invalid metadata lifetime; check device clock")
	}
	return nil
}

// Download only returns a completely verified, fsynced private file. Callers
// recheck selection and slot eligibility before activation.
func (c *Client) Download(ctx context.Context, r Release) (string, error) {
	if err := r.Fresh(); err != nil {
		return "", err
	}
	resp, err := c.get(ctx, r.URL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	f, err := os.CreateTemp(c.Directory, ".download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	h := sha256.New()
	n, e := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, r.Size+1))
	if e == nil {
		e = f.Sync()
	}
	closeErr := f.Close()
	if e != nil {
		return "", e
	}
	if closeErr != nil {
		return "", closeErr
	}
	if n != r.Size || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return "", errors.New("bundle size or digest mismatch")
	}
	path := filepath.Join(c.Directory, r.SHA256+".zip")
	if err = os.Rename(f.Name(), path); err != nil {
		return "", err
	}
	return path, nil
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
