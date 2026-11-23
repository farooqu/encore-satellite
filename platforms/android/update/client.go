// Package update verifies Android release metadata before downloading an APK.
// Trust is provisioned in the APK, never supplied by ESPHome or the feed.
package update

import (
	"context"
	"crypto/ed25519"
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
	DisplayVersion    string   `json:"display_version,omitempty"`
	Source            string   `json:"source"`
	Toolchain         string   `json:"toolchain"`
	MinSDK            int      `json:"min_sdk"`
	ABIs              []string `json:"abis"`
	URL               string   `json:"url"`
	ReleaseSummary    string   `json:"release_summary,omitempty"`
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
	metadata, err := updateverify.FetchMetadata(ctx, c.HTTP, strings.TrimRight(c.BaseURL, "/")+"/android-generic/incubator/"+channel+".jws")
	if err != nil {
		return nil, err
	}
	payload, _, err := updateverify.VerifyJWS(metadata, c.Key, Header)
	if err != nil {
		return nil, err
	}
	var r Release
	if err = json.Unmarshal(payload, &r); err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	if r.Target != "android-generic" || r.Backend != "incubator" || r.Channel != channel || r.ApplicationID != c.ApplicationID || r.CertificateSHA256 != c.CertificateSHA256 {
		return nil, errors.New("update identity mismatch")
	}
	if r.Revision <= 0 || r.Revision > 2100000000 || r.Version == "" || !corePattern.MatchString(r.CoreVersion) || r.Toolchain == "" || !sourcePattern.MatchString(r.Source) || !digestPattern.MatchString(r.SHA256) || !updateverify.SecureURL(r.URL) || r.Size <= 0 || r.Size > MaxAPK {
		return nil, errors.New("invalid release metadata")
	}
	if channel == "stable" && strings.Contains(strings.SplitN(r.CoreVersion, "+", 2)[0], "-") {
		return nil, errors.New("stable core must not be a prerelease")
	}
	if r.DisplayVersion != "" && (!corePattern.MatchString(r.DisplayVersion) || (channel == "stable" && semver.Prerelease("v"+r.DisplayVersion) != "")) {
		return nil, errors.New("invalid release display version")
	}
	if !updateverify.Fresh(now, r.Issued, r.Expires) {
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
	b, err := os.ReadFile(path)
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
	resp, err := updateverify.Get(ctx, c.HTTP, r.URL)
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
	match, copyErr := updateverify.CopyAndMatch(f, resp.Body, r.Size, r.SHA256)
	closeErr := f.Close()
	if copyErr != nil {
		return nil, "", copyErr
	}
	if closeErr != nil {
		return nil, "", closeErr
	}
	if !match {
		return nil, "", errors.New("APK size or digest mismatch")
	}
	if err = os.Rename(path+".part", path); err != nil {
		return nil, "", err
	}
	return r, path, nil
}
