package illuminance_test

import (
	"context"
	"errors"
	"math"
	"sync"
	"testing"
	"testing/synctest"
	"time"

	"github.com/farooqu/encore-satellite/core/illuminance"
)

type sourceFunc func(context.Context) (illuminance.Reading, error)

func (f sourceFunc) Read(ctx context.Context) (illuminance.Reading, error) { return f(ctx) }

func TestCapabilitySurvivesFailureAndReadingsExpire(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		var absent *illuminance.Monitor
		if absent.Supported() || illuminance.New(nil).Supported() {
			t.Fatal("absent hardware supported")
		}
		r := illuminance.Reading{Lux: 213.5, At: time.Now()}
		var failure error
		var mu sync.Mutex
		m := illuminance.New(sourceFunc(func(context.Context) (illuminance.Reading, error) {
			mu.Lock()
			defer mu.Unlock()
			return r, failure
		}))
		if _, err := m.Snapshot(); err == nil {
			t.Fatal("invented initial value")
		}
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		go m.Run(ctx, func() {})
		synctest.Wait()
		if got, err := m.Snapshot(); err != nil || got.Lux != 213.5 {
			t.Fatalf("initial reading: %+v %v", got, err)
		}
		mu.Lock()
		failure = errors.New("permission denied")
		mu.Unlock()
		time.Sleep(illuminance.Interval)
		synctest.Wait()
		if got, err := m.Snapshot(); err == nil || got.Lux != 0 || !m.Supported() {
			t.Fatalf("stale reading or lost hardware: %+v %v", got, err)
		}
		mu.Lock()
		failure = nil
		r = illuminance.Reading{Lux: 0, At: time.Now()}
		mu.Unlock()
		time.Sleep(illuminance.Interval)
		synctest.Wait()
		if got, err := m.Snapshot(); err != nil || got.Lux != 0 {
			t.Fatalf("zero lux is valid: %+v %v", got, err)
		}
		time.Sleep(illuminance.MaxAge)
		synctest.Wait()
		if _, err := m.Snapshot(); err == nil {
			t.Fatal("re-reading cached data refreshed its age")
		}
		cancel()
		synctest.Wait()
		if _, err := m.Snapshot(); err == nil || !m.Supported() {
			t.Fatal("shutdown left a value or removed capability")
		}
	})
}

func TestInvalidSamplesAndReadDeadline(t *testing.T) {
	for _, value := range []float64{-1, math.NaN(), math.Inf(1), math.MaxFloat64} {
		t.Run("invalid", func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				m := illuminance.New(sourceFunc(func(context.Context) (illuminance.Reading, error) {
					return illuminance.Reading{Lux: value, At: time.Now()}, nil
				}))
				ctx, cancel := context.WithCancel(context.Background())
				go m.Run(ctx, func() {})
				synctest.Wait()
				if _, err := m.Snapshot(); err == nil {
					t.Fatal("accepted invalid lux")
				}
				cancel()
			})
		})
	}
	synctest.Test(t, func(t *testing.T) {
		m := illuminance.New(sourceFunc(func(ctx context.Context) (illuminance.Reading, error) {
			<-ctx.Done()
			return illuminance.Reading{Lux: 99, At: time.Now()}, nil
		}))
		ctx, cancel := context.WithCancel(context.Background())
		go m.Run(ctx, func() {})
		time.Sleep(3 * time.Second)
		synctest.Wait()
		if _, err := m.Snapshot(); err == nil {
			t.Fatal("accepted read past deadline")
		}
		cancel()
	})
}
