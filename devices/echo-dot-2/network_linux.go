package dot

// The voice process is only an IPC client. The independently supervised worker
// owns network changes; its flock and generation fence requests across restarts.
import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/farooqu/encore-satellite/core/improv"
	"golang.org/x/sys/unix"
)

type wifiStatus struct {
	ID      string `json:"id"`
	State   string `json:"state"`
	SSID    string `json:"ssid"`
	Address string `json:"address"`
	Error   string `json:"error"`
}

type wifiRecord struct {
	wifiStatus
	Generation string    `json:"generation"`
	Deadline   time.Time `json:"deadline"`
	Start      time.Time `json:"start"`
	Cancel     bool      `json:"cancel"`
	Renewed    bool      `json:"renewed"`
}

// apply must release the existing DHCP lease and clear the old address before
// reconfiguring supplicant. renew clears once more after candidate association.
// observe is read-only, including when called from an API process at confirm.
type wifiAdapter interface {
	apply(context.Context, []byte) error
	renew(context.Context) error
	observe(context.Context) (ssid, address string, err error)
}

type wifiManager struct {
	dir, saved                      string
	adapter                         wifiAdapter
	grace, join, confirmation, poll time.Duration
}

func newWiFiManager() *wifiManager {
	return &wifiManager{dir: "/run/encore/network", saved: wifiPath,
		adapter: systemWiFi{}, grace: 2 * time.Second, join: 80 * time.Second,
		confirmation: 60 * time.Second, poll: time.Second}
}

func networkError(message string) error { return errors.New("dot: network " + message) }

func networkID() (string, error) {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", networkError("randomness unavailable")
	}
	return hex.EncodeToString(b[:]), nil
}

