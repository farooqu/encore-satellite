package dot

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"encoding/base64"
	"math/big"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"golang.org/x/crypto/ssh"
)

func sshTestKey(t *testing.T) string {
	t.Helper()
	pub, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	k, err := ssh.NewPublicKey(pub)
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k)))
}

func TestSSHKeyValidation(t *testing.T) {
	key := sshTestKey(t)
	normalized, fingerprint, err := normalizeSSHKey(key + " owner comment\n")
	if err != nil || normalized != key || !strings.HasPrefix(fingerprint, "SHA256:") {
		t.Fatal("normalization failed", err)
	}
	for _, bad := range []string{"", key + "\n" + key, key + "\n\n", key + "\r\n", key + "\x00", "restrict " + key, "command=\"sh\" " + key, "-----BEGIN OPENSSH PRIVATE KEY-----", "ssh-ed25519 AAAA", strings.Repeat("x", sshMaxKey+1)} {
		if _, _, err := normalizeSSHKey(bad); err == nil {
			t.Error("accepted invalid key input")
		}
	}
	for _, bits := range []int{2048, 3071, 3072, 8192, 8193} {
		// Public wire fixtures need no expensive prime generation or private keys.
		n := new(big.Int).Lsh(big.NewInt(1), uint(bits-1))
		n.SetBit(n, 0, 1)
		k, err := ssh.NewPublicKey(&rsa.PublicKey{N: n, E: 65537})
		if err != nil {
			t.Fatal(err)
		}
		_, _, err = normalizeSSHKey(strings.TrimSpace(string(ssh.MarshalAuthorizedKey(k))))
		if (err == nil) != (bits >= 3072 && bits <= 8192) {
			t.Errorf("RSA %d: %v", bits, err)
		}
	}
	k := strings.Fields(key)
	wire, _ := base64.StdEncoding.DecodeString(k[1])
	wire = append(wire, 0)
	if _, _, err := normalizeSSHKey(k[0] + " " + base64.StdEncoding.EncodeToString(wire)); err == nil {
		t.Error("accepted trailing wire data")
	}
}

func TestSSHManagerPersistenceRotationAndDisable(t *testing.T) {
	root := t.TempDir()
	s := &sshManager{filepath.Join(root, "data"), filepath.Join(root, "run")}
	st, err := s.status()
	if err != nil || st.Enabled || st.Running || len(st.Fingerprints) != 0 {
		t.Fatal(st, err)
	}
	if err := s.setEnabled(true); err == nil {
		t.Fatal("enabled without a key")
	}
	a, b := sshTestKey(t), sshTestKey(t)
	if err := s.addKey(a + " comment\n"); err != nil {
		t.Fatal(err)
	}
	if err := s.addKey(a); err != nil {
		t.Fatal(err)
	}
	if err := s.setEnabled(true); err != nil {
		t.Fatal(err)
	}
	st, err = s.status()
	if err != nil || !st.Enabled || st.Running || len(st.Fingerprints) != 1 {
		t.Fatal(st, err)
	}
	if err := s.removeKey(st.Fingerprints[0]); err == nil {
		t.Fatal("removed last enabled key")
	}
	if err := s.addKey(b); err != nil {
		t.Fatal(err)
	}
	if err := s.removeKey(st.Fingerprints[0]); err != nil {
		t.Fatal(err)
	}
	s = &sshManager{s.dataDir, s.runDir} // a new voice runtime reads persistent state
	st, err = s.status()
	if err != nil || !st.Enabled || len(st.Fingerprints) != 1 {
		t.Fatal(st, err)
	}
	for _, path := range []string{s.dataDir, s.runDir, filepath.Join(s.dataDir, "authorized_keys"), filepath.Join(s.dataDir, "enabled")} {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm()&0077 != 0 {
			t.Fatal("permissive state", path, err)
		}
	}
	// Simulate only the init supervisor's disabled acknowledgement, not a daemon.
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 100; i++ {
			b, _ := os.ReadFile(filepath.Join(s.dataDir, "enabled"))
			if len(b) == 0 {
				_ = os.WriteFile(filepath.Join(s.runDir, "stopped"), nil, 0600)
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
	}()
	if err := s.setEnabled(false); err != nil {
		t.Fatal(err)
	}
	<-done
	if err := s.removeKey(st.Fingerprints[0]); err != nil {
		t.Fatal(err)
	}
	st, err = s.status()
	if err != nil || st.Enabled || len(st.Fingerprints) != 0 {
		t.Fatal(st, err)
	}
}

func TestSSHBoundedStateStalePIDAndConcurrentAdds(t *testing.T) {
	root := t.TempDir()
	s := &sshManager{filepath.Join(root, "data"), filepath.Join(root, "run")}
	var wg sync.WaitGroup
	for i := 0; i < sshMaxKeys; i++ {
		key := sshTestKey(t)
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.addKey(key); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if err := s.addKey(sshTestKey(t)); err == nil {
		t.Error("exceeded key limit")
	}
	if err := os.WriteFile(filepath.Join(s.runDir, "running"), []byte("1 1\n"), 0600); err != nil {
		t.Fatal(err)
	}
	st, err := s.status()
	if err != nil || st.Running || len(st.Fingerprints) != sshMaxKeys {
		t.Fatal(st, err)
	}
	if err := os.WriteFile(filepath.Join(s.dataDir, "authorized_keys"), []byte(strings.Repeat("x", sshMaxFile+1)), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := s.status(); err == nil {
		t.Error("accepted oversized persisted state")
	}
	if err := s.setEnabled(true); err == nil {
		t.Error("enabled corrupt state")
	}
	path := filepath.Join(s.dataDir, "authorized_keys")
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(s.dataDir, "enabled"), path); err != nil {
		t.Fatal(err)
	}
	if _, err := s.status(); err == nil {
		t.Error("accepted symlinked persisted keys")
	}
	pid := strconv.Itoa(os.Getpid())
	stat, err := os.ReadFile("/proc/" + pid + "/stat")
	if err != nil {
		t.Fatal(err)
	}
	fields := strings.Fields(string(stat[strings.LastIndex(string(stat), ")")+1:]))
	if err := os.WriteFile(filepath.Join(s.runDir, "running"), []byte(pid+" "+fields[19]+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if s.running() {
		t.Error("trusted a live, identity-matched PID that is not Dropbear")
	}
}
