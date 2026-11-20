// Package updateverify implements the common verification steps for signed
// software updates. Callers own release identity, eligibility, replay state,
// and durable storage and activation.
package updateverify

import (
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const maxMetadata = 32 * 1024

// SecureURL allows query-signed HTTPS URLs but not credentials or fragments.
func SecureURL(address string) bool {
	u, err := url.Parse(address)
	return err == nil && u.Scheme == "https" && u.Hostname() != "" && u.User == nil && u.Fragment == ""
}

// Get fetches an update resource, enforcing the HTTPS policy on redirects too.
// The caller closes the body on success. An optional client is copied, with its
// redirect policy replaced so it cannot relax the update policy.
func Get(ctx context.Context, client *http.Client, address string) (*http.Response, error) {
	if !SecureURL(address) {
		return nil, errors.New("updates require HTTPS without credentials or fragment")
	}
	h := http.Client{Timeout: 5 * time.Minute}
	if client != nil {
		h = *client
	}
	h.CheckRedirect = func(r *http.Request, via []*http.Request) error {
		if len(via) >= 5 || !SecureURL(r.URL.String()) {
			return errors.New("unsafe update redirect")
		}
		return nil
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, address, nil)
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

// FetchMetadata fetches signed release metadata and rejects bodies over 32 KiB.
func FetchMetadata(ctx context.Context, client *http.Client, address string) ([]byte, error) {
	resp, err := Get(ctx, client, address)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxMetadata+1))
	if err != nil || len(b) > maxMetadata {
		return nil, errors.New("invalid metadata size")
	}
	return b, nil
}

// VerifyJWS verifies compact EdDSA JWS against the caller's exact protected
// header and trusted key. It returns the decoded payload and authenticated
// compact proof, without checking the payload's identity or freshness.
func VerifyJWS(b []byte, key ed25519.PublicKey, expectedHeader string) ([]byte, string, error) {
	if len(b) > maxMetadata || len(key) != ed25519.PublicKeySize {
		return nil, "", errors.New("invalid metadata size or trust root")
	}
	proof := strings.TrimSpace(string(b))
	parts := strings.Split(proof, ".")
	if len(parts) != 3 {
		return nil, "", errors.New("invalid JWS")
	}
	header, e1 := base64.RawURLEncoding.DecodeString(parts[0])
	payload, e2 := base64.RawURLEncoding.DecodeString(parts[1])
	sig, e3 := base64.RawURLEncoding.DecodeString(parts[2])
	if e1 != nil || e2 != nil || e3 != nil || string(header) != expectedHeader || !ed25519.Verify(key, []byte(parts[0]+"."+parts[1]), sig) {
		return nil, "", errors.New("invalid update signature")
	}
	return payload, proof, nil
}

// Fresh reports whether the feed's Unix timestamps admit it at now, allowing
// five minutes of clock skew and at most seven days of validity.
func Fresh(now, issued, expires int64) bool {
	return issued > 0 && issued <= now+300 && expires > now && expires > issued && expires-issued <= 7*24*3600
}

// CopyAndMatch copies into private staging storage and checks the expected size
// and lowercase SHA-256. The caller supplies a validated size and must discard
// failed output. One extra byte is read to detect an oversized body. A mismatch
// is reported separately from I/O errors so the caller controls sync and close.
func CopyAndMatch(dst io.Writer, src io.Reader, size int64, digest string) (bool, error) {
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(dst, h), io.LimitReader(src, size+1))
	if err != nil {
		return false, err
	}
	return n == size && hex.EncodeToString(h.Sum(nil)) == digest, nil
}
