package nativeab

import (
	"archive/zip"
	"bytes"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/platforms/linuxupdate"
)

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

func reject(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		t.Fatal("unexpected acceptance")
	}
}

func TestSignedNativeInstallOnlyActivatesVerifiedInactivePair(t *testing.T) {
	if LayoutID != fmt.Sprintf("%x", sha256.Sum256([]byte(Format))) {
		t.Fatal("native layout ID differs from the release packager's protocol digest")
	}
	for _, running := range []string{"a", "b"} {
		t.Run(running, func(t *testing.T) {
			dir := t.TempDir()
			active := int(running[0] - 'a')
			next := 1 - active
			peer := string("ab"[next])
			for _, slot := range []string{running, peer} {
				check(t, os.MkdirAll(filepath.Join(dir, "slots", slot), 0700))
				check(t, os.WriteFile(filepath.Join(dir, "slots", slot, "rootfs.tar.gz"), []byte("old "+slot), 0600))
			}
			check(t, os.WriteFile(filepath.Join(dir, "highest-revision"), []byte("14\n"), 0600))
			misc := make([]byte, 1536)
			copy(misc[0x360:], "\x00ABB\x01")
			misc[512+slotOffset+active], misc[512+slotOffset+next] = 0x8f, 0x8e
			misc[511], misc[512], misc[1023], misc[1024] = 11, 17, 23, 31
			for name, data := range map[string][]byte{"slot": []byte(running), "8": misc, "10": []byte("old boot A"), "11": []byte("old boot B")} {
				check(t, os.WriteFile(filepath.Join(dir, name), data, 0600))
			}
			read := func(name string) []byte {
				t.Helper()
				b, err := os.ReadFile(filepath.Join(dir, name))
				check(t, err)
				return b
			}
			boot := [2][]byte{append([]byte("ANDROID!"), bytes.Repeat([]byte{'A'}, 2048)...), append([]byte("ANDROID!"), bytes.Repeat([]byte{'B'}, 2048)...)}
			archive := []byte("new runtime")
			var raw bytes.Buffer
			z := zip.NewWriter(&raw)
			for name, content := range map[string][]byte{"boot_a.img": boot[0], "boot_b.img": boot[1], "rootfs.tar.gz": archive} {
				w, err := z.Create(name)
				check(t, err)
				_, err = w.Write(content)
				check(t, err)
			}
			check(t, z.Close())
			bundle := filepath.Join(dir, "bundle.zip")
			check(t, os.WriteFile(bundle, raw.Bytes(), 0600))
			pub, key, err := ed25519.GenerateKey(nil)
			check(t, err)
			m := linuxupdate.Manifest{Schema: 1, Target: Target, Backend: "incubator", Format: Format, BaseSHA256: LayoutID, CoreVersion: "1.2.3", Revision: 14}
			now := time.Now().Unix()
			r := linuxupdate.Release{Target: Target, Backend: m.Backend, Format: Format, BaseSHA256: LayoutID, Channel: "stable", CoreVersion: "1.3.0", Revision: 32,
				URL: "https://releases.example.com/ota.zip", Size: int64(raw.Len()), SHA256: fmt.Sprintf("%x", sha256.Sum256(raw.Bytes())), Issued: now - 60, Expires: now + 3600}
			b, err := json.Marshal(r)
			check(t, err)
			encode := base64.RawURLEncoding.EncodeToString
			msg := encode([]byte(linuxupdate.Header)) + "." + encode(b)
			proof := msg + "." + encode(ed25519.Sign(key, []byte(msg)))

			bootWritable := false
			s := Store{dir, filepath.Join(dir, "slot"), func(part, flags int) (*os.File, error) {
				if part == 10+active || (part == 10+next && !bootWritable) {
					return nil, errors.New("simulated boot store access failure")
				}
				return os.OpenFile(filepath.Join(dir, fmt.Sprint(part)), flags, 0)
			}}
			reject(t, s.Install(m, pub, "dev", proof, bundle))
			if !bytes.Equal(read("8"), misc) {
				t.Fatal("invalid channel touched boot control")
			}
			reject(t, s.Install(m, pub, "stable", proof, bundle))
			misc[512+slotOffset+next] = 0
			if !bytes.Equal(read("8"), misc) || string(read("highest-revision")) != "14\n" {
				t.Fatal("incomplete write armed trial or advanced revision")
			}

			bootWritable = true
			check(t, s.Install(m, pub, "stable", proof, bundle))
			misc[512+slotOffset+active], misc[512+slotOffset+next] = 0x8e, 0x2f
			if !bytes.Equal(read("8"), misc) || string(read("highest-revision")) != "32\n" ||
				!bytes.Equal(read(fmt.Sprint(10+next)), boot[next]) || string(read(fmt.Sprint(10+active))) != "old boot "+string("AB"[active]) ||
				!bytes.Equal(read("slots/"+peer+"/rootfs.tar.gz"), archive) || string(read("slots/"+running+"/rootfs.tar.gz")) != "old "+running {
				t.Fatal("activation failed to preserve the running pair or arm the verified inactive pair")
			}
			reject(t, s.Confirm()) // cannot confirm B from A (or vice versa)
			reject(t, s.Install(m, pub, "stable", proof, bundle))
			misc[512+slotOffset+next] = 0x1f // loader consumed one attempt
			check(t, os.WriteFile(filepath.Join(dir, "8"), misc, 0600))
			check(t, os.WriteFile(s.booted, []byte(peer), 0600))
			trial, err := s.Trial()
			check(t, err)
			if !trial {
				t.Fatal("armed slot did not require trial health")
			}
			check(t, s.Confirm())
			misc[512+slotOffset+next] = 0x8f
			if !bytes.Equal(read("8"), misc) {
				t.Fatal("confirmation changed unexpected bytes or removed last-good fallback")
			}
		})
	}
}
