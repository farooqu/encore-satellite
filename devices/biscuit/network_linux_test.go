package biscuit

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/farooqu/encore-satellite/core/improv"
)

// A network-lifecycle fake, not command mocks. Release/flush/renew and
// supplicant observation are independently controllable from the public API.
type testWiFiAdapter struct {
	mu                  sync.Mutex
	ssid, address       string
	autoJoin, autoLease bool
	renews, applies     int
	failApply           bool
	failRestore         bool
}

func (a *testWiFiAdapter) apply(_ context.Context, b []byte) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.applies++
	ssid := savedWiFiSSID(b)
	a.ssid, a.address = "", ""
	if ssid == "last-good" {
		if a.failRestore {
			return errors.New("restore leaked old-password")
		}
		a.ssid, a.address = ssid, "192.168.1.20"
		return nil
	}
	if a.failApply {
		return errors.New("adapter leaked super-secret-password")
	}
	if a.autoJoin {
		a.ssid, a.address = ssid, "192.168.1.20"
	}
	return nil
}

func (a *testWiFiAdapter) renew(context.Context) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.renews++
	a.address = ""
	if a.autoLease {
		a.address = "10.0.0.20"
	}
	return nil
}

func (a *testWiFiAdapter) observe(context.Context) (string, string, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.ssid, a.address, nil
}

func (a *testWiFiAdapter) connection(ssid, address string) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.ssid, a.address = ssid, address
}

func testWiFiManager(t *testing.T) (*wifiManager, *testWiFiAdapter, []byte) {
	t.Helper()
	dir := t.TempDir()
	b, err := wifiConfig(improv.WiFi{SSID: "last-good", Passphrase: "old-password"})
	if err != nil {
		t.Fatal(err)
	}
	saved := filepath.Join(dir, "last-good")
	if err = writePrivate(saved, b); err != nil {
		t.Fatal(err)
	}
	a := &testWiFiAdapter{ssid: "last-good", address: "192.168.1.20", autoJoin: true, autoLease: true}
	m := &wifiManager{dir: filepath.Join(dir, "ipc"), saved: saved, adapter: a,
		grace: 30 * time.Millisecond, join: 500 * time.Millisecond, confirmation: 500 * time.Millisecond, poll: 5 * time.Millisecond}
	return m, a, b
}

func startTestWiFiWorker(t *testing.T, m *wifiManager) func() {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- m.run(ctx) }()
	var once sync.Once
	stop := func() {
		once.Do(func() {
			cancel()
			select {
			case err := <-done:
				if err != nil {
					t.Errorf("worker: %v", err)
				}
			case <-time.After(3 * time.Second):
				t.Error("worker did not stop")
			}
		})
	}
	t.Cleanup(stop)
	deadline := time.Now().Add(2 * time.Second)
	for {
		if _, err := m.status(); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("worker did not start")
		}
		time.Sleep(time.Millisecond)
	}
	return stop
}

func waitWiFiState(t *testing.T, m *wifiManager, state string) wifiStatus {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for {
		s, err := m.status()
		if err == nil && s.State == state {
			return s
		}
		if time.Now().After(deadline) {
			t.Fatalf("wanted %s, got %+v (%v)", state, s, err)
		}
		time.Sleep(2 * time.Millisecond)
	}
}

func mustStageWiFi(t *testing.T, m *wifiManager) wifiStatus {
	t.Helper()
	s, err := m.stage("candidate", "super-secret-password")
	if err != nil || s.State != "queued" || len(s.ID) != 32 {
		t.Fatalf("stage: %+v, %v", s, err)
	}
	return s
}

func assertSavedWiFi(t *testing.T, m *wifiManager, want []byte) {
	t.Helper()
	b, err := os.ReadFile(m.saved)
	if err != nil || !bytes.Equal(b, want) {
		t.Fatal("last-good changed unexpectedly", err)
	}
}

func TestWiFiExplicitConfirmAndRuntimeRestart(t *testing.T) {
	m, _, old := testWiFiManager(t)
	startTestWiFiWorker(t, m)
	if current, err := m.status(); err != nil || current.SSID != "last-good" || current.Address != "192.168.1.20" {
		t.Fatalf("initial live network: %+v %v", current, err)
	}
	s := mustStageWiFi(t, m)
	if _, err := m.confirm(s.ID); err == nil {
		t.Fatal("confirmed before association")
	}
	assertSavedWiFi(t, m, old)
	s = waitWiFiState(t, m, "awaiting_confirmation")
	assertSavedWiFi(t, m, old)
	// A new API client (voice process restart) neither owns nor restarts worker.
	client := *m
	if _, err := client.confirm("obsolete-id"); err == nil {
		t.Fatal("accepted stale confirm")
	}
	if _, err := client.cancel("obsolete-id"); err == nil {
		t.Fatal("accepted stale cancel")
	}
	committed, err := client.confirm(s.ID)
	if err != nil || committed.State != "committed" {
		t.Fatalf("confirm: %+v %v", committed, err)
	}
	want, _ := wifiConfig(improv.WiFi{SSID: "candidate", Passphrase: "super-secret-password"})
	assertSavedWiFi(t, m, want)
	if _, err = os.Stat(filepath.Join(m.dir, "candidate")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("candidate secret retained")
	}
	if _, err = m.cancel(s.ID); err == nil {
		t.Fatal("cancelled committed transaction")
	}
}

