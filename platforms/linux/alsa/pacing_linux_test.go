package alsa

import (
	"os"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestPlaybackWaitUsesKernelReadinessAndCoalescedControl(t *testing.T) {
	var pipe [2]int
	if err := unix.Pipe2(pipe[:], unix.O_CLOEXEC|unix.O_NONBLOCK); err != nil {
		t.Fatal(err)
	}
	defer unix.Close(pipe[0])
	writer := os.NewFile(uintptr(pipe[1]), "poll fixture")
	defer writer.Close()
	wakeFD, err := unix.Eventfd(0, unix.EFD_NONBLOCK|unix.EFD_CLOEXEC)
	if err != nil {
		t.Fatal(err)
	}
	defer unix.Close(wakeFD)
	p := &Playback{f: writer, wakeFD: wakeFD}
	var b [4096]byte
	for {
		if _, err := unix.Write(pipe[1], b[:]); err == unix.EAGAIN {
			break
		} else if err != nil {
			t.Fatal(err)
		}
	}
	type result struct {
		hardware bool
		err      error
	}
	wait := func() <-chan result {
		got := make(chan result, 1)
		go func() { hardware, err := p.WaitPlayback(); got <- result{hardware, err} }()
		return got
	}
	check := func(got <-chan result, want bool) {
		t.Helper()
		select {
		case r := <-got:
			if r.err != nil || r.hardware != want {
				t.Fatalf("wake=%+v, want hardware=%v", r, want)
			}
		case <-time.After(time.Second):
			t.Fatal("poll did not wake")
		}
	}
	p.WakePlayback()
	p.WakePlayback()
	check(wait(), false)
	got := wait()
	select {
	case r := <-got:
		t.Fatalf("wait returned with a full PCM fixture and no pending control: %+v", r)
	case <-time.After(15 * time.Millisecond):
	}
	if _, err := unix.Read(pipe[0], b[:]); err != nil {
		t.Fatal(err)
	}
	check(got, true)
	// The writable fixture and queued control are ready together. The control
	// result still takes priority, and the hardware readiness remains visible.
	p.WakePlayback()
	check(wait(), false)
	check(wait(), true)
}
