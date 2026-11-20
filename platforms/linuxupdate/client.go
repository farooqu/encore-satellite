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
	"net/http"
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
	Proof       string `json:"-"` // authenticated compact JWS retained for boot verification
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
// recheck selection and slot eligibility before activation.
func (c *Client) Download(ctx context.Context, r Release) (string, error) {
	if err := r.Fresh(); err != nil {
		return "", err
	}
	resp, err := updateverify.Get(ctx, c.HTTP, r.URL)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	f, err := os.CreateTemp(c.Directory, ".download-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	match, e := updateverify.CopyAndMatch(f, resp.Body, r.Size, r.SHA256)
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
	if !match {
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
