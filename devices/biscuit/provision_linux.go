package biscuit

import (
	"context"
	"crypto/pbkdf2"
	"crypto/sha1"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/farooqu/encore-satellite/core/improv"
	"github.com/farooqu/encore-satellite/platforms/linux/input"
	"github.com/farooqu/encore-satellite/platforms/linuxble/bluez"
)

const wifiPath = "/data/encore/wpa_supplicant.conf"
const liveWiFiPath = "/run/encore/wpa_supplicant.conf"

func wifiConfig(w improv.WiFi) ([]byte, error) {
	if len(w.SSID) < 1 || len(w.SSID) > 32 || len(w.Passphrase) < 8 || len(w.Passphrase) > 63 {
		return nil, errors.New("dot: WPA2 requires a 1..32 byte SSID and 8..63 ASCII passphrase")
	}
	for _, c := range w.Passphrase {
		if c < 32 || c > 126 {
			return nil, errors.New("dot: WPA2 passphrase must be printable ASCII")
		}
	}
	key, err := pbkdf2.Key(sha1.New, w.Passphrase, []byte(w.SSID), 4096, 32)
	if err != nil {
		return nil, errors.New("dot: WPA2 derivation failed")
	}
	defer clear(key)
	return fmt.Appendf(nil, "ctrl_interface=/run/wpa_supplicant\nupdate_config=0\nnetwork={\n ssid=%s\n psk=%s\n}\n", hex.EncodeToString([]byte(w.SSID)), hex.EncodeToString(key)), nil
}

func wlanIPv4() net.IP {
	iface, err := net.InterfaceByName("wlan0")
	if err != nil {
		return nil
	}
	addresses, _ := iface.Addrs()
	for _, a := range addresses {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsUnspecified() && !n.IP.IsLinkLocalUnicast() {
			return n.IP.To4()
		}
	}
	return nil
}

func reconfigureWiFi(ctx context.Context) error {
	b, err := exec.CommandContext(ctx, "wpa_cli", "-i", "wlan0", "reconfigure").Output()
	if err != nil || strings.TrimSpace(string(b)) != "OK" {
		return errors.New("dot: supplicant reconfigure failed")
	}
	return nil
}

func joinWiFi(ctx context.Context, w improv.WiFi) (string, error) {
	b, err := wifiConfig(w)
	if err != nil {
		return "", err
	}
	defer clear(b)
	// Only used before initial provisioning. A failed candidate is not saved and
	// must not retain an old address that could masquerade as a successful join.
	saved := false
	defer func() {
		if !saved {
			_ = writePrivate(liveWiFiPath, []byte("ctrl_interface=/run/wpa_supplicant\n"))
			cleanup, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			_ = reconfigureWiFi(cleanup)
		}
	}()
	adapter := systemWiFi{}
	if err = adapter.apply(ctx, b); err != nil {
		return "", err
	}
	renewed := false
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	for {
		status, statusErr := exec.CommandContext(ctx, "wpa_cli", "-i", "wlan0", "status").Output()
		if statusErr == nil && strings.Contains("\n"+string(status), "\nwpa_state=COMPLETED\n") {
			if !renewed {
				// A DHCP daemon can still hold a lease from a prior connection.
				// Reuse its validated owner and require a fresh address.
				if err = adapter.renew(ctx); err != nil {
					return "", err
				}
				renewed = true
			}
			if ip := wlanIPv4(); ip != nil {
				if err = writePrivate(wifiPath, b); err != nil {
					return "", err
				}
				saved = true
				// Home Assistant's standard ESPHome onboarding surface. Do not
				// invent a device web server or expose the Noise key in a URL.
				return "https://my.home-assistant.io/redirect/config_flow_start/?domain=esphome", nil
			}
		}
		select {
		case <-ctx.Done():
			return "", ctx.Err()
		case <-tick.C:
		}
	}
}

// PrepareNetwork offers physically authorized Improv only when no Encore Wi-Fi
// file exists. An ordinary network outage never opens a provisioning window.
// It runs before either encrypted controls or voice owns the controller/buttons.
func PrepareNetwork(ctx context.Context, c Config) error {
	const provisioningReady = "/run/encore/provisioning-ready"
	_ = os.Remove(provisioningReady)
	defer os.Remove(provisioningReady)
	if _, err := os.Stat(wifiPath); errors.Is(err, os.ErrNotExist) {
		ctx, cancel := context.WithCancel(ctx)
		defer cancel()
		generation, err := os.ReadFile("/run/encore/bluetooth-ready")
		if err != nil {
			return err
		}
		devices, err := openButtons()
		if err != nil {
			return err
		}
		auth := make(chan struct{}, 1)
		var wg sync.WaitGroup
		defer func() {
			for _, d := range devices {
				_ = d.Close()
			}
			wg.Wait()
		}()
		for _, d := range devices {
			wg.Go(func() {
				for {
					e, err := d.Read()
					if err != nil {
						cancel() // A lost Action button is not healthy user waiting.
						return
					}
					if e.Type == input.EvKey && e.Code == 138 && e.Value == 1 {
						select {
						case auth <- struct{}{}:
						default:
						}
					}
				}
			})
		}
		log.Print("First setup: press Action to authorize Improv Wi-Fi; proxy remains off")
		if err = bluez.Provision(ctx, improv.Options{Name: c.Name, Version: "0.1.0-dev.0", Hardware: "Echo Dot 2", Authorize: auth, Join: joinWiFi,
			Health: func() error {
				if err := ctx.Err(); err != nil {
					return err
				}
				current, err := os.ReadFile("/run/encore/bluetooth-ready")
				if err != nil || string(current) != string(generation) {
					return errors.New("dot: Bluetooth supervisor changed during provisioning")
				}
				return writePrivate(provisioningReady, fmt.Appendf(nil, "%d %d\n", os.Getpid(), time.Now().Unix()))
			},
		}); err != nil {
			return err
		}
	} else if err != nil {
		return err
	}
	for wlanIPv4() == nil {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(time.Second):
		}
	}
	return nil
}
