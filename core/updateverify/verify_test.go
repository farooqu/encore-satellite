package updateverify_test

import (
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/farooqu/encore-satellite/core/updateverify"
)

func TestFetchMetadataSize(t *testing.T) {
	const max = 32 * 1024
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := max
		if r.URL.Path == "/oversized" {
			n++
		}
		io.WriteString(w, strings.Repeat("x", n))
	}))
	defer srv.Close()
	if b, err := updateverify.FetchMetadata(t.Context(), srv.Client(), srv.URL); err != nil || len(b) != max {
		t.Fatalf("metadata at limit: got %d bytes, err %v", len(b), err)
	}
	if b, err := updateverify.FetchMetadata(t.Context(), srv.Client(), srv.URL+"/oversized"); err == nil || b != nil {
		t.Fatalf("oversized metadata: got %d bytes, err %v", len(b), err)
	}
}

func TestVerifyJWS(t *testing.T) {
	const header = `{"alg":"EdDSA","typ":"first-update+jws"}`
	const otherHeader = `{"alg":"EdDSA","typ":"second-update+jws"}`
	const body = `{"release":42}`
	public, private, err := ed25519.GenerateKey(nil)
	if err != nil {
		t.Fatal(err)
	}
	encode := base64.RawURLEncoding.EncodeToString
	input := encode([]byte(header)) + "." + encode([]byte(body))
	proof := input + "." + encode(ed25519.Sign(private, []byte(input)))
	withWhitespace := " \n" + proof + "\n"
	payload, verified, err := updateverify.VerifyJWS([]byte(withWhitespace), public, header)
	if err != nil || string(payload) != body || verified != proof {
		t.Fatalf("valid proof: payload=%q proof=%q err=%v", payload, verified, err)
	}
	for _, tc := range []struct {
		name, compact, expected string
		key                     ed25519.PublicKey
	}{
		{"wrong header", proof, otherHeader, public},
		{"invalid key size", proof, header, public[:1]},
		{"invalid signature", input + "." + encode(make([]byte, ed25519.SignatureSize)), header, public},
		{"extra segment", proof + ".", header, public},
		{"oversized offline proof", proof + strings.Repeat(" ", 32*1024-len(proof)+1), header, public},
	} {
		t.Run(tc.name, func(t *testing.T) {
			payload, verified, err := updateverify.VerifyJWS([]byte(tc.compact), tc.key, tc.expected)
			if err == nil || payload != nil || verified != "" {
				t.Fatal("invalid proof returned authenticated data")
			}
		})
	}
}

func TestFreshBoundaries(t *testing.T) {
	const now = 1_700_000_000
	const week = 7 * 24 * 3600
	for _, tc := range []struct {
		name            string
		issued, expires int64
		want            bool
	}{
		{"at future skew and max lifetime", now + 300, now + 300 + week, true},
		{"past future skew", now + 301, now + 302, false},
		{"past max lifetime", now, now + week + 1, false},
		{"expires now", now - 1, now, false},
		{"expires after now", now - 1, now + 1, true},
		{"expires at issue", now + 1, now + 1, false},
		{"unset issue", 0, now + 1, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := updateverify.Fresh(now, tc.issued, tc.expires); got != tc.want {
				t.Fatalf("Fresh() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestCopyAndMatch(t *testing.T) {
	const body = "test download"
	digest := sha256.Sum256([]byte(body))
	for _, tc := range []struct {
		name, source string
		wantBytes    int
		wantMatch    bool
	}{
		{"matching", body, len(body), true},
		{"short", body[:len(body)-1], len(body) - 1, false},
		{"oversized", body + "extra bytes", len(body) + 1, false},
		{"corrupt", "x" + body[1:], len(body), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var dst strings.Builder
			match, err := updateverify.CopyAndMatch(&dst, strings.NewReader(tc.source), int64(len(body)), hex.EncodeToString(digest[:]))
			if err != nil || match != tc.wantMatch || dst.String() != tc.source[:tc.wantBytes] {
				t.Fatalf("copy: got %q, match=%t, err=%v", dst.String(), match, err)
			}
		})
	}
}
