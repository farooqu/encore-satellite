package biscuit

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/farooqu/encore-satellite/core/improv"
)

func TestConfigurationAndDefault(t *testing.T) {
	c, err := parseConfig(strings.NewReader(`{"schema":1,"name":"kitchen-dot"}`))
	if err != nil || c.Name != "kitchen-dot" || c.BluetoothProxy || c.Sendspin {
		t.Fatalf("got %+v %v", c, err)
	}
	for _, bad := range []string{
		`{}`, `{"schema":2,"name":"dot"}`, `{"schema":1,"name":"Dot"}`, `{"schema":1,"name":"-dot"}`,
		`{"schema":1,"name":"dot.local"}`, `{"schema":1,"name":"dot","backend":"wyoming"}`,
		`{"schema":1,"name":"dot","backend":"assist"}`, `{"schema":1,"name":"dot","backend":"incubator"}`,
		`{"schema":1,"name":"dot","secret":"no"}`, `{"schema":1,"name":"dot"} {}`,
	} {
		if _, err := parseConfig(strings.NewReader(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestSendspinIgnoresLegacyURLWithoutBreakingRollback(t *testing.T) {
	const setup = `{"schema":1,"name":"dot","sendspin":true`
	auto, err := parseConfig(strings.NewReader(setup + `}`))
	if err != nil || !auto.Sendspin {
		t.Fatalf("could not enable discovery: %+v %v", auto, err)
	}
	old, err := parseConfig(strings.NewReader(setup + `,"sendspin_url":"ws://old.local:9000/sendspin"}`))
	if err != nil || old != auto {
		t.Fatalf("legacy URL affected discovery config: %+v %v", old, err)
	}
	path := filepath.Join(t.TempDir(), "config.json")
	if err := SaveConfig(path, old); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile(path)
	if err != nil || !strings.Contains(string(b), `"sendspin_url":"ws://sendspin-discovery.invalid:8927/sendspin"`) {
		t.Fatal("saved config must remain readable by older slots without carrying a dialable URL", err)
	}
}

func TestProxySettingPersistsWithoutChangingIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := Config{Schema: 1, Name: "office-dot", Sendspin: true}
	for _, enabled := range []bool{true, false} {
		c.BluetoothProxy = enabled
		if err := SaveConfig(path, c); err != nil {
			t.Fatal(err)
		}
		got, err := LoadConfig(path)
		if err != nil || got != c {
			t.Fatalf("config did not round trip: %+v %v", got, err)
		}
		st, err := os.Stat(path)
		if err != nil || st.Mode().Perm() != 0600 {
			t.Fatal("config is not private")
		}
	}
	c.Name = "invalid.name"
	if err := SaveConfig(path, c); err == nil {
		t.Fatal("saved invalid configuration")
	}
	if got, err := LoadConfig(path); err != nil || got.Name != "office-dot" {
		t.Fatal("invalid save damaged existing identity")
	}
}

func TestImprovWiFiUsesKnownWPAVectorAndEscapesSSID(t *testing.T) {
	b, err := wifiConfig(improv.WiFi{SSID: "IEEE", Passphrase: "password"})
	if err != nil || !strings.Contains(string(b), "psk=f42c6fc52df0ebef9ebb4b90b38a5f902e83fe1b135a70e23aed762e9710a12e\n") {
		t.Fatal("WPA derivation does not match the published vector")
	}
	b, err = wifiConfig(improv.WiFi{SSID: "a\"\nnetwork={", Passphrase: "password"})
	if err != nil || strings.Count(string(b), "network={") != 1 || strings.Contains(string(b), "password") {
		t.Fatal("unsafe supplicant configuration")
	}
	for _, w := range []improv.WiFi{{SSID: "", Passphrase: "password"}, {SSID: "ok", Passphrase: "bad\npassword"}, {SSID: "ok", Passphrase: "short"}, {SSID: strings.Repeat("é", 17), Passphrase: "password"}} {
		if _, err := wifiConfig(w); err == nil {
			t.Fatal("accepted invalid WPA credentials")
		}
	}
}

func TestNoiseKeyAbsentOrInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "noise.psk")
	if _, err := noiseKey(path); err != os.ErrNotExist {
		t.Fatalf("missing key: %v", err)
	}
	if err := os.WriteFile(path, []byte("broken"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := noiseKey(path); err == nil {
		t.Fatal("invalid key silently replaced")
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(path, link); err != nil {
		t.Fatal(err)
	}
	if _, err := noiseKey(link); err == nil {
		t.Fatal("followed symlink")
	}
}
