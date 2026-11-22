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

// Runtime has one listener lifetime. Android stops and joins capture before Close.
type Runtime struct {
	voice    *incubator.Server
	cancel   context.CancelFunc
	done     chan struct{}
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
	}}, nil
}

func (r *Runtime) Name() string { return r.voice.Name }
func (r *Runtime) MAC() string  { return r.voice.MAC }

func IsPrivateIPv4(ip string) bool {
	address, err := netip.ParseAddr(ip)
	return err == nil && address.Is4() && address.IsPrivate()
}

func (r *Runtime) Start(localIP, serverIP string) error {
	if r.done != nil {
		return errors.New("Personal Mic already started")
	}
	if !IsPrivateIPv4(localIP) || !IsPrivateIPv4(serverIP) || localIP == serverIP {
		return errors.New("use distinct private IPv4 addresses for Wi-Fi and Incubator")
	}
	l, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP(localIP), Port: Port})
	if err != nil {
		return err
	}
	r.serve(&peerListener{l, netip.MustParseAddr(serverIP)})
	return nil
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
	if r.done != nil {
		r.cancel()
		<-r.done
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

func (r *Runtime) Talk() error { return r.voice.Engine.StartManual() }
func (r *Runtime) Stop()       { _ = r.voice.Engine.Stop() }

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
