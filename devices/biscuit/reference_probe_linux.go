package biscuit

import (
	"context"
	"errors"
	"log"
	"math"
	"os"
	"path/filepath"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/farooqu/encore-satellite/platforms/linux"
)

const (
	probeWindow = 16000 // One contiguous second of capture, independent of read sizes.
	probeStride = 8     // Average groups for a 0.5 ms lag grid.
	probeMaxLag = 200   // +/-100 ms, not a suggested AEC filter length.
)

type referenceProbe struct {
	arm     atomic.Pointer[referenceArm] // set once by the local trigger after READY
	reports chan referenceReport
	slow    int // capture owner only
	count   int
	sums    [3]int32
	series  [3][probeWindow / probeStride]float64 // mic mix, then both candidate refs
	energy  [2]int64
	clips   [2]int
}

type referenceArm struct {
	started time.Time
	ctx     context.Context
	stop    context.CancelCauseFunc
}

var (
	errReferencePrivacy = errors.New("physical privacy is muted or unavailable")
	errReferenceLoss    = errors.New("capture loss")
	errReferenceSlow    = errors.New("two consecutive scans exceeded 5 ms")
)

type referenceReport struct {
	at, scanTime time.Duration
	lane         [2]struct {
		rms, correlation float64
		clips, lag       int
	}
}

func (p *referenceProbe) publish(arm *referenceArm, report referenceReport) {
	select {
	case p.reports <- report:
	default: // Never backpressure capture.
	}
	if report.scanTime <= 5*time.Millisecond {
		p.slow = 0
	} else {
		p.slow++
		if p.slow >= 2 {
			arm.stop(errReferenceSlow)
		}
	}
}

func (p *referenceProbe) reset() {
	p.count, p.sums, p.energy, p.clips = 0, [3]int32{}, [2]int64{}, [2]int{}
	clear(p.series[:])
}

// observe runs on the capture owner, after the read and loss checks. Only
// scalar results leave this process-local window, and only after the caller's
// final privacy check. No ALSA timing is inferred from the read's timestamp.
func (p *referenceProbe) observe(raw []byte, mic []int16, at time.Duration) (referenceReport, bool) {
	var report referenceReport
	complete := false
	for i, sample := range mic {
		p.sums[0] += int32(sample) << 8
		for lane := range 2 {
			v := packed24(raw[i*27+(7+lane)*3:])
			p.sums[lane+1] += v
			p.energy[lane] += int64(v) * int64(v)
			if v == -1<<23 || v == 1<<23-1 {
				p.clips[lane]++
			}
		}
		p.count++
		if p.count%probeStride == 0 {
			for ch, sum := range p.sums {
				p.series[ch][p.count/probeStride-1] = float64(sum) / probeStride
			}
			p.sums = [3]int32{}
		}
		if p.count == probeWindow {
			report.at = at
			started := time.Now()
			for lane := range 2 {
				r := &report.lane[lane]
				r.rms = math.Sqrt(float64(p.energy[lane])/probeWindow) / (1 << 23)
				r.clips = p.clips[lane]
				r.lag, r.correlation = referenceLag(p.series[0][:], p.series[lane+1][:])
			}
			p.reset()
			report.scanTime = time.Since(started)
			complete = true
		}
	}
	return report, complete
}

// Use the same reference samples for each candidate lag. Positive lag means
// the mic trails the reference. The coefficient retains the path polarity.
func referenceLag(mic, ref []float64) (bestLag int, best float64) {
	x := ref[probeMaxLag : len(ref)-probeMaxLag]
	n := float64(len(x))
	var sx, xx float64
	for _, v := range x {
		sx += v
		xx += v * v
	}
	vx := xx - sx*sx/n
	if vx <= 0 {
		return 0, 0
	}
	for lag := -probeMaxLag; lag <= probeMaxLag; lag++ {
		y := mic[probeMaxLag+lag:]
		var sy, yy, xy float64
		for i, v := range x {
			w := y[i]
			sy += w
			yy += w * w
			xy += v * w
		}
		if vy := yy - sy*sy/n; vy > 0 {
			r := (xy - sx*sy/n) / math.Sqrt(vx*vy)
			if math.Abs(r) > math.Abs(best) {
				bestLag, best = lag, r
			}
		}
	}
	return bestLag * probeStride, best
}

// run waits after READY with no observation deadline and no PCM work. An invalid
// or muted trigger is terminal for this process; unmute cannot arm or resume it.
func (p *referenceProbe) run(ctx context.Context, marker string, gate *linux.Privacy, duration time.Duration) error {
	dir, err := os.Lstat(filepath.Dir(marker))
	if err != nil || !dir.IsDir() || dir.Mode().Perm() != 0700 || dir.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
		return errors.New("owned mode-0700 RAM directory required")
	}
	if _, err := os.Lstat(marker); !errors.Is(err, os.ErrNotExist) {
		return errors.New("arm marker must be absent at READY")
	}
	log.Printf("dot: reference probe waiting marker=%s", marker)
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
		info, err := os.Lstat(marker)
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0600 || info.Size() != 0 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Geteuid()) {
			return errors.New("invalid arm marker")
		}
		break
	}
	ticker.Stop()
	var arm *referenceArm
	var expire context.CancelFunc
	muted, err := gate.Check(func() {
		if ctx.Err() == nil {
			var deadline context.Context
			deadline, expire = context.WithTimeout(ctx, duration)
			observation, stop := context.WithCancelCause(deadline)
			arm = &referenceArm{time.Now(), observation, stop}
			p.arm.Store(arm)
		}
	})
	if muted || err != nil {
		return errReferencePrivacy
	}
	if arm == nil {
		return ctx.Err()
	}
	defer expire()
	defer arm.stop(nil)
	log.Printf("dot: reference probe armed elapsed_ms=0 limit_ms=%d; scalar results only", duration.Milliseconds())
	return p.log(arm.ctx)
}

func (p *referenceProbe) log(ctx context.Context) error {
	for ctx.Err() == nil {
		select {
		case <-ctx.Done():
			return context.Cause(ctx)
		case r := <-p.reports:
			if ctx.Err() != nil {
				return context.Cause(ctx)
			}
			l, h := r.lane[0], r.lane[1]
			log.Printf("dot: reference probe elapsed_ms=%d scan_us=%d lane7_rms=%.7f lane7_clips=%d lane7_lag_samples=%d lane7_r=%.4f lane8_rms=%.7f lane8_clips=%d lane8_lag_samples=%d lane8_r=%.4f",
				r.at.Milliseconds(), r.scanTime.Microseconds(), l.rms, l.clips, l.lag, l.correlation, h.rms, h.clips, h.lag, h.correlation)
		}
	}
	return context.Cause(ctx)
}
