//go:build linux

package linuxsendspin

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

// The test binary is also a deterministic companion protocol peer. It receives
// the same inherited endpoint as the real SDK process, without env/argv tokens.
func TestMain(m *testing.M) {
	if len(os.Args) > 3 && os.Args[1] == "run" {
		mode := filepath.Base(os.Args[3])
		f := os.NewFile(3, "ipc")
		nc, e := net.FileConn(f)
		f.Close()
		if e != nil {
			os.Exit(90)
		}
		c := nc.(*net.UnixConn)
		send := func(p packet) {
			if _, err := c.Write(encode(p)); err != nil {
				os.Exit(91)
			}
		}
		read := func() packet {
			b := make([]byte, headerSize+maxPCM)
			n, e := c.Read(b)
			if e != nil {
				os.Exit(92)
			}
			p, e := decode(b[:n])
			if e != nil {
				os.Exit(93)
			}
			return p
		}
		if mode == "no-read" {
			for {
				time.Sleep(time.Hour)
			}
		}
		send(packet{kind: msgStart, epoch: 1})
		switch mode {
		case "malformed":
			_, _ = c.Write([]byte("bad"))
		case "oversize":
			_, _ = c.Write(make([]byte, headerSize+maxPCM+1))
		case "exit":
			send(packet{kind: msgPCM, epoch: 1, id: 1, value: 1000, data: make([]byte, 192)})
			for read().kind != msgAccepted {
			}
			os.Exit(0)
		case "feedback":
			for epoch := uint64(1); epoch <= 2; epoch++ {
				if epoch == 2 {
					send(packet{kind: msgStart, epoch: epoch})
				}
				send(packet{kind: msgPCM, epoch: epoch, id: epoch, value: 1000, data: make([]byte, 192)})
				ack, progress := false, false
				for !ack || !progress {
					p := read()
					if p.epoch != epoch {
						os.Exit(94)
					}
					switch p.kind {
					case msgAccepted:
						ack = p.id == epoch && p.value == 192
					case msgPlayed:
						var now unix.Timespec
						unix.ClockGettime(unix.CLOCK_MONOTONIC_RAW, &now)
						delta := now.Nano()/1000 - int64(p.id)
						progress = p.value == 48 && delta >= 0 && delta < 1000000
					default:
						os.Exit(95)
					}
				}
				send(packet{kind: msgEnd, epoch: epoch})
			}
			send(packet{kind: msgVolume, value: 87})
		case "write-cancel":
			send(packet{kind: msgPCM, epoch: 1, id: 1, value: 10000, data: make([]byte, 192)})
		case "deadline":
			send(packet{kind: msgPCM, epoch: 1, id: 1, value: 10, data: make([]byte, 192)})
			p := read()
			if p.kind != msgAccepted || p.epoch != 1 || p.id != 1 || p.value != 0 {
				os.Exit(96)
			}
			send(packet{kind: msgEnd, epoch: 1})
			send(packet{kind: msgVolume, value: 87})
		}
		for {
			read()
		}
	}
	os.Exit(m.Run())
}

type testOutput struct {
	mu           sync.Mutex
	starts, ends []uint64
	callback     func(uint32, int64)
	failed       func()
	started      chan struct{}
	writing      chan struct{}
	block        bool
	completed    int
	verified     chan struct{}
}

func (o *testOutput) MusicStart(e uint64, cb func(uint32, int64), failed ...func()) {
	o.mu.Lock()
	old := o.callback
	oldFailed := o.failed
	o.starts = append(o.starts, e)
	o.callback = cb
	o.failed = failed[0]
	o.mu.Unlock()
	if old != nil {
		old(999, 0)
	} // stale progress must be ignored, even when invalid
	if oldFailed != nil {
		oldFailed()
	} // a stale clock-fault closure must not tear down a new epoch
	if o.started != nil {
		select {
		case o.started <- struct{}{}:
		default:
		}
	}
}
func (o *testOutput) MusicEnd(e uint64) { o.mu.Lock(); defer o.mu.Unlock(); o.ends = append(o.ends, e) }
func (o *testOutput) MusicVolume(v uint8, _ bool) {
	o.mu.Lock()
	defer o.mu.Unlock()
	if v == 87 {
		o.completed++
		if o.verified != nil {
			select {
			case o.verified <- struct{}{}:
			default:
			}
		}
	}
}
func (o *testOutput) MusicWrite(ctx context.Context, e uint64, b []byte) error {
	if o.block {
		if o.writing != nil {
			select {
			case o.writing <- struct{}{}:
			default:
			}
		}
		<-ctx.Done()
		return ctx.Err()
	}
	o.mu.Lock()
	cb := o.callback
	o.mu.Unlock()
	var ts unix.Timespec
	if err := unix.ClockGettime(unix.CLOCK_MONOTONIC_RAW, &ts); err != nil {
		return err
	}
	cb(uint32(len(b)/4), ts.Nano()/1000)
	return nil
}
func options(t *testing.T, mode string, o Output) Options {
	t.Helper()
	exe, e := os.Executable()
	if e != nil {
		t.Fatal(e)
	}
	return Options{Name: "test", URL: "ws://localhost:8928/sendspin", StateDir: mode, Executable: exe, Output: o}
}

