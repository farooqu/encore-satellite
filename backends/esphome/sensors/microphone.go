package sensors

import (
	"context"
	"sync"
	"sync/atomic"

	"google.golang.org/protobuf/proto"
)

// Microphone reports a device's effective privacy state without letting slow
// subscribers block the hardware or software mute path.
type Microphone struct {
	muted   atomic.Bool
	changed chan struct{}
	sendMu  sync.Mutex
}

func NewMicrophone(muted bool) *Microphone {
	m := &Microphone{changed: make(chan struct{}, 1)}
	m.muted.Store(muted)
	return m
}

func (m *Microphone) Muted() bool { return m.muted.Load() }

func (m *Microphone) SetMuted(muted bool) {
	if m.muted.Swap(muted) != muted {
		select {
		case m.changed <- struct{}{}:
		default:
		}
	}
}

// Send serializes reading and writing so a subscription cannot receive an old
// snapshot after a newer broadcast. SetMuted does not take this lock.
func (m *Microphone) Send(send func(proto.Message) error) error {
	m.sendMu.Lock()
	defer m.sendMu.Unlock()
	return send((Sources{MicrophoneMuted: m.Muted}).MicrophoneState())
}

func (m *Microphone) Run(ctx context.Context, broadcast func(proto.Message) error) {
	for {
		select {
		case <-ctx.Done():
			return
		case <-m.changed:
			_ = m.Send(broadcast)
		}
	}
}
