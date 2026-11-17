package android_test

import (
	"bytes"
	"context"
	"errors"
	"testing"

	"github.com/farooqu/encore-satellite/platforms/android"
)

// admissionContext preserves the cancellation result already sampled by Err
// while allowing cancel+Flush to complete before that result reaches playback.
type admissionContext struct {
	context.Context
	afterCheck func()
}

func (c *admissionContext) Err() error {
	err := c.Context.Err()
	if f := c.afterCheck; f != nil {
		c.afterCheck = nil
		f()
	}
	return err
}

// fencedSpeaker models the native equality fence, including a stop after
// entering the speaker call but before native admission checks the epoch.
type fencedSpeaker struct {
	epoch       int64
	beforeFence func()
	pcm         []byte
	drains      int
}

func (s *fencedSpeaker) admit(epoch int64) bool {
	if f := s.beforeFence; f != nil {
		s.beforeFence = nil
		f()
	}
	return epoch == s.epoch
}

func (s *fencedSpeaker) WriteVoice(pcm []byte, epoch int64) bool {
	if !s.admit(epoch) {
		return false
	}
	s.pcm = append(s.pcm, pcm...)
	return true
}

func (s *fencedSpeaker) DrainVoice(epoch int64) bool {
	if !s.admit(epoch) {
		return false
	}
	s.drains++
	return true
}

func (s *fencedSpeaker) FlushVoice(epoch int64) { s.epoch = epoch }

func TestPlaybackCancelAndFlushAdmission(t *testing.T) {
	for _, operation := range []string{"write", "drain"} {
		for _, stopAt := range []string{"before call", "cancellation admission", "native admission"} {
			t.Run(operation+"/"+stopAt, func(t *testing.T) {
				s := &fencedSpeaker{}
				p := android.NewPlayback(s)
				parent, cancel := context.WithCancel(context.Background())
				defer cancel()
				ctx := &admissionContext{Context: parent}
				stop := func() {
					cancel()
					p.Flush()
				}
				switch stopAt {
				case "before call":
					stop()
				case "cancellation admission":
					ctx.afterCheck = stop
				case "native admission":
					// Flush must be able to finish during a speaker call;
					// playback cannot hold a lock across JNI.
					s.beforeFence = stop
				}
				var err error
				if operation == "write" {
					err = p.Write(ctx, []int16{17, -83})
				} else {
					err = p.Drain(ctx)
				}
				if err == nil {
					t.Fatal("cancelled operation succeeded")
				}
				if stopAt == "before call" && !errors.Is(err, context.Canceled) {
					t.Fatalf("already cancelled: got %v", err)
				}
				if len(s.pcm) != 0 || s.drains != 0 {
					t.Fatalf("stale operation admitted after cancel+Flush: pcm=%v drains=%d, err=%v", s.pcm, s.drains, err)
				}

				// A fresh operation must use the new epoch, not remain
				// permanently fenced out after the stop.
				if err := p.Write(context.Background(), []int16{1234, -2345}); err != nil {
					t.Fatalf("fresh write: %v", err)
				}
				if err := p.Drain(context.Background()); err != nil {
					t.Fatalf("fresh drain: %v", err)
				}
				if !bytes.Equal(s.pcm, []byte{0xd2, 0x04, 0xd7, 0xf6}) || s.drains != 1 {
					t.Fatalf("fresh playback: pcm=%v drains=%d", s.pcm, s.drains)
				}
			})
		}
	}
}