func readNetworkFile(path string) ([]byte, error) {
	f, err := os.OpenFile(path, os.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 8192 {
		return nil, networkError("invalid file")
	}
	b, err := io.ReadAll(io.LimitReader(f, 8193))
	if err != nil || len(b) > 8192 {
		return nil, networkError("invalid file")
	}
	return b, nil
}

func (m *wifiManager) lock() (*os.File, error) {
	if err := os.MkdirAll(m.dir, 0700); err != nil {
		return nil, networkError("IPC unavailable")
	}
	info, err := os.Lstat(m.dir)
	if err != nil || !info.IsDir() || info.Mode().Perm() != 0700 {
		return nil, networkError("IPC is not private")
	}
	f, err := os.OpenFile(filepath.Join(m.dir, "lock"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, networkError("lock unavailable")
	}
	// API calls must not hang if a worker gets stuck in a device command.
	deadline := time.Now().Add(5 * time.Second)
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, syscall.EWOULDBLOCK) || time.Now().After(deadline) {
			f.Close()
			return nil, networkError("busy")
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func unlockNetwork(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }

func (m *wifiManager) read() (wifiRecord, error) {
	var s wifiRecord
	b, err := readNetworkFile(filepath.Join(m.dir, "state"))
	if err != nil || json.Unmarshal(b, &s) != nil {
		return s, networkError("state unavailable")
	}
	return s, nil
}

func (m *wifiManager) write(s wifiRecord) error {
	b, err := json.Marshal(s)
	if err != nil || writePrivate(filepath.Join(m.dir, "state"), b) != nil {
		return networkError("state save failed")
	}
	return nil
}

func activeWiFi(s wifiRecord) bool {
	return s.State == "queued" || s.State == "switching" || s.State == "awaiting_confirmation"
}

// Called with the IPC lock held. A free owner lock means no worker can service
// this request. Startup takes this same IPC lock before obtaining ownership.
func (m *wifiManager) available() error {
	f, err := os.OpenFile(filepath.Join(m.dir, "owner"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return networkError("worker unavailable")
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		return networkError("worker unavailable")
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) {
		return networkError("worker unavailable")
	}
	return nil
}

func (m *wifiManager) status() (wifiStatus, error) {
	l, err := m.lock()
	if err != nil {
		return wifiStatus{}, err
	}
	defer unlockNetwork(l)
	s, err := m.read()
	if err != nil {
		return wifiStatus{}, err
	}
	if err = m.available(); err != nil {
		return s.wifiStatus, err
	}
	if !activeWiFi(s) && s.State != "failed" {
		// Outside a transaction, report the live network, not an empty startup
		// record or the candidate from the last rolled-back change.
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		defer cancel()
		s.SSID, s.Address, err = m.adapter.observe(ctx)
		if err != nil {
			return s.wifiStatus, networkError("association unavailable")
		}
	}
	return s.wifiStatus, nil
}

func (m *wifiManager) stage(ssid, password string) (wifiStatus, error) {
	b, err := wifiConfig(improv.WiFi{SSID: ssid, Passphrase: password})
	if err != nil {
		return wifiStatus{}, networkError("invalid WPA2 credentials")
	}
	defer clear(b)
	l, err := m.lock()
	if err != nil {
		return wifiStatus{}, err
	}
	defer unlockNetwork(l)
	if err = m.available(); err != nil {
		return wifiStatus{}, err
	}
	s, err := m.read()
	if err != nil {
		return wifiStatus{}, err
	}
	if activeWiFi(s) || s.State == "failed" {
		return s.wifiStatus, networkError("transaction already active")
	}
	old, err := readNetworkFile(m.saved)
	if err != nil || savedWiFiSSID(old) == "" {
		return s.wifiStatus, networkError("last-good configuration required")
	}
	clear(old)
	id, err := networkID()
	if err != nil {
		return s.wifiStatus, err
	}
	if err = writePrivate(filepath.Join(m.dir, "candidate"), b); err != nil {
		return s.wifiStatus, networkError("request save failed")
	}
	s = wifiRecord{wifiStatus: wifiStatus{ID: id, State: "queued", SSID: ssid}, Generation: s.Generation, Start: time.Now().Add(m.grace)}
	if err = m.write(s); err != nil {
		_ = os.Remove(filepath.Join(m.dir, "candidate"))
		return wifiStatus{}, err
	}
	return s.wifiStatus, nil
}

func (m *wifiManager) confirm(id string) (wifiStatus, error) {
	l, err := m.lock()
	if err != nil {
		return wifiStatus{}, err
	}
	defer unlockNetwork(l)
	if err = m.available(); err != nil {
		return wifiStatus{}, err
	}
	s, err := m.read()
	if err != nil {
		return wifiStatus{}, err
	}
	if id == "" || id != s.ID {
		return s.wifiStatus, networkError("stale transaction")
	}
	if s.State != "awaiting_confirmation" || s.Cancel || !time.Now().Before(s.Deadline) {
		return s.wifiStatus, networkError("not awaiting confirmation")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	ssid, address, err := m.adapter.observe(ctx)
	if err != nil || ssid != s.SSID || !validWiFiAddress(address) || address != s.Address || !time.Now().Before(s.Deadline) {
		return s.wifiStatus, networkError("candidate connection not verified")
	}
	b, err := readNetworkFile(filepath.Join(m.dir, "candidate"))
	if err != nil {
		return s.wifiStatus, networkError("candidate unavailable")
	}
	defer clear(b)
	if err = writePrivate(m.saved, b); err != nil {
		s.Cancel = true
		s.Error = "configuration save failed"
		_ = m.write(s)
		return s.wifiStatus, networkError("configuration save failed")
	}
	s.State = "committed"
	// If this state write fails the durable config is still the source of truth:
	// worker recovery always reapplies last-good, never an older backup.
	err = m.write(s)
	if err == nil {
		_ = os.Remove(filepath.Join(m.dir, "candidate"))
	}
	return s.wifiStatus, err
}

func (m *wifiManager) cancel(id string) (wifiStatus, error) {
	l, err := m.lock()
	if err != nil {
		return wifiStatus{}, err
	}
	defer unlockNetwork(l)
	if err = m.available(); err != nil {
		return wifiStatus{}, err
	}
	s, err := m.read()
	if err != nil {
		return wifiStatus{}, err
	}
	if id == "" || id != s.ID {
		return s.wifiStatus, networkError("stale transaction")
	}
	if !activeWiFi(s) {
		return s.wifiStatus, networkError("transaction is not active")
	}
	s.Cancel = true
	return s.wifiStatus, m.write(s)
}

func validWiFiAddress(address string) bool {
	ip := net.ParseIP(address)
	return ip != nil && ip.To4() != nil && ip.IsGlobalUnicast() && !ip.IsLinkLocalUnicast()
}

// Installer and Improv both write a single hex-encoded SSID. Refuse unknown
// config formats rather than claim rollback success on some unrelated network.
func savedWiFiSSID(b []byte) string {
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(strings.TrimSpace(line), "ssid="); ok {
			ssid, err := hex.DecodeString(v)
			if err == nil && len(ssid) > 0 && len(ssid) <= 32 {
				return string(ssid)
			}
		}
	}
	return ""
}

// RunNetworkManager must run as an independent, respawned service, not a child
// of the voice runtime. A killed worker leaves its candidate marker in tmpfs;
// the next owner restores persistent last-good before accepting any requests.
func RunNetworkManager(ctx context.Context) error { return newWiFiManager().run(ctx) }

func (m *wifiManager) run(ctx context.Context) error {
	l, err := m.lock()
	if err != nil {
		return err
	}
	owner, err := os.OpenFile(filepath.Join(m.dir, "owner"), os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		unlockNetwork(l)
		return networkError("owner unavailable")
	}
	defer owner.Close()
	if err = syscall.Flock(int(owner.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		unlockNetwork(l)
		return networkError("worker already running")
	}
	defer syscall.Flock(int(owner.Fd()), syscall.LOCK_UN)
	generation, err := networkID()
	if err != nil {
		unlockNetwork(l)
		return err
	}
	s := wifiRecord{wifiStatus: wifiStatus{State: "idle"}, Generation: generation}
	if _, e := os.Stat(filepath.Join(m.dir, "candidate")); e == nil {
		previous, _ := m.read()
		s.wifiStatus = previous.wifiStatus
		s.State = "switching"
		// Recovery is itself bounded, including DHCP/address restoration.
		err = m.restore(ctx, &s, "worker restarted")
	}
	if err == nil {
		err = m.write(s)
	}
	unlockNetwork(l)
	if err != nil {
		return err
	}
	ticker := time.NewTicker(m.poll)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			// Leave the marker for supervised recovery, even on graceful shutdown.
			return nil
		case <-ticker.C:
		}
		l, err = m.lock()
		if err != nil {
			return err
		}
		s, err = m.read()
		if err == nil && s.Generation != generation {
			err = networkError("ownership changed")
		}
		if err == nil {
			err = m.step(ctx, &s)
		}
		unlockNetwork(l)
		if err != nil {
			return err
		}
	}
}

func (m *wifiManager) step(ctx context.Context, s *wifiRecord) error {
	if !activeWiFi(*s) {
		return nil
	}
	if s.Cancel {
		return m.restore(ctx, s, "cancelled")
	}
	if s.State == "queued" {
		if time.Now().Before(s.Start) {
			return nil
		}
		s.State = "switching"
		s.Deadline = time.Now().Add(m.join)
		if err := m.write(*s); err != nil {
			return err
		}
		b, err := readNetworkFile(filepath.Join(m.dir, "candidate"))
		if err != nil {
			return m.restore(ctx, s, "candidate unavailable")
		}
		defer clear(b)
		bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = m.adapter.apply(bounded, b)
		cancel()
		if err != nil {
			return m.restore(ctx, s, "candidate apply failed")
		}
		return nil
	}
	if !time.Now().Before(s.Deadline) {
		return m.restore(ctx, s, "transaction timed out")
	}
	bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
	ssid, address, err := m.adapter.observe(bounded)
	cancel()
	if s.State == "switching" {
		if err != nil || ssid != s.SSID {
			return nil
		}
		if !s.Renewed {
			bounded, cancel = context.WithTimeout(ctx, 3*time.Second)
			err = m.adapter.renew(bounded)
			cancel()
			if err != nil {
				return m.restore(ctx, s, "DHCP renewal failed")
			}
			s.Renewed = true
			return m.write(*s) // Never accept the pre-renewal/stale address.
		}
		if !validWiFiAddress(address) {
			return nil
		}
		s.State = "awaiting_confirmation"
		s.Address = address
		s.Deadline = time.Now().Add(m.confirmation)
		return m.write(*s)
	}
	if err == nil && (ssid != s.SSID || !validWiFiAddress(address)) {
		return m.restore(ctx, s, "candidate connection lost")
	}
	return nil
}

func (m *wifiManager) restore(ctx context.Context, s *wifiRecord, reason string) error {
	b, err := readNetworkFile(m.saved)
	ssid := savedWiFiSSID(b)
	if ssid == "" {
		err = networkError("invalid last-good configuration")
	}
	if err == nil {
		defer clear(b)
		bounded, cancel := context.WithTimeout(ctx, 3*time.Second)
		err = m.adapter.apply(bounded, b)
		cancel()
	}
	if err == nil {
		// apply renews the existing daemon; wait for a usable last-good address.
		bounded, cancel := context.WithTimeout(ctx, m.join)
		defer cancel()
		for {
			probe, done := context.WithTimeout(bounded, 3*time.Second)
			associated, address, e := m.adapter.observe(probe)
			done()
			if e == nil && associated == ssid && validWiFiAddress(address) {
				s.SSID = ssid
				s.Address = address
				break
			}
			select {
			case <-bounded.Done():
				err = bounded.Err()
			case <-time.After(m.poll):
				continue
			}
			break
		}
	}
	s.Error = reason
	if err != nil {
		s.State = "failed"
		s.Error = "last-good restoration failed"
		_ = m.write(*s)
		// Retain candidate marker and exit: respawn retries recovery. Never
		// accept a new stage while last-good restoration is unresolved.
		return networkError("last-good restoration failed")
	}
	s.State = "rolled_back"
	if err = m.write(*s); err != nil {
		return err
	}
	return os.Remove(filepath.Join(m.dir, "candidate"))
}

type systemWiFi struct{}

func networkCommand(ctx context.Context, name string, args ...string) ([]byte, error) {
	// No shell, no command output propagated into states/errors. Commands have
	// fixed arguments and never include credentials (even derived PSKs).
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.WaitDelay = 200 * time.Millisecond
	return cmd.Output()
}

// Prefer pidfd to pin identity. Vendor 3.10 kernels lack pidfd: keep a proc
// directory open and recheck that identity immediately before the signal.
// Never start a second DHCP client; absence/mismatch is a fail-closed error.
func signalDHCP(signal syscall.Signal) error {
	b, err := readNetworkFile("/run/udhcpc.pid")
	if err != nil {
		return networkError("DHCP daemon unavailable")
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || pid <= 1 {
		return networkError("invalid DHCP identity")
	}
	proc, err := os.Open(fmt.Sprintf("/proc/%d", pid))
	if err != nil {
		return networkError("DHCP identity unavailable")
	}
	defer proc.Close()
	fd, pidfdErr := unix.PidfdOpen(pid, 0)
	if pidfdErr == nil {
		defer unix.Close(fd)
	} else if !errors.Is(pidfdErr, unix.ENOSYS) && !errors.Is(pidfdErr, unix.EINVAL) {
		return networkError("DHCP identity unavailable")
	}
	cmdfd, err := unix.Openat(int(proc.Fd()), "cmdline", unix.O_RDONLY|unix.O_CLOEXEC, 0)
	if err != nil {
		return networkError("DHCP identity unavailable")
	}
	cmdfile := os.NewFile(uintptr(cmdfd), "cmdline")
	cmd, err := io.ReadAll(io.LimitReader(cmdfile, 8193))
	cmdfile.Close()
	if err != nil {
		return networkError("DHCP identity unavailable")
	}
	args := strings.Split(strings.TrimRight(string(cmd), "\x00"), "\x00")
	validName := len(args) > 0 && filepath.Base(args[0]) == "udhcpc"
	iface, pidfile := false, false
	for i := 1; i+1 < len(args); i++ {
		if args[i] == "-i" && args[i+1] == "wlan0" {
			iface = true
		}
		if args[i] == "-p" && args[i+1] == "/run/udhcpc.pid" {
			pidfile = true
		}
	}
	if !validName || !iface || !pidfile {
		return networkError("unexpected DHCP identity")
	}
	if pidfdErr == nil {
		err = unix.PidfdSendSignal(fd, unix.Signal(signal), nil, 0)
	} else {
		var info unix.Stat_t
		if err = unix.Fstatat(int(proc.Fd()), "cmdline", &info, 0); err != nil {
			return networkError("DHCP identity unavailable")
		}
		err = syscall.Kill(pid, signal)
	}
	if err != nil {
		return networkError("DHCP signal failed")
	}
	return nil
}

func (systemWiFi) renew(ctx context.Context) error {
	if err := signalDHCP(syscall.SIGUSR2); err != nil {
		return err
	}
	// BusyBox processes SIGUSR2 asynchronously, including its deconfig hook.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(250 * time.Millisecond):
	}
	if _, err := networkCommand(ctx, "ip", "-4", "addr", "flush", "dev", "wlan0"); err != nil {
		return networkError("address clear failed")
	}
	return signalDHCP(syscall.SIGUSR1)
}

func (s systemWiFi) apply(ctx context.Context, b []byte) error {
	// Force a new authentication even for a password-only change on the same
	// SSID: an existing completed association must not validate new credentials.
	if reply, err := networkCommand(ctx, "wpa_cli", "-i", "wlan0", "disconnect"); err != nil || strings.TrimSpace(string(reply)) != "OK" {
		return networkError("supplicant disconnect failed")
	}
	if err := signalDHCP(syscall.SIGUSR2); err != nil {
		return err
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(250 * time.Millisecond):
	}
	if _, err := networkCommand(ctx, "ip", "-4", "addr", "flush", "dev", "wlan0"); err != nil {
		return networkError("address clear failed")
	}
	if err := writePrivate(liveWiFiPath, b); err != nil {
		return networkError("live configuration save failed")
	}
	if err := reconfigureWiFi(ctx); err != nil {
		return networkError("supplicant reconfigure failed")
	}
	if reply, err := networkCommand(ctx, "wpa_cli", "-i", "wlan0", "reconnect"); err != nil || strings.TrimSpace(string(reply)) != "OK" {
		return networkError("supplicant reconnect failed")
	}
	return signalDHCP(syscall.SIGUSR1)
}

func (systemWiFi) observe(ctx context.Context) (string, string, error) {
	b, err := networkCommand(ctx, "wpa_cli", "-i", "wlan0", "status")
	if err != nil {
		return "", "", networkError("association unavailable")
	}
	var ssid, state string
	for _, line := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(line, "ssid="); ok {
			ssid = decodeWPASSID(v)
		}
		if v, ok := strings.CutPrefix(line, "wpa_state="); ok {
			state = v
		}
	}
	if state != "COMPLETED" {
		return "", "", nil
	}
	address := ""
	if ip := wlanIPv4(); ip != nil {
		address = ip.String()
	}
	return ssid, address, nil
}

// wpa_cli status uses printf_encode escaping, not shell or JSON escaping.
func decodeWPASSID(s string) string {
	var b []byte
	for i := 0; i < len(s); i++ {
		if s[i] != '\\' {
			b = append(b, s[i])
			continue
		}
		i++
		if i >= len(s) {
			return ""
		}
		switch s[i] {
		case '\\', '"':
			b = append(b, s[i])
		case 'n':
			b = append(b, '\n')
		case 'r':
			b = append(b, '\r')
		case 't':
			b = append(b, '\t')
		case 'e':
			b = append(b, 27)
		case 'x':
			if i+2 >= len(s) {
				return ""
			}
			v, err := hex.DecodeString(s[i+1 : i+3])
			if err != nil {
				return ""
			}
			b = append(b, v[0])
			i += 2
		default:
			return ""
		}
	}
	return string(b)
}