func TestWiFiWrongSSIDAndStaleAddress(t *testing.T) {
	m, a, old := testWiFiManager(t)
	a.autoJoin, a.autoLease = false, false
	startTestWiFiWorker(t, m)
	s := mustStageWiFi(t, m)
	waitWiFiState(t, m, "switching")
	a.connection("unrelated", "192.168.1.20")
	time.Sleep(25 * time.Millisecond)
	if current, _ := m.status(); current.State != "switching" {
		t.Fatal("accepted wrong SSID")
	}
	a.connection("candidate", "192.168.1.20")
	// The old address must be discarded by post-association renew.
	deadline := time.Now().Add(time.Second)
	for {
		a.mu.Lock()
		renewed := a.renews > 0
		a.mu.Unlock()
		if renewed {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("no renew")
		}
		time.Sleep(time.Millisecond)
	}
	if current, _ := m.status(); current.State != "switching" {
		t.Fatal("accepted stale pre-renew address")
	}
	if _, err := m.confirm(s.ID); err == nil {
		t.Fatal("confirmed stale address")
	}
	for _, ip := range []string{"", "169.254.1.2", "0.0.0.0", "::1", "127.0.0.1"} {
		a.connection("candidate", ip)
		time.Sleep(10 * time.Millisecond)
		if current, _ := m.status(); current.State != "switching" {
			t.Fatalf("accepted invalid address %s", ip)
		}
	}
	a.connection("candidate", "10.0.0.20")
	waitWiFiState(t, m, "awaiting_confirmation")
	a.connection("unrelated", "10.0.0.20")
	if _, err := m.confirm(s.ID); err == nil {
		t.Fatal("confirm did not revalidate SSID")
	}
	waitWiFiState(t, m, "rolled_back")
	assertSavedWiFi(t, m, old)
}

