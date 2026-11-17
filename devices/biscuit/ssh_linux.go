package biscuit

// SSH is opt-in rescue access, independent of the voice process. The init-owned
// supervisor consumes these files; none of the management methods starts sshd.
import (
	"bytes"
	"crypto/rsa"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"golang.org/x/crypto/ssh"
)

const sshDataDir = "/data/encore/ssh"
const sshRunDir = "/run/encore/ssh"
const sshMaxKeys = 16
const sshMaxFile = 32768
const sshMaxKey = 2048

type sshStatus struct {
	Enabled      bool     `json:"enabled"`
	Running      bool     `json:"running"`
	Fingerprints []string `json:"fingerprints"`
}

type sshManager struct{ dataDir, runDir string }

func newSSHManager() *sshManager { return &sshManager{sshDataDir, sshRunDir} }

// Accepted input: exactly one OpenSSH public key, optional comment and one
// terminal LF. Ed25519 and RSA 3072..8192 bits (odd exponent <=24 bits) only.
// Options, certificates, private keys, noncanonical wire encodings and embedded
// newlines are rejected. Comments never reach authorized_keys.
func normalizeSSHKey(input string) (string, string, error) {
	bad := errors.New("dot: expected one Ed25519 or RSA 3072..8192 public key without options")
	if len(input) > sshMaxKey {
		return "", "", bad
	}
	input = strings.TrimSuffix(input, "\n")
	if strings.ContainsAny(input, "\r\n\x00") {
		return "", "", bad
	}
	for _, c := range input {
		if c < 32 && c != '\t' || c > 126 {
			return "", "", bad
		}
	}
	f := strings.Fields(input)
	if len(f) < 2 || (f[0] != "ssh-ed25519" && f[0] != "ssh-rsa") {
		return "", "", bad
	}
	wire, err := base64.StdEncoding.Strict().DecodeString(f[1])
	if err != nil {
		return "", "", bad
	}
	k, err := ssh.ParsePublicKey(wire)
	if err != nil || k.Type() != f[0] || !bytes.Equal(k.Marshal(), wire) {
		return "", "", bad
	}
	if k.Type() == "ssh-rsa" {
		r := k.(ssh.CryptoPublicKey).CryptoPublicKey().(*rsa.PublicKey)
		if r.N.Sign() <= 0 || r.N.BitLen() < 3072 || r.N.BitLen() > 8192 || r.N.Bit(0) == 0 {
			return "", "", bad
		}
	}
	return strings.TrimSuffix(string(ssh.MarshalAuthorizedKey(k)), "\n"), ssh.FingerprintSHA256(k), nil
}

func sshRead(path string, limit int) ([]byte, error) {
	// Do not follow a symlink or block forever opening a nonregular state file.
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > int64(limit) {
		return nil, errors.New("dot: invalid SSH state file")
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(limit+1)))
	if len(b) > limit {
		return nil, errors.New("dot: SSH state file too large")
	}
	return b, err
}

func (s *sshManager) keys() ([]string, []string, error) {
	b, err := sshRead(filepath.Join(s.dataDir, "authorized_keys"), sshMaxFile)
	keys, prints := []string{}, []string{}
	if err != nil || len(b) == 0 {
		return keys, prints, err
	}
	for _, line := range strings.Split(strings.TrimSuffix(string(b), "\n"), "\n") {
		key, print, err := normalizeSSHKey(line)
		if err != nil || key != line || len(keys) == sshMaxKeys {
			return nil, nil, errors.New("dot: invalid persisted SSH keys")
		}
		for _, p := range prints {
			if p == print {
				return nil, nil, errors.New("dot: duplicate persisted SSH key")
			}
		}
		keys, prints = append(keys, key), append(prints, print)
	}
	return keys, prints, nil
}

func (s *sshManager) enabled() (bool, error) {
	b, err := sshRead(filepath.Join(s.dataDir, "enabled"), 2)
	if err != nil {
		return false, err
	}
	if len(b) != 0 && string(b) != "1\n" {
		return false, errors.New("dot: invalid SSH enabled setting")
	}
	return string(b) == "1\n", nil
}

