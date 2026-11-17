// Package illuminance keeps hardware capability separate from sample availability.
package illuminance

import (
	"context"
	"errors"
	"math"
	"sync"
	"time"
)

const Interval = 5 * time.Second
const MaxAge = 30 * time.Second

// Reading is driver-reported lux, not a claim of calibrated photometric accuracy.
// At is the source observation time, never the time a cached reading is requested.
type Reading struct {
	Lux float64
	At  time.Time
}

// Source exists only after positive platform capability discovery. A temporary
// permission, disabled-device or read failure returns an error, not a nil source.
// Read must honor cancellation; it must not acquire audio/session resources.
type Source interface {
	Read(context.Context) (Reading, error)
}

type Monitor struct {
	source  Source
	mu      sync.Mutex
	reading Reading
	err     error
}

func New(source Source) *Monitor {
	return &Monitor{source: source, err: errors.New("awaiting light sample")}
}
func (m *Monitor) Supported() bool { return m != nil && m.source != nil }

func (m *Monitor) Snapshot() (Reading, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.err != nil {
		return Reading{}, m.err
	}
	age := time.Since(m.reading.At)
	if age < 0 || age > MaxAge {
		return Reading{}, errors.New("light sample expired")
	}
	return m.reading, nil
}

// Run has one bounded, independent read lifecycle. It clears old values on every
// failed read and on shutdown; failures never change the entity catalog.
func (m *Monitor) Run(ctx context.Context, changed func()) {
	if !m.Supported() {
		return
	}
	poll := func() {
		readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
		r, err := m.source.Read(readCtx)
		if err == nil {
			err = readCtx.Err()
		}
		cancel()
		if err == nil && (math.IsNaN(r.Lux) || math.IsInf(r.Lux, 0) || r.Lux < 0 || r.Lux > math.MaxFloat32 || r.At.IsZero() || time.Since(r.At) < 0 || time.Since(r.At) > MaxAge) {
			err = errors.New("invalid or expired light sample")
		}
		m.mu.Lock()
		m.reading, m.err = r, err
		m.mu.Unlock()
		changed()
	}
	defer func() {
		m.mu.Lock()
		m.reading, m.err = Reading{}, context.Canceled
		m.mu.Unlock()
		changed()
	}()
	poll()
	ticker := time.NewTicker(Interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			poll()
		}
	}
}
