package mobile

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/farooqu/encore-satellite/core/illuminance"
)

// LightHost is the native TYPE_LIGHT boundary. Android supplies one only when
// SensorManager finds a light sensor, regardless of its current availability.
// Sample returns an atomic JSON snapshot {lux, age_ms, problem}. age_ms is based
// on SensorEvent.timestamp and Android's monotonic clock, not callback receipt.
type LightHost interface {
	Sample() string
}

type lightSource struct{ host LightHost }

func (s lightSource) Read(ctx context.Context) (illuminance.Reading, error) {
	observed := time.Now()
	var sample struct {
		Lux     *float64 `json:"lux"`
		Age     *int64   `json:"age_ms"`
		Problem string   `json:"problem"`
	}
	if err := json.Unmarshal([]byte(s.host.Sample()), &sample); err != nil {
		return illuminance.Reading{}, err
	}
	if err := ctx.Err(); err != nil {
		return illuminance.Reading{}, err
	}
	if sample.Problem != "" {
		return illuminance.Reading{}, errors.New(sample.Problem)
	}
	if sample.Lux == nil || sample.Age == nil || *sample.Age < 0 || *sample.Age > illuminance.MaxAge.Milliseconds() {
		return illuminance.Reading{}, errors.New("missing or expired native light sample")
	}
	return illuminance.Reading{Lux: *sample.Lux, At: observed.Add(-time.Duration(*sample.Age) * time.Millisecond)}, nil
}

// SetLightHost must precede Start so the catalog is stable for every connection.
// A nil host means no light sensor; transient failures are reported in samples.
func (r *Runtime) SetLightHost(host LightHost) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.cancel != nil {
		return errors.New("light capability must be set before startup")
	}
	var source illuminance.Source
	if host != nil {
		source = lightSource{host}
	}
	r.light = illuminance.New(source)
	return nil
}