// A PID is not an identity. Require Linux starttime, executable and exact argv
// recorded by the supervisor, so stale/reused PID files never report running.
func (s *sshManager) running() bool {
	b, err := sshRead(filepath.Join(s.runDir, "running"), 128)
	if err != nil {
		return false
	}
	f := strings.Fields(string(b))
	if len(f) != 2 {
		return false
	}
	pid, err := strconv.Atoi(f[0])
	if err != nil || pid <= 1 {
		return false
	}
	stat, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
	if err != nil {
		return false
	}
	i := bytes.LastIndexByte(stat, ')')
	if i < 0 {
		return false
	}
	fields := strings.Fields(string(stat[i+1:]))
	if len(fields) < 20 || fields[0] == "Z" || fields[19] != f[1] {
		return false
	}
	exe, err := os.Readlink(fmt.Sprintf("/proc/%d/exe", pid))
	if err != nil || exe != "/usr/sbin/dropbear" {
		return false
	}
	cmd, err := os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid))
	if err != nil {
		return false
	}
	want := []string{"/usr/sbin/dropbear", "-F", "-E", "-s", "-g", "-j", "-k", "-T", "3", "-I", "300", "-r", filepath.Join(s.dataDir, "host_ed25519"), "-P", filepath.Join(s.runDir, "dropbear.pid")}
	return bytes.Equal(cmd, []byte(strings.Join(want, "\x00")+"\x00"))
}

func (s *sshManager) status() (sshStatus, error) {
	var result sshStatus
	err := s.locked(func() error {
		var err error
		result.Enabled, err = s.enabled()
		if err != nil {
			return err
		}
		_, result.Fingerprints, err = s.keys()
		result.Running = s.running()
		return err
	})
	return result, err
}

func (s *sshManager) locked(fn func() error) error {
	for _, dir := range []string{s.dataDir, s.runDir} {
		if err := os.MkdirAll(dir, 0700); err != nil {
			return err
		}
		if err := os.Chmod(dir, 0700); err != nil {
			return err
		}
	}
	f, err := os.OpenFile(filepath.Join(s.runDir, "lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		return err
	}
	defer syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
	return fn()
}

func sshAtomic(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".ssh-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(data); err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err != nil {
		return err
	}
	if closeErr != nil {
		return closeErr
	}
	if err = os.Rename(f.Name(), path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}

func (s *sshManager) setEnabled(enabled bool) error {
	err := s.locked(func() error {
		if enabled {
			keys, _, err := s.keys()
			if err != nil {
				return err
			}
			if len(keys) == 0 {
				return errors.New("dot: add a public key before enabling SSH")
			}
			return sshAtomic(filepath.Join(s.dataDir, "enabled"), []byte("1\n"))
		}
		// Empty is explicitly disabled. Remove the old acknowledgement under
		// the shared lock; only the supervisor may acknowledge session teardown.
		if err := os.Remove(filepath.Join(s.runDir, "stopped")); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		return sshAtomic(filepath.Join(s.dataDir, "enabled"), nil)
	})
	if err != nil || enabled {
		return err
	}
	for deadline := time.Now().Add(10 * time.Second); time.Now().Before(deadline); {
		if _, err := os.Stat(filepath.Join(s.runDir, "stopped")); err == nil && !s.running() {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return errors.New("dot: SSH disabled persistently but supervisor has not confirmed session termination")
}

func (s *sshManager) addKey(publicKey string) error {
	key, print, err := normalizeSSHKey(publicKey)
	if err != nil {
		return err
	}
	return s.locked(func() error {
		keys, prints, err := s.keys()
		if err != nil {
			return err
		}
		for _, p := range prints {
			if p == print {
				return nil
			}
		}
		if len(keys) == sshMaxKeys {
			return errors.New("dot: SSH key limit reached")
		}
		return sshAtomic(filepath.Join(s.dataDir, "authorized_keys"), []byte(strings.Join(append(keys, key), "\n")+"\n"))
	})
}

func (s *sshManager) removeKey(fingerprint string) error {
	return s.locked(func() error {
		keys, prints, err := s.keys()
		if err != nil {
			return err
		}
		enabled, err := s.enabled()
		if err != nil {
			return err
		}
		for i, p := range prints {
			if p != fingerprint {
				continue
			}
			if enabled && len(keys) == 1 {
				return errors.New("dot: cannot remove the last key while SSH is enabled")
			}
			keys = append(keys[:i], keys[i+1:]...)
			var b []byte
			if len(keys) > 0 {
				b = []byte(strings.Join(keys, "\n") + "\n")
			}
			return sshAtomic(filepath.Join(s.dataDir, "authorized_keys"), b)
		}
		return errors.New("dot: SSH fingerprint not found")
	})
}
