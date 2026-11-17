package biscuit

import (
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	esphome "github.com/ygelfand/go-esphome-device"
)

const ConfigPath = "/data/encore/config.json"
const KeyPath = "/data/encore/noise.psk"
const ReadyPath = "/run/encore/ready"

type Config struct {
	Schema         int    `json:"schema"`
	Name           string `json:"name"`
	BluetoothProxy bool   `json:"bluetooth_proxy,omitempty"`
	Sendspin       bool   `json:"sendspin,omitempty"`
	SendspinURL    string `json:"sendspin_url,omitempty"`
}

var dnsLabel = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9-]{0,61}[a-z0-9])?$`)

func LoadConfig(path string) (Config, error) {
	f, err := os.Open(path)
	if err != nil {
		return Config{}, err
	}
	defer f.Close()
	return parseConfig(io.LimitReader(f, 4097))
}

func parseConfig(r io.Reader) (Config, error) {
	var c Config
	d := json.NewDecoder(r)
	d.DisallowUnknownFields()
	if err := d.Decode(&c); err != nil {
		return c, errors.New("dot: invalid configuration JSON")
	}
	if d.Decode(new(any)) != io.EOF {
		return c, errors.New("dot: trailing configuration data")
	}
	if c.Schema != 1 || !dnsLabel.MatchString(c.Name) {
		return c, errors.New("dot: expected schema 1 and lowercase DNS label name; voice backend belongs to the installed package")
	}
	if err := validateMusicURL(c.SendspinURL); err != nil {
		return c, err
	}
	if c.Sendspin && c.SendspinURL == "" {
		return c, errors.New("dot: configure sendspin_url before enabling music")
	}
	return c, nil
}

// ws is only the carrier: Sendspin mandates its own authenticated Noise channel.
// No credentials, pairing tokens or guessed fallback endpoints enter the URL.
func validateMusicURL(raw string) error {
	if raw == "" {
		return nil
	}
	u, err := url.Parse(raw)
	if err == nil {
		_, _, err = net.SplitHostPort(u.Host)
	}
	if err != nil || len(raw) > 255 || u.Scheme != "ws" || u.Hostname() == "" || u.Port() == "" || u.Path != "/sendspin" || u.RawPath != "" || u.User != nil || u.RawQuery != "" || u.ForceQuery || u.Fragment != "" || strings.ContainsAny(raw, "\r\n\t ") {
		return errors.New("dot: expected ws://host:port/sendspin without credentials or query")
	}
	port, err := strconv.Atoi(u.Port())
	if err != nil || port < 1 || port > 65535 {
		return errors.New("dot: invalid Sendspin server port")
	}
	return nil
}

// SaveConfig publishes a complete private config before applying a control.
func SaveConfig(path string, c Config) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	if _, err = parseConfig(strings.NewReader(string(b))); err != nil {
		return err
	}
	return writePrivate(path, b)
}

func writePrivate(path string, b []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), ".encore-*")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	if _, err = f.Write(b); err == nil {
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
	dir, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

// noiseKey never logs or includes key material in returned errors. The parent
// directory belongs to the base; refuse symlinks and persist a fresh 0600 PSK
// before advertising encrypted controls. An invalid/truncated key is not silently replaced.
func noiseKey(path string) (esphome.PSK, error) {
	var zero esphome.PSK
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err == nil {
		key, e := esphome.GeneratePSK()
		if e != nil {
			_ = f.Close()
			_ = os.Remove(path)
			return zero, errors.New("dot: PSK generation failed")
		}
		_, e = f.WriteString(key.String() + "\n")
		if e == nil {
			e = f.Sync()
		}
		closeErr := f.Close()
		if e != nil || closeErr != nil {
			return zero, errors.New("dot: PSK persistence failed")
		}
	} else if !errors.Is(err, os.ErrExist) {
		return zero, errors.New("dot: cannot create Noise PSK")
	}
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() {
		return zero, errors.New("dot: Noise PSK must be a regular file")
	}
	if err = os.Chmod(path, 0600); err != nil {
		return zero, errors.New("dot: cannot protect Noise PSK")
	}
	b, err := os.ReadFile(path)
	if err != nil || len(b) > 128 {
		return zero, errors.New("dot: cannot read Noise PSK")
	}
	key, err := esphome.ParsePSK(strings.TrimSpace(string(b)))
	clear(b)
	if err != nil || key.IsZero() {
		return zero, errors.New("dot: invalid Noise PSK; refusing replacement")
	}
	return key, nil
}
