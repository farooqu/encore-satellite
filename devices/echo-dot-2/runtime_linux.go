package dot

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"os"
	"slices"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/libp2p/zeroconf/v2"
	esphome "github.com/ygelfand/go-esphome-device"
	"github.com/ygelfand/go-esphome-device/mdns"

	"github.com/farooqu/encore-satellite/backends/bluetooth"
	"github.com/farooqu/encore-satellite/backends/incubator"
	"github.com/farooqu/encore-satellite/core/ble"
	"github.com/farooqu/encore-satellite/core/session"
	"github.com/farooqu/encore-satellite/platforms/echo"
	"github.com/farooqu/encore-satellite/platforms/echo/alsa"
	"github.com/farooqu/encore-satellite/platforms/linuxble/bluez"
)

// Run opens the real host before exposing this package's sole Incubator backend. It does
// not provision the base, start Wi-Fi, execute shell commands or run HTTP control.
// ReadyPath is ephemeral startup readiness, NOT a hardware qualification result.
func Run(parent context.Context, c Config) error {
	_ = os.Remove(ReadyPath)
	defer os.Remove(ReadyPath)
	if err := syscall.Sethostname([]byte(c.Name)); err != nil {
		return fmt.Errorf("dot: setting configured hostname: %w", err)
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	gate := echo.NewPrivacy(func() (bool, error) { return echo.ReadPrivacy(privacyState) })
	muted, err := gate.Check(nil)
	if err != nil {
		return errors.New("dot: authoritative privacy driver is required")
	}
	mixer, err := alsa.OpenMixer(0)
	if err != nil {
		return err
	}
	defer mixer.Close()
	defer mixer.SetEnum("Ext_Speaker_Amp_Switch", "Off")
	if err = configureMixer(mixer); err != nil {
		return fmt.Errorf("dot: speaker routing: %w", err)
	}
	pcm, err := alsa.OpenPlayback(0, 23, alsa.Config{Channels: 2, Rate: 48000, Format: alsa.FormatS16_LE, Bits: 16, PeriodSize: 1024, Periods: 4})
	if err != nil {
		return err
	}
	player := newSpeaker(pcm)
	defer player.Close()
	mic, err := alsa.OpenPrepared(0, 24, alsa.Config{Channels: 9, Rate: 16000, Format: alsa.FormatS24_3LE, Bits: 24, PeriodSize: 320, Periods: 8})
	if err != nil {
		return err
	}
	defer mic.Close()
	if err = configureMic(mixer); err != nil {
		return fmt.Errorf("dot: microphone routing: %w", err)
	}
	loss, err := captureLossMonitor(mixer)
	if err != nil {
		return err
	}
	devices, err := openButtons()
	if err != nil {
		return err
	}
	defer func() {
		for _, d := range devices {
			_ = d.Close()
		}
	}()
	if err = os.WriteFile(ringPath+"boot_animation", []byte("0"), 0644); err != nil {
		return err
	}
	if err = os.WriteFile(ringPath+"led_current", []byte("3"), 0644); err != nil {
		return err
	}
	engine := session.New(player)
	engine.SetActivityHandler(player.SetVoiceActive)
	player.SetMusicPrivacy(muted)
	if err = engine.Select(session.Incubator); err != nil {
		return err
	}
	engine.SetMuted(muted)
	defer engine.SetMuted(true)
	if err = writeRing(engine.Snapshot()); err != nil {
		return err
	}
	defer func() { engine.SetMuted(true); _ = writeRing(engine.Snapshot()) }()
	if err = mixer.SetEnum("Ext_Speaker_Amp_Switch", "On"); err != nil {
		return err
	}

	iface, err := net.InterfaceByName("wlan0")
	if err != nil || len(iface.HardwareAddr) != 6 {
		return errors.New("dot: wlan0 with a real six-byte MAC is required")
	}
	mac := iface.HardwareAddr
	if mac[0]&1 != 0 || mac.String() == "00:00:00:00:00:00" {
		return errors.New("dot: invalid wlan0 MAC")
	}
	addresses, err := interfaceIPv4(iface)
	if err != nil {
		return err
	}
	var ips []net.IP
	for _, address := range addresses {
		ips = append(ips, net.ParseIP(address))
	}
	if len(ips) == 0 {
		return errors.New("dot: wlan0 IPv4 address is required before startup")
	}
	baseGeneration, err := os.ReadFile("/run/encore/bluetooth-ready")
	if err != nil || len(baseGeneration) == 0 {
		return errors.New("dot: Bluetooth base is not running")
	}
	// Required BLE base is validated even when the optional proxy is OFF.
	if err = bluez.Validate(ctx); err != nil {
		return fmt.Errorf("dot: required Bluetooth base: %w", err)
	}
	scanner, err := bluez.OpenScanner(ctx)
	if err != nil {
		return err
	}
	defer scanner.Close()
	saved := &settings{path: ConfigPath, config: c}
	proxy := bluetooth.New(scanner, bluetooth.Options{
		Enabled: c.BluetoothProxy, Pairing: true,
		SaveEnabled: func(enabled bool) error {
			return saved.update(func(next *Config) { next.BluetoothProxy = enabled })
		},
		OpenCentral: func(value func(string, uint16, []byte), lost func(string)) (ble.Central, error) {
			return bluez.OpenCentral(value, lost)
		},
	})
	admin, err := newManagement()
	if err != nil {
		return err
	}
	controls := &dotControls{Proxy: proxy, music: newMusic(c, saved, player), management: admin}
	key, err := noiseKey(KeyPath)
	if err != nil {
		return err
	}
	listener, err := net.Listen("tcp4", ":6053")
	if err != nil {
		return err
	}
	defer listener.Close()
	ad, err := mdns.Advertise(mdns.Config{Name: c.Name, FriendlyName: c.Name, Port: 6053, MACAddress: mac.String(), Version: "2026.10.1", Platform: "encore", Board: "echo-dot-2", Encrypted: true, IPs: ips})
	if err != nil {
		return err
	}
	defer ad.Close()
	voiceListener, err := net.Listen("tcp4", ":6055")
	if err != nil {
		return err
	}
	defer voiceListener.Close()
	voiceAd, err := zeroconf.RegisterProxy(c.Name, "_voice-incubator._tcp", "local.", 6055, c.Name, addresses, []string{"mac=" + mac.String(), "protocol=2"}, []net.Interface{*iface})
	if err != nil {
		return err
	}
	defer voiceAd.Shutdown()
	log.Print("Incubator is unencrypted and unauthenticated; use a trusted LAN only")
	if err = os.MkdirAll("/run/encore", 0755); err != nil {
		return err
	}
	var workers sync.WaitGroup
	failed := make(chan error, len(devices)+5)
	start := func(run func() error) { workers.Go(func() { failed <- run() }) }
	// Close pollable evdev readers before waiting, and stop capture before its
	// descriptor is released. No reader survives this Run invocation.
	defer func() {
		_ = os.Remove(ReadyPath)
		engine.SetMuted(true)
		cancel()
		_ = listener.Close()
		for _, d := range devices {
			_ = d.Close()
		}
		workers.Wait()
	}()
	start(func() error {
		select {
		case <-ctx.Done():
			return nil
		case <-player.Done():
			return player.Err()
		}
	})
	start(func() error {
		info := esphome.Info{Name: c.Name, FriendlyName: c.Name, MACAddress: mac.String(), Manufacturer: "Encore", Model: "Echo Dot 2", Version: "0.1.0-dev.0", ProjectName: "encore.satellite"}
		return controls.Serve(ctx, listener, info, key)
	})
	voice := &incubator.Server{Engine: engine, Name: c.Name, MAC: mac.String(), Version: "0.1.0-dev.0", EnableModels: true}
	start(func() error {
		return voice.Serve(ctx, voiceListener)
	})
	for _, d := range devices {
		start(func() error { return buttons(ctx, d, gate, engine, player) })
	}
	start(func() error {
		t := time.NewTicker(50 * time.Millisecond)
		defer t.Stop()
		nextAddressCheck := time.Now().Add(time.Second)
		for {
			select {
			case <-ctx.Done():
				return nil
			case now := <-t.C:
				if !now.Before(nextAddressCheck) {
					nextAddressCheck = now.Add(time.Second)
					current, addressErr := interfaceIPv4(iface)
					if addressErr != nil || !slices.Equal(current, addresses) {
						// The separately supervised network worker still owns
						// rollback. Recreate both advertisements on the new IP.
						return errors.New("dot: network address changed; reinitialization required")
					}
				}
				generation, e := os.ReadFile("/run/encore/bluetooth-ready")
				if e != nil || string(generation) != string(baseGeneration) {
					return errors.New("dot: Bluetooth base restarted; reinitialization required")
				}
				if e := writeRing(engine.Snapshot()); e != nil {
					return e
				}
			}
		}
	})
	ready := make(chan struct{}, 1)
	start(func() error {
		return capture(ctx, mic, loss, gate, voice, func() { ready <- struct{}{} }, player.SetMusicPrivacy)
	})
	for {
		select {
		case <-ready:
			if err = os.WriteFile(ReadyPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0644); err != nil {
				return err
			}
			log.Printf("Encore %s ready (Incubator package); Action or app-pushed MixedNet wake models; no AEC/beamformer", c.Name)
		case err = <-failed:
			engine.SetMuted(true)
			if err == nil && ctx.Err() == nil {
				return errors.New("dot: runtime worker stopped unexpectedly")
			}
			return err
		case <-ctx.Done():
			return nil
		}
	}
}

func interfaceIPv4(iface *net.Interface) ([]string, error) {
	addrs, err := iface.Addrs()
	if err != nil {
		return nil, err
	}
	var addresses []string
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok && n.IP.To4() != nil && !n.IP.IsLoopback() && !n.IP.IsUnspecified() && !n.IP.IsLinkLocalUnicast() {
			addresses = append(addresses, n.IP.String())
		}
	}
	slices.Sort(addresses)
	return addresses, nil
}
