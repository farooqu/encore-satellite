package echo

import (
	"errors"
	"os"
	"strings"
	"sync"
	"time"
)

// Privacy gates acquisition and delivery using an authoritative OS state read.
// Unknown/malformed state is always muted. The hardware driver owns toggling;
// a key event only inhibits capture during its delayed state transition.
type Privacy struct {
	mu    sync.Mutex
	read  func() (bool, error)
	until time.Time
}

func NewPrivacy(read func() (bool, error)) *Privacy { return &Privacy{read: read} }

func ReadPrivacy(path string) (bool, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return true, err
	}
	switch strings.TrimSpace(string(b)) {
	case "0":
		return false, nil
	case "1":
		return true, nil
	default:
		return true, errors.New("echo: invalid privacy state")
	}
}

func (p *Privacy) Inhibit(d time.Duration) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.until = time.Now().Add(d)
}

// Check holds the gate through a short nonblocking OS read or engine delivery.
// Call on both sides of capture; never retain a block across a denied check.
func (p *Privacy) Check(deliver func()) (bool, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	muted, err := p.read()
	muted = muted || err != nil || time.Now().Before(p.until)
	if !muted && deliver != nil {
		deliver()
	}
	return muted, err
}
