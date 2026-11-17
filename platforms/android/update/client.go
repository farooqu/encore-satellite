// Package update verifies Android release metadata before downloading an APK.
// Trust is provisioned in the APK, never supplied by ESPHome or the feed.
package update

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

const Header = `{"alg":"EdDSA","typ":"encore-android-update+jws"}`
const MaxAPK = 256 << 20

type Release struct {
	Target            string   `json:"target"`
	Backend           string   `json:"backend"`
	Channel           string   `json:"channel"`
	ApplicationID     string   `json:"application_id"`
	Revision          int64    `json:"revision"`
	Version           string   `json:"version"`
	CoreVersion       string   `json:"core_version"`
	Source            string   `json:"source"`
	Toolchain         string   `json:"toolchain"`
	MinSDK            int      `json:"min_sdk"`
	ABIs              []string `json:"abis"`
	URL               string   `json:"url"`
	Size              int64    `json:"size"`
	SHA256            string   `json:"sha256"`
	CertificateSHA256 string   `json:"certificate_sha256"`
	Issued            int64    `json:"issued"`
	Expires           int64    `json:"expires"`
}

type checkpoint struct {
	Revision int64  `json:"revision"`
	Issued   int64  `json:"issued"`
	SHA256   string `json:"sha256"`
}

// Client is used serially by the Android update worker. Its directory is private
// app storage. Metadata is short-lived; an untrusted/incorrect device clock fails
// closed rather than allowing indefinitely replayed metadata.
type Client struct {
	BaseURL, ApplicationID, CertificateSHA256, ABI, Directory string
	Installed                                                 int64
	InstalledCore                                             string
	SDK                                                       int
	Key                                                       ed25519.PublicKey
	HTTP                                                      *http.Client
}

func secureURL(value string) bool {
	u, err := url.Parse(value)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}

func (c *Client) get(ctx context.Context, address string) (*http.Response, error) {
	if !secureURL(address) {
		return nil, errors.New("updates require HTTPS")
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

var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)
var sourcePattern = regexp.MustCompile(`^[0-9a-f]{40}$`)

// Same SemVer grammar as the central release inventory.
var corePattern = regexp.MustCompile(`^(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*)(?:\.(?:0|[1-9][0-9]*|[0-9]*[A-Za-z-][0-9A-Za-z-]*))*)?(?:\+[0-9A-Za-z-]+(?:\.[0-9A-Za-z-]+)*)?$`)

// Check verifies a compact JWS (RFC 7515, EdDSA/RFC 8037), compatibility and a
// persistent per-channel replay checkpoint. A nil release means no upgrade.
func (c *Client) Check(ctx context.Context, channel string) (*Release, error) {
	if channel != "dev" && channel != "stable" {
		return nil, errors.New("invalid update channel")
	}
	if len(c.Key) != ed25519.PublicKeySize || !digestPattern.MatchString(c.CertificateSHA256) {
		return nil, errors.New("update trust not provisioned")
	}
	if !corePattern.MatchString(c.InstalledCore) {
		return nil, errors.New("installed core SemVer not provisioned")
	}
	resp, err := c.get(ctx, strings.TrimRight(c.BaseURL, "/")+"/android-generic/incubator/"+channel+".jws")
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, 32*1024+1))
	if err != nil || len(b) > 32*1024 {
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
	if err = json.Unmarshal(payload, &r); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if r.Target != "android-generic" || r.Backend != "incubator" || r.Channel != channel || r.ApplicationID != c.ApplicationID || r.CertificateSHA256 != c.CertificateSHA256 {
		return nil, errors.New("update identity mismatch")
	}
	if r.Revision <= 0 || r.Revision > 2100000000 || r.Version == "" || !corePattern.MatchString(r.CoreVersion) || r.Toolchain == "" || !sourcePattern.MatchString(r.Source) || !digestPattern.MatchString(r.SHA256) || !secureURL(r.URL) || r.Size <= 0 || r.Size > MaxAPK {
		return nil, errors.New("invalid release metadata")
	}
	if channel == "stable" && strings.Contains(strings.SplitN(r.CoreVersion, "+", 2)[0], "-") {
		return nil, errors.New("stable core must not be a prerelease")
	}
	if r.Issued <= 0 || r.Issued > now+300 || r.Expires <= now || r.Expires <= r.Issued || r.Expires-r.Issued > int64((7*24*time.Hour)/time.Second) {
		return nil, errors.New("expired or invalid metadata lifetime")
	}
	compatible := false
	for _, abi := range r.ABIs {
		if abi == c.ABI {
			compatible = true
		}
	}
	if r.MinSDK < 26 || r.MinSDK > c.SDK || !compatible {
		return nil, errors.New("incompatible Android release")
	}
	path := filepath.Join(c.Directory, channel+".json")
	var previous checkpoint
	b, err = os.ReadFile(path)
	if err == nil {
		if err = json.Unmarshal(b, &previous); err != nil {
			return nil, err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	if r.Revision < previous.Revision || r.Issued < previous.Issued || (r.Revision == previous.Revision && r.SHA256 != previous.SHA256) {
		return nil, errors.New("replayed or replaced release")
	}
	if err = os.MkdirAll(c.Directory, 0700); err != nil {
		return nil, err
	}
	b, _ = json.Marshal(checkpoint{r.Revision, r.Issued, r.SHA256})
	if err = os.WriteFile(path+".tmp", b, 0600); err != nil {
		return nil, err
	}
	if err = os.Rename(path+".tmp", path); err != nil {
		return nil, err
	}
	if r.Revision <= c.Installed || semver.Compare("v"+r.CoreVersion, "v"+c.InstalledCore) <= 0 {
		return nil, nil
	}
	return &r, nil
}

// Download always rechecks the selected channel. Interrupted/unverified bytes
// are removed; only the hash-verified file is handed to Android's package parser.
func (c *Client) Download(ctx context.Context, channel string) (*Release, string, error) {
	r, err := c.Check(ctx, channel)
	if err != nil {
		return nil, "", err
	}
	if r == nil {
		return nil, "", errors.New("no newer package")
	}
	resp, err := c.get(ctx, r.URL)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	path := filepath.Join(c.Directory, r.SHA256+".apk")
	f, err := os.OpenFile(path+".part", os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		return nil, "", err
	}
	defer os.Remove(path + ".part")
	h := sha256.New()
	n, copyErr := io.Copy(io.MultiWriter(f, h), io.LimitReader(resp.Body, r.Size+1))
	closeErr := f.Close()
	if copyErr != nil {
		return nil, "", copyErr
	}
	if closeErr != nil {
		return nil, "", closeErr
	}
	if n != r.Size || hex.EncodeToString(h.Sum(nil)) != r.SHA256 {
		return nil, "", errors.New("APK size or digest mismatch")
	}
	if err = os.Rename(path+".part", path); err != nil {
		return nil, "", err
	}
	return r, path, nil
}