func TestWiFiTimeoutAndCancelRestore(t *testing.T) {
	for _, scenario := range []string{"bad-password", "confirmation-timeout", "cancel", "cancel-after-join", "cancel-during-switch", "apply-failure"} {
		t.Run(scenario, func(t *testing.T) {
			// Keep real filesystem IPC, but don't let fsync or runner scheduling
			// consume the deadlines whose behavior this scenario is testing.
			synctest.Test(t, func(t *testing.T) {
				m, a, old := testWiFiManager(t)
				m.join, m.confirmation = 100*time.Millisecond, 100*time.Millisecond
				if scenario == "bad-password" || scenario == "cancel-during-switch" {
					a.autoJoin = false
				}
				if scenario == "apply-failure" {
					a.failApply = true
				}
				startTestWiFiWorker(t, m)
				s := mustStageWiFi(t, m)
				if scenario == "bad-password" {
					waitWiFiState(t, m, "switching")
					synctest.Sleep(m.join / 2)
					if current, err := m.status(); err != nil || current.State != "switching" {
						t.Fatalf("join expired early: %+v %v", current, err)
					}
				}
				if scenario == "cancel" {
					if _, err := m.cancel(s.ID); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "cancel-after-join" || scenario == "cancel-during-switch" {
					state := "awaiting_confirmation"
					if scenario == "cancel-during-switch" {
						state = "switching"
					}
					waitWiFiState(t, m, state)
					if _, err := m.cancel(s.ID); err != nil {
						t.Fatal(err)
					}
				}
				if scenario == "confirmation-timeout" {
					waitWiFiState(t, m, "awaiting_confirmation")
					synctest.Sleep(m.confirmation / 2)
					if current, err := m.status(); err != nil || current.State != "awaiting_confirmation" {
						t.Fatalf("confirmation expired early: %+v %v", current, err)
					}
				}
				rolled := waitWiFiState(t, m, "rolled_back")
				wantError := "transaction timed out"
				switch scenario {
				case "cancel", "cancel-after-join", "cancel-during-switch":
					wantError = "cancelled"
				case "apply-failure":
					wantError = "candidate apply failed"
				}
				if rolled.Error != wantError {
					t.Fatalf("rollback reason: got %q, want %q", rolled.Error, wantError)
				}
				if rolled.Address != "192.168.1.20" || rolled.SSID != "last-good" {
					t.Fatal("last-good network status not restored")
				}
				if strings.Contains(rolled.Error, "super-secret-password") {
					t.Fatal("secret in adapter error")
				}
				if _, err := m.confirm(s.ID); err == nil {
					t.Fatal("accepted expired confirmation")
				}
				assertSavedWiFi(t, m, old)
			})
		})
	}
}

func TestWiFiWorkerRestartRestoresCandidate(t *testing.T) {
	m, _, old := testWiFiManager(t)
	stop := startTestWiFiWorker(t, m)
	s := mustStageWiFi(t, m)
	waitWiFiState(t, m, "awaiting_confirmation")
	stop()
	if _, err := m.confirm(s.ID); err == nil {
		t.Fatal("accepted confirmation without worker")
	}
	startTestWiFiWorker(t, m)
	waitWiFiState(t, m, "rolled_back")
	assertSavedWiFi(t, m, old)
	if _, err := m.confirm(s.ID); err == nil {
		t.Fatal("accepted pre-restart confirmation")
	}
	if _, err := m.cancel(s.ID); err == nil {
		t.Fatal("accepted pre-restart cancellation")
	}
	newStage := mustStageWiFi(t, m)
	if newStage.ID == s.ID {
		t.Fatal("transaction ID reused")
	}
}

func TestWiFiWorkerOwnershipAndFailedRecovery(t *testing.T) {
	m, a, old := testWiFiManager(t)
	stop := startTestWiFiWorker(t, m)
	if err := m.run(context.Background()); err == nil {
		t.Fatal("second worker acquired ownership")
	}
	mustStageWiFi(t, m)
	waitWiFiState(t, m, "awaiting_confirmation")
	stop()
	a.mu.Lock()
	a.failRestore = true
	a.mu.Unlock()
	if err := m.run(context.Background()); err == nil || strings.Contains(err.Error(), "old-password") {
		t.Fatal("failed restoration did not fail safely")
	}
	s, err := m.status()
	if err == nil || s.State != "failed" || strings.Contains(s.Error, "old-password") {
		t.Fatalf("failed status: %+v, %v", s, err)
	}
	if _, err := m.stage("candidate", "super-secret-password"); err == nil {
		t.Fatal("accepted stage during unresolved recovery")
	}
	if _, err := os.Stat(filepath.Join(m.dir, "candidate")); err != nil {
		t.Fatal("recovery marker lost")
	}
	a.mu.Lock()
	a.failRestore = false
	a.mu.Unlock()
	startTestWiFiWorker(t, m)
	waitWiFiState(t, m, "rolled_back")
	assertSavedWiFi(t, m, old)
}

func TestWiFiConcurrentStageAndPrivateIPC(t *testing.T) {
	m, _, old := testWiFiManager(t)
	m.grace = 200 * time.Millisecond
	startTestWiFiWorker(t, m)
	type result struct {
		s   wifiStatus
		err error
	}
	results := make(chan result, 2)
	for range 2 {
		go func() { s, err := m.stage("candidate", "super-secret-password"); results <- result{s, err} }()
	}
	success := 0
	var id string
	for range 2 {
		r := <-results
		if r.err == nil {
			success++
			id = r.s.ID
		}
		b, _ := json.Marshal(r.s)
		if bytes.Contains(b, []byte("super-secret-password")) {
			t.Fatal("secret in state")
		}
	}
	if success != 1 {
		t.Fatalf("%d concurrent stages accepted", success)
	}
	for _, name := range []string{"state", "candidate", "lock", "owner"} {
		path := filepath.Join(m.dir, name)
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("insecure %s: %v", name, err)
		}
		b, _ := os.ReadFile(path)
		if bytes.Contains(b, []byte("super-secret-password")) {
			t.Fatal("plaintext password in IPC")
		}
	}
	if _, err := m.cancel(id); err != nil {
		t.Fatal(err)
	}
	waitWiFiState(t, m, "rolled_back")
	assertSavedWiFi(t, m, old)
}

func TestWiFiFailedSaveRollsBack(t *testing.T) {
	m, _, old := testWiFiManager(t)
	startTestWiFiWorker(t, m)
	s := mustStageWiFi(t, m)
	waitWiFiState(t, m, "awaiting_confirmation")
	client := *m
	// Atomically renaming a file onto a directory must fail, even as root.
	client.saved = t.TempDir()
	if _, err := client.confirm(s.ID); err == nil {
		t.Fatal("save failure reported success")
	}
	waitWiFiState(t, m, "rolled_back")
	assertSavedWiFi(t, m, old)
}

func TestWiFiRequiresLastGoodAndValidCredentials(t *testing.T) {
	m, _, _ := testWiFiManager(t)
	stop := startTestWiFiWorker(t, m)
	if err := os.Remove(m.saved); err != nil {
		t.Fatal(err)
	}
	if _, err := m.stage("candidate", "super-secret-password"); err == nil {
		t.Fatal("allowed initial setup through change API")
	}
	if _, err := m.stage("candidate", "short"); err == nil || strings.Contains(err.Error(), "short") {
		t.Fatal("bad validation or secret echo")
	}
	stop()
	if err := os.WriteFile(filepath.Join(m.dir, "state"), bytes.Repeat([]byte("x"), 8193), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := m.status(); err == nil {
		t.Fatal("accepted oversized IPC")
	}
}

func TestWiFiSSIDDecoding(t *testing.T) {
	for encoded, want := range map[string]string{`plain`: "plain", `a\x00b`: "a\x00b", `a\\b`: `a\b`, `a\nb`: "a\nb", `\xzz`: "", `bad\`: ""} {
		if got := decodeWPASSID(encoded); got != want {
			t.Fatalf("decode %q: %q, want %q", encoded, got, want)
		}
	}
}