func TestValidateURL(t *testing.T) {
	for _, s := range []string{"ws://host:8928/sendspin", "ws://[::1]:8928/sendspin"} {
		if e := ValidateURL(s); e != nil {
			t.Fatal(e)
		}
	}
	for _, s := range []string{"ws://host/sendspin", "wss://host:8928/sendspin", "ws://token@host:8928/sendspin", "ws://host:8928/sendspin?token=x", "ws://host:8928/sendspin?", "ws://host:8928/sendspin#x", "ws://host:0/sendspin", "ws://host:99999/sendspin", "ws://host:8928/%73endspin"} {
		if ValidateURL(s) == nil {
			t.Fatalf("accepted invalid URL %q", s)
		}
	}
}
func TestFrameValidation(t *testing.T) {
	p := packet{kind: msgPCM, epoch: 3, id: 7, value: 50, data: []byte{1, 2, 3, 4}}
	b := encode(p)
	q, e := decode(b)
	if e != nil || q.epoch != 3 || len(q.data) != 4 {
		t.Fatal("roundtrip", e)
	}
	for n := 0; n < len(b); n++ {
		if _, e := decode(b[:n]); e == nil {
			t.Fatalf("accepted truncated frame %d", n)
		}
	}
	for _, i := range []int{0, 4, 24} {
		bad := append([]byte(nil), b...)
		bad[i] ^= 255
		if _, e := decode(bad); e == nil {
			t.Fatal("accepted bad header")
		}
	}
}
func TestMalformedAndExitFenceMusic(t *testing.T) {
	for _, mode := range []string{"malformed", "oversize", "exit"} {
		t.Run(mode, func(t *testing.T) {
			o := &testOutput{}
			ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
			defer cancel()
			if err := Run(ctx, options(t, mode, o)); err == nil || errors.Is(err, context.DeadlineExceeded) {
				t.Fatal("did not promptly fail closed", err)
			}
			o.mu.Lock()
			defer o.mu.Unlock()
			if len(o.starts) != 1 || len(o.ends) != 1 || o.starts[0] != o.ends[0] {
				t.Fatalf("unfenced lifecycle: %v %v", o.starts, o.ends)
			}
		})
	}
}
func TestFeedbackAndEpochsAcrossRestarts(t *testing.T) {
	o := &testOutput{}
	for i := 0; i < 2; i++ {
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan error, 1)
		go func() { done <- Run(ctx, options(t, "feedback", o)) }()
		deadline := time.After(3 * time.Second)
		ticker := time.NewTicker(time.Millisecond)
		for {
			o.mu.Lock()
			complete := o.completed == i+1
			o.mu.Unlock()
			if complete {
				break
			}
			select {
			case <-ticker.C:
			case <-deadline:
				cancel()
				ticker.Stop()
				t.Fatal("peer failed to verify feedback")
			}
		}
		ticker.Stop()
		cancel()
		<-done
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.starts) != 4 || len(o.ends) != 4 {
		t.Fatalf("wrong stream count %v %v", o.starts, o.ends)
	}
	for i := range o.starts {
		if o.starts[i] != o.ends[i] || (i > 0 && o.starts[i] <= o.starts[i-1]) {
			t.Fatal("epochs reused or not fenced")
		}
	}
}
func TestCancellationInterruptsWrite(t *testing.T) {
	o := &testOutput{block: true, writing: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Run(ctx, options(t, "write-cancel", o)) }()
	select {
	case <-o.writing:
	case <-time.After(2 * time.Second):
		cancel()
		t.Fatal("no write")
	}
	start := time.Now()
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("cancellation blocked")
	}
	if time.Since(start) > time.Second {
		t.Fatal("slow shutdown")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.ends) != 1 {
		t.Fatal("missing end")
	}
}

func TestOutputDiscontinuityFencesMusicWithoutStoppingHost(t *testing.T) {
	o := &testOutput{block: true, writing: make(chan struct{}, 1)}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, options(t, "write-cancel", o)) }()
	select {
	case <-o.writing:
	case <-ctx.Done():
		t.Fatal("no pending output")
	}
	o.mu.Lock()
	fault := o.failed
	o.mu.Unlock()
	fault()
	select {
	case err := <-done:
		if err == nil || errors.Is(err, context.DeadlineExceeded) {
			t.Fatalf("clock loss did not terminate stream promptly: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("clock fault left child/write running")
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.ends) != 1 || o.ends[0] != o.starts[0] {
		t.Fatal("uncertain music epoch was not fenced")
	}
}

func TestOutputBackpressureReturnsZero(t *testing.T) {
	o := &testOutput{block: true, verified: make(chan struct{}, 1)}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- Run(ctx, options(t, "deadline", o)) }()
	select {
	case <-o.verified:
	case e := <-done:
		t.Fatal("peer failed to verify zero acceptance", e)
	case <-time.After(time.Second):
		t.Fatal("no bounded backpressure response")
	}
	cancel()
	<-done
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.ends) != 1 {
		t.Fatal("lifecycle blocked by PCM timeout")
	}
}

func TestCommandSaturationFailsClosed(t *testing.T) {
	o := &testOutput{}
	cmds := make(chan Command, 1000)
	for i := 0; i < cap(cmds); i++ {
		cmds <- Play
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	opt := options(t, "no-read", o)
	opt.Commands = cmds
	err := Run(ctx, opt)
	if err == nil || errors.Is(err, context.DeadlineExceeded) {
		t.Fatal("saturated IPC did not fail promptly", err)
	}
}
