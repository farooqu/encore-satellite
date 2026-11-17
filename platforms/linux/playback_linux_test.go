package linux_test

import (
	"context"
	"encoding/binary"
	"errors"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/farooqu/encore-satellite/core/audio"
	"github.com/farooqu/encore-satellite/platforms/linux"
)

type fakePCM struct {
	mu                                 sync.Mutex
	writeBlocked, drainBlocked, closed bool
	drops                              int
	dropErr                            error
	written                            []byte
	called                             chan string
}

func (f *fakePCM) note(s string) {
	select {
	case f.called <- s:
	default:
	}
}
func (f *fakePCM) Write(b []byte) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note("write")
	if f.writeBlocked {
		return 0, syscall.EAGAIN
	}
	n := min(len(b), 2) // force short writes at whole PCM frame boundaries
	f.written = append(f.written, b[:n]...)
	return n, nil
}
func (f *fakePCM) Drain() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.note("drain")
	if f.drainBlocked {
		return syscall.EAGAIN
	}
	return nil
}
func (f *fakePCM) Drop() error  { f.mu.Lock(); defer f.mu.Unlock(); f.drops++; return f.dropErr }
func (f *fakePCM) Close() error { f.mu.Lock(); defer f.mu.Unlock(); f.closed = true; return nil }
func encode(s []int16) []byte {
	b := make([]byte, len(s)*2)
	for i, v := range s {
		binary.LittleEndian.PutUint16(b[i*2:], uint16(v))
	}
	return b
}
func waitCall(t *testing.T, f *fakePCM, kind string) {
	t.Helper()
	for {
		select {
		case s := <-f.called:
			if s == kind {
				return
			}
		case <-time.After(time.Second):
			t.Fatal("no PCM call")
		}
	}
}
func result(t *testing.T, ch <-chan error) error {
	t.Helper()
	select {
	case e := <-ch:
		return e
	case <-time.After(time.Second):
		t.Fatal("operation did not unblock")
		return nil
	}
}

func TestPublicPlaybackDrainWaitsForPhysicalPCM(t *testing.T) {
	f := &fakePCM{drainBlocked: true, called: make(chan string, 32)}
	p := linux.NewPlayer(f, encode, 2)
	defer p.Close()
	var contract audio.Playback = p
	if err := contract.Write(context.Background(), []int16{1234, -2345, 32767, -32768}); err != nil {
		t.Fatal(err)
	}
	f.mu.Lock()
	got := append([]byte(nil), f.written...)
	f.mu.Unlock()
	if string(got) != string(encode([]int16{1234, -2345, 32767, -32768})) {
		t.Fatal("partial write corrupted PCM")
	}
	done := make(chan error, 1)
	go func() { done <- contract.Drain(context.Background()) }()
	waitCall(t, f, "drain")
	select {
	case <-done:
		t.Fatal("claimed drain before physical PCM drained")
	case <-time.After(10 * time.Millisecond):
	}
	f.mu.Lock()
	f.drainBlocked = false
	f.mu.Unlock()
	if err := result(t, done); err != nil {
		t.Fatal(err)
	}
}

func TestPublicPlaybackCancelAndFlush(t *testing.T) {
	for _, operation := range []string{"write", "drain"} {
		for _, stop := range []string{"cancel", "flush"} {
			t.Run(operation+"/"+stop, func(t *testing.T) {
				f := &fakePCM{writeBlocked: true, drainBlocked: true, called: make(chan string, 32)}
				p := linux.NewPlayer(f, encode, 2)
				defer p.Close()
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				done := make(chan error, 1)
				go func() {
					if operation == "write" {
						done <- p.Write(ctx, []int16{100, -200})
					} else {
						done <- p.Drain(ctx)
					}
				}()
				waitCall(t, f, operation)
				if stop == "cancel" {
					cancel()
				} else {
					p.Flush()
				}
				if err := result(t, done); !errors.Is(err, context.Canceled) {
					t.Fatalf("got %v", err)
				}
				f.mu.Lock()
				drops := f.drops
				f.writeBlocked = false
				f.mu.Unlock()
				if drops == 0 {
					t.Fatal("did not discard physical ring")
				}
				cancel()
				if err := p.Write(ctx, []int16{999}); !errors.Is(err, context.Canceled) {
					t.Fatalf("cancelled write: %v", err)
				}
				f.mu.Lock()
				n := len(f.written)
				f.mu.Unlock()
				if n != 0 {
					t.Fatal("cancelled samples played")
				}
				if err := p.Write(context.Background(), []int16{11}); err != nil {
					t.Fatal("new generation failed", err)
				}
			})
		}
	}
}

func TestPublicPlaybackFailedDropClosesDevice(t *testing.T) {
	f := &fakePCM{called: make(chan string, 1), dropErr: syscall.EIO}
	p := linux.NewPlayer(f, encode, 2)
	p.Flush()
	if err := p.Write(context.Background(), []int16{1}); !errors.Is(err, syscall.EIO) {
		t.Fatalf("got %v", err)
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.closed || len(f.written) != 0 {
		t.Fatal("failed DROP left usable device")
	}
}
