package biscuit

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
	"github.com/farooqu/encore-satellite/backends/esphome/sensors"
	"github.com/farooqu/encore-satellite/backends/incubator"
	"github.com/farooqu/encore-satellite/core/ble"
	"github.com/farooqu/encore-satellite/core/session"
	"github.com/farooqu/encore-satellite/platforms/linux"
	"github.com/farooqu/encore-satellite/platforms/linux/alsa"
	"github.com/farooqu/encore-satellite/platforms/linuxble/bluez"
)

func packagedExecutable(path string) bool {
	info, err := os.Stat(path)
	return err == nil && info.Mode().IsRegular() && info.Mode().Perm()&0111 != 0
}

// Run opens the real host before exposing this package's sole Incubator backend. It does
// not provision the base, start Wi-Fi, execute shell commands or run HTTP control.
// ReadyPath is ephemeral startup readiness, NOT a hardware qualification result.
func Run(parent context.Context, c Config, probeReference bool) error {
	_ = os.Remove(ReadyPath)
	defer os.Remove(ReadyPath)
	if err := syscall.Sethostname([]byte(c.Name)); err != nil {
		return fmt.Errorf("dot: setting configured hostname: %w", err)
	}
	ctx, cancel := context.WithCancel(parent)
	defer cancel()
	gate := linux.NewPrivacy(func() (bool, error) { return linux.ReadPrivacy(privacyState) })
	// An unreadable state is muted; the capture worker keeps checking it.
	muted, _ := gate.Check(nil)
	mixer, err := alsa.OpenMixer(0)
	if err != nil {
		return err
	}
	defer mixer.Close()
	defer mixer.SetEnum("Ext_Speaker_Amp_Switch", "Off")
	if err = configureMixer(mixer); err != nil {
		return fmt.Errorf("dot: speaker routing: %w", err)
	}
	// Wake at or below 35 ms; refill remains capped at the 40 ms lead.
	pcm, err := alsa.OpenPlayback(0, 23, alsa.Config{Channels: 2, Rate: 48000, Format: alsa.FormatS16_LE, Bits: 16, PeriodSize: 1024, Periods: 4, PlaybackWakeLead: speakerLeadFrames - 240})
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
	engine := session.New(player)
	engine.SetActivityHandler(player.SetVoiceActive)
	player.SetMusicPrivacy(muted)
	if err = engine.Select(session.Incubator); err != nil {
		return err
	}
	engine.SetMuted(muted)
	defer engine.SetMuted(true)
	ringPolicy, err := loadRingSettings(ringSettingsPath)
	if err != nil {
		return err
	}
	ring := &ringRenderer{volume: player.volumeFeedback, write: func(frame []byte) error {
		// The kernel latches quarter current with every scaled frame.
		return os.WriteFile(ringPath+"scaled_frame", scaledRingFrame(frame), 0644)
	}}
	if err = ring.update(engine.Snapshot(), captureReading{}, time.Now(), ringPolicy.snapshot()); err != nil {
		return err
	}
	defer func() {
		engine.SetMuted(true)
		_ = ring.update(engine.Snapshot(), captureReading{}, time.Now(), ringPolicy.snapshot())
	}()
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
	var ssh *sshManager
	if packagedExecutable("/usr/local/sbin/encore-ssh") {
		ssh = newSSHManager()
	}
	admin, err := newManagement(ssh)
	if err != nil {
		return err
	}
	controls := &biscuitControls{Proxy: proxy, player: player, management: admin, ring: ringPolicy, announcements: newAnnouncementPlayer(engine, player), microphone: sensors.NewMicrophone(muted)}
	if controls.updates, err = newARM64Updates("/data/encore/arm64-update-channel.json", "/run/encore/arm64-installed.json"); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("read ARM64 boot identity: %w", err)
	}
	if packagedExecutable(musicExecutable) {
		controls.music = newMusic(c, saved, player)
	}
	apiAction := make(chan struct{})
	apiAd := mdns.Config{Name: c.Name, FriendlyName: c.Name, Port: esphome.DefaultPort, MACAddress: mac.String(), Version: "2026.10.1", Platform: "encore", Board: "biscuit", IPs: ips}
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
		info := esphome.Info{Name: c.Name, FriendlyName: c.Name, MACAddress: mac.String(), Manufacturer: "Encore Satellite", Model: "Echo Dot (2nd gen)", Version: "0.1.0-dev.0", ProjectName: "encore.satellite"}
		return controls.serveAPI(ctx, KeyPath, apiAction, info, apiAd)
	})
	voice := &incubator.Server{Engine: engine, Name: c.Name, MAC: mac.String(), Version: "0.1.0-dev.0", EnableModels: true, CallAEC: true, MonitorAEC: true}
	start(func() error {
		return voice.Serve(ctx, voiceListener)
	})
	for _, d := range devices {
		start(func() error { return buttons(ctx, d, gate, engine, player, apiAction) })
	}
	meter := &captureMeter{origin: time.Now()}
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
				now = time.Now()
				reading := meter.snapshot(now)
				state := voice.Snapshot(reading.ready)
				if e := ring.update(state, reading, now, ringPolicy.snapshot()); e != nil {
					return e
				}
				if ringMode(state) == "monitor" {
					engine.ConfirmMonitor(state.MonitorID)
				}
			}
		}
	})
	ready := make(chan struct{}, 1)
	var probe *referenceProbe
	if probeReference {
		probe = &referenceProbe{reports: make(chan referenceReport, 1)}
	}
	aec := &loopbackAEC{}
	start(func() error {
		return capture(ctx, mic, loss, gate, voice, meter, probe, aec, func() { ready <- struct{}{} }, player.SetMusicPrivacy, controls.microphone.SetMuted)
	})
	for {
		select {
		case <-ready:
			if err = os.WriteFile(ReadyPath, []byte(strconv.Itoa(os.Getpid())+"\n"), 0644); err != nil {
				return err
			}
			log.Printf("Encore %s ready (Incubator package); Action or app-pushed MixedNet wake models", c.Name)
			if probe != nil {
				workers.Go(func() {
					marker := fmt.Sprintf("/run/encore/aec-probe/arm.%d", os.Getpid())
					err := probe.run(ctx, marker, gate, 2*time.Minute)
					log.Printf("dot: reference probe stopped reason=%q; observation only", err.Error())
				})
			}
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
