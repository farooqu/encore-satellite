// Package mobile binds the Personal Mic voice-only composition with gomobile.
package mobile

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/farooqu/encore-satellite/backends/incubator"
	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/core/presentation"
	"github.com/farooqu/encore-satellite/core/session"
	androidhost "github.com/farooqu/encore-satellite/platforms/android"
)

const Port = 6055

type Audio interface {
	WriteVoice(pcm []byte, epoch int64) bool
	DrainVoice(epoch int64) bool
	FlushVoice(epoch int64)
}

// PresentationObserver must enqueue the callback on the Android thread, not
// synchronously call back into the runtime.
type PresentationObserver interface{ Changed(snapshot string) }

// Android serializes listener changes and stops and joins capture before Close.
type Runtime struct {
	voice    *incubator.Server
	cancel   context.CancelFunc
	done     chan struct{}
	local    netip.Addr
	peer     netip.Addr
	sequence uint64
	samples  int64
}

func Open(directory string, output Audio) (*Runtime, error) {
	if output == nil {
		return nil, errors.New("native audio required")
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		return nil, err
	}
	path := filepath.Join(directory, "id")
	id, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		var bytes [6]byte
		if _, err := rand.Read(bytes[:]); err != nil {
			return nil, err
		}
		bytes[0] = (bytes[0] | 2) & 0xfe
		id = []byte(net.HardwareAddr(bytes[:]).String())
		if err := os.WriteFile(path+".new", id, 0600); err != nil {
			return nil, err
		}
		err = os.Rename(path+".new", path)
	}
	if err != nil {
		return nil, err
	}
	mac, err := net.ParseMAC(string(id))
	if err != nil || len(mac) != 6 {
		return nil, errors.New("invalid saved Personal Mic identity")
	}
	name := "encore-personal-" + strings.ReplaceAll(mac.String()[9:], ":", "")
	return &Runtime{voice: &incubator.Server{
		Engine: session.New(androidhost.NewPlayback(output)), Name: name, MAC: mac.String(),
		Version: "0.1.0-personal", EnableModels: true,
		EarlyTranscript: true, ReplyChunks: true, TimerNames: true,
	}}, nil
}

func (r *Runtime) Name() string { return r.voice.Name }
func (r *Runtime) MAC() string  { return r.voice.MAC }

func usableIPv4(ip string) netip.Addr {
	address, err := netip.ParseAddr(ip)
	if err != nil || !address.Is4() || !address.IsGlobalUnicast() {
		return netip.Addr{}
	}
	return address
}

// RefreshRoute asks the OS which source address it would use for Incubator.
// UDP connect selects the route without sending a packet. Only an actual
// incoming Incubator connection makes Talk or wake ready.
func (r *Runtime) RefreshRoute(serverIP string) (string, error) {
	peer := usableIPv4(serverIP)
	if !peer.IsValid() {
		return "", errors.New("use a unicast IPv4 address for Incubator")
	}
	var local netip.Addr
	if probe, err := net.DialUDP("udp4", nil, net.UDPAddrFromAddrPort(netip.AddrPortFrom(peer, Port))); err == nil {
		local = usableIPv4(probe.LocalAddr().(*net.UDPAddr).AddrPort().Addr().Unmap().String())
		_ = probe.Close()
	}
	return r.listen(local, peer)
}

func (r *Runtime) listen(local, peer netip.Addr) (string, error) {
	if local == r.local && peer == r.peer {
		return local.String(), nil
	}
	r.stopListening()
	if !local.IsValid() || local == peer {
		return "", nil
	}
	l, err := net.ListenTCP("tcp4", net.TCPAddrFromAddrPort(netip.AddrPortFrom(local, Port)))
	if err != nil {
		return "", err
	}
	r.local, r.peer = local, peer
	r.serve(&peerListener{l, peer})
	return local.String(), nil
}

type peerListener struct {
	*net.TCPListener
	peer netip.Addr
}

func (l *peerListener) Accept() (net.Conn, error) {
	for {
		c, err := l.AcceptTCP()
		if err != nil {
			return nil, err
		}
		if c.RemoteAddr().(*net.TCPAddr).AddrPort().Addr().Unmap() == l.peer {
			_ = c.SetKeepAliveConfig(net.KeepAliveConfig{Enable: true, Idle: 15 * time.Second, Interval: 5 * time.Second, Count: 3})
			return c, nil
		}
		_ = c.Close()
	}
}

func (r *Runtime) serve(l net.Listener) {
	ctx, cancel := context.WithCancel(context.Background())
	r.cancel, r.done = cancel, make(chan struct{})
	go func() {
		defer close(r.done)
		defer l.Close()
		_ = r.voice.Serve(ctx, l)
	}()
}

func (r *Runtime) Close() {
	r.voice.Engine.SetMuted(true)
	r.stopListening()
}

func (r *Runtime) stopListening() {
	if r.done != nil {
		r.cancel()
		<-r.done
		r.done = nil
		r.local, r.peer = netip.Addr{}, netip.Addr{}
	}
}

func (r *Runtime) Capture(pcm []byte) error {
	if len(pcm) == 0 || len(pcm)%2 != 0 || len(pcm) > audio.MaxSamples*2 {
		return errors.New("invalid PCM block")
	}
	samples := make([]int16, len(pcm)/2)
	for i := range samples {
		samples[i] = int16(binary.LittleEndian.Uint16(pcm[2*i:]))
	}
	frame := audio.Frame{Samples: samples, Sequence: r.sequence, CapturedAt: time.Duration(r.samples) * time.Second / audio.SampleRate}
	r.sequence++
	r.samples += int64(len(samples))
	return r.voice.Capture(frame)
}

func (r *Runtime) SetWakeWordEnabled(enabled bool) { r.voice.SetWakeWordEnabled(enabled) }
func (r *Runtime) Talk() error                     { return r.voice.Engine.StartManual() }
func (r *Runtime) Stop()                           { _ = r.voice.Engine.Stop() }

func (r *Runtime) StopTurn(generation, turn int64) {
	r.voice.Engine.StopTurn(uint64(generation), uint64(turn))
}

func (r *Runtime) ObservePresentation(observer PresentationObserver) {
	if observer == nil {
		r.voice.Engine.SetPresentationChanged(nil)
		return
	}
	r.voice.Engine.SetPresentationChanged(func(state presentation.State) {
		b, _ := json.Marshal(state)
		observer.Changed(string(b))
	})
}

func (r *Runtime) Snapshot(captureReady bool) string {
	b, _ := json.Marshal(r.voice.Snapshot(captureReady))
	return string(b)
}
