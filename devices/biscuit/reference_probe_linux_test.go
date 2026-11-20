package biscuit

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"math/rand/v2"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"testing/synctest"
	"time"

	"github.com/farooqu/encore-satellite/platforms/linux"
)

func TestReferenceProbeWaitsForExplicitArmAndExpiresOnce(t *testing.T) {
	synctest.Test(t, func(t *testing.T) {
		marker := filepath.Join(t.TempDir(), "arm")
		if err := os.Chmod(filepath.Dir(marker), 0700); err != nil {
			t.Fatal(err)
		}
		var muted atomic.Bool
		muted.Store(true)
		gate := linux.NewPrivacy(func() (bool, error) { return muted.Load(), nil })
		p := &referenceProbe{reports: make(chan referenceReport, 1)}
		done := make(chan error, 1)
		go func() { done <- p.run(t.Context(), marker, gate, 2*time.Minute) }()
		time.Sleep(3 * time.Minute)
		muted.Store(false)
		time.Sleep(time.Second)
		if p.arm.Load() != nil || len(done) != 0 {
			t.Fatal("startup or physical unmute armed or expired the diagnostic")
		}
		trigger := time.Now()
		if err := os.WriteFile(marker, nil, 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(100 * time.Millisecond)
		arm := p.arm.Load()
		if arm == nil || arm.started.Before(trigger) || arm.ctx.Err() != nil {
			t.Fatal("fresh marker did not arm with a fresh deadline")
		}
		deadline, _ := arm.ctx.Deadline()
		time.Sleep(time.Until(deadline) - time.Millisecond)
		if arm.ctx.Err() != nil {
			t.Fatal("observation expired early")
		}
		if err := <-done; !errors.Is(err, context.DeadlineExceeded) || t.Context().Err() != nil {
			t.Fatal("expected diagnostic-only expiry", err)
		}
		if err := os.Remove(marker); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(marker, nil, 0600); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Second)
		if p.arm.Load() != arm || arm.ctx.Err() == nil {
			t.Fatal("diagnostic rearmed after expiry")
		}
	})
}

func TestReferenceProbeRejectsUnsafeArm(t *testing.T) {
	for _, mode := range []string{"stale", "invalid", "muted", "unreadable"} {
		t.Run(mode, func(t *testing.T) {
			synctest.Test(t, func(t *testing.T) {
				marker := filepath.Join(t.TempDir(), "arm")
				if err := os.Chmod(filepath.Dir(marker), 0700); err != nil {
					t.Fatal(err)
				}
				if mode == "stale" {
					if err := os.WriteFile(marker, nil, 0600); err != nil {
						t.Fatal(err)
					}
				}
				gate := linux.NewPrivacy(func() (bool, error) {
					if mode == "unreadable" {
						return false, errors.New("driver read failed")
					}
					return mode == "muted", nil
				})
				p := &referenceProbe{}
				done := make(chan error, 1)
				go func() { done <- p.run(t.Context(), marker, gate, time.Minute) }()
				synctest.Wait()
				if mode != "stale" {
					var payload []byte
					if mode == "invalid" {
						payload = []byte("not an empty marker")
					}
					if err := os.WriteFile(marker, payload, 0600); err != nil {
						t.Fatal(err)
					}
				}
				err := <-done
				if err == nil || errors.Is(err, context.DeadlineExceeded) || p.arm.Load() != nil {
					t.Fatal("unsafe trigger did not fail closed", err)
				}
			})
		})
	}
}

func TestReferenceProbeCutsOffSlowScansWithoutBackpressure(t *testing.T) {
	p := &referenceProbe{reports: make(chan referenceReport, 1)}
	ctx, stop := context.WithCancelCause(t.Context())
	defer stop(nil)
	arm := &referenceArm{time.Now(), ctx, stop}
	for _, d := range []time.Duration{6, 5, 6, 7} {
		if ctx.Err() != nil {
			t.Fatal("cut off without consecutive slow scans")
		}
		p.publish(arm, referenceReport{scanTime: d * time.Millisecond})
	}
	if !errors.Is(context.Cause(ctx), errReferenceSlow) || len(p.reports) != 1 {
		t.Fatal("slow observation did not stop with bounded scalar queue")
	}
}

func TestReferenceProbeReportsSignedLagAndLaneLevels(t *testing.T) {
	for _, tc := range []struct {
		name string
		lag  int
	}{{"reference leads", 240}, {"reference trails", -552}} {
		t.Run(tc.name, func(t *testing.T) {
			rng := rand.New(rand.NewPCG(3, 91))
			render := make([]int16, probeWindow+2000)
			for i := range render {
				render[i] = int16(rng.IntN(12001) - 6000)
			}
			mic := make([]int16, probeWindow)
			raw := make([]byte, 27*len(mic))
			for i := range mic {
				mic[i] = -render[1000+i-tc.lag] + 500 // Inverted echo and DC offset.
				ref := uint16(render[1000+i])
				binary.LittleEndian.PutUint16(raw[i*27+22:], ref)
			}
			// Lane 8 has exactly two rail samples, outside the correlation span.
			copy(raw[24:], []byte{0, 0, 0x80})
			copy(raw[27+24:], []byte{0xff, 0xff, 0x7f})
			var p referenceProbe
			const split = 197 // A short READ can end mid-decimation-group.
			if _, ready := p.observe(raw[:27*split], mic[:split], time.Millisecond); ready {
				t.Fatal("partial window reported")
			}
			report, ready := p.observe(raw[27*split:], mic[split:], time.Second)
			if !ready || report.at != time.Second {
				t.Fatalf("window not reported: %+v", report)
			}
			if r := report.lane[0]; r.lag != tc.lag || r.correlation > -0.9999 || r.clips != 0 || r.rms < 0.10 || r.rms > 0.11 {
				t.Fatalf("lane 7: %+v", r)
			}
			if r := report.lane[1]; r.correlation != 0 || r.clips != 2 || math.Abs(r.rms-math.Sqrt(2.0/16000)) > 1e-7 {
				t.Fatalf("lane 8: %+v", r)
			}
		})
	}
}
