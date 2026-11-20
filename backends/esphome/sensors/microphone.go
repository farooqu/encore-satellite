package sensors

import (
	"sync"

	"google.golang.org/protobuf/proto"
)

type statePeer interface {
	Send(proto.Message) error
	Close() error
}

// Microphone reports a device's effective privacy state without letting slow
// subscribers block the hardware or software mute path.
type Microphone struct {
	mu    sync.Mutex
	muted bool
	peers map[statePeer]chan struct{}
}

func NewMicrophone(muted bool) *Microphone {
	return &Microphone{muted: muted, peers: make(map[statePeer]chan struct{})}
}

func (m *Microphone) Muted() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.muted
}

func (m *Microphone) SetMuted(muted bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.muted == muted {
		return
	}
	m.muted = muted
	for _, wake := range m.peers {
		queueMicrophoneState(wake)
	}
}

// Subscribe queues an initial snapshot on the same per-peer writer as live
// updates. Slow peers coalesce updates without delaying other connections.
func (m *Microphone) Subscribe(peer statePeer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	wake := m.peers[peer]
	if wake == nil {
		wake = make(chan struct{}, 1)
		m.peers[peer] = wake
		go m.publish(peer, wake)
	}
	queueMicrophoneState(wake)
}

func queueMicrophoneState(wake chan<- struct{}) {
	select {
	case wake <- struct{}{}:
	default:
	}
}

func (m *Microphone) publish(peer statePeer, wake <-chan struct{}) {
	sources := Sources{MicrophoneMuted: m.Muted}
	for range wake {
		if err := peer.Send(sources.MicrophoneState()); err != nil {
			_ = peer.Close()
			return
		}
	}
}

// Unsubscribe removes a closed connection and releases its writer.
func (m *Microphone) Unsubscribe(peer statePeer) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if wake := m.peers[peer]; wake != nil {
		delete(m.peers, peer)
		close(wake)
	}
}
