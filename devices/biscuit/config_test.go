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
		`{"schema":1,"name":"dot","sendspin":true}`,
	} {
		if _, err := parseConfig(strings.NewReader(bad)); err == nil {
			t.Fatalf("accepted %s", bad)
		}
	}
}

func TestSendspinURLIsManualEndpointNotCredentialCarrier(t *testing.T) {
	for _, raw := range []string{"ws://ma.local:8927/sendspin", "ws://[2001:db8::1]:8927/sendspin"} {
		if err := validateMusicURL(raw); err != nil {
			t.Fatal(err)
		}
	}
	for _, raw := range []string{"ws://ma/sendspin", "ws://ma:0/sendspin", "ws://ma:65536/sendspin", "wss://ma:8927/sendspin", "ws://user:secret@ma:8927/sendspin", "ws://ma:8927/sendspin?token=x", "ws://ma:8927/sendspin#secret", "ws://ma:8927/", "ws://ma:8927/%73endspin"} {
		if err := validateMusicURL(raw); err == nil {
			t.Fatalf("accepted %q", raw)
		}
	}
}

func TestProxySettingPersistsWithoutChangingIdentity(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.json")
	c := Config{Schema: 1, Name: "office-dot", Sendspin: true, SendspinURL: "ws://ma.local:8927/sendspin"}
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

func TestNoiseKeyUniquePersistentAndPrivate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "noise.psk")
	a, err := noiseKey(path)
	if err != nil {
		t.Fatal(err)
	}
	b, err := noiseKey(path)
	if err != nil || a != b || a.IsZero() {
		t.Fatal("key not persistent")
	}
	c, err := noiseKey(filepath.Join(dir, "other.psk"))
	if err != nil || a == c {
		t.Fatal("key not unique")
	}
	st, err := os.Stat(path)
	if err != nil || st.Mode().Perm() != 0600 {
		t.Fatal("insecure permissions")
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
