// Package android adapts the shared voice playback contract to the native host.
package android

import (
	"context"
	"encoding/binary"
	"errors"
	"sync/atomic"
)

// Speaker is implemented by Android AudioTrack. Epoch fencing prevents a write
// already crossing JNI from resuming after a concurrent stop or privacy action.
type Speaker interface {
	WriteVoice(pcm []byte, epoch int64) bool
	DrainVoice(epoch int64) bool
	FlushVoice(epoch int64)
}

type Playback struct {
	speaker Speaker
	epoch   atomic.Int64
}

func NewPlayback(s Speaker) *Playback { return &Playback{speaker: s} }

func (p *Playback) Write(ctx context.Context, samples []int16) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	epoch := p.epoch.Load()
	pcm := make([]byte, len(samples)*2)
	for i, v := range samples {
		binary.LittleEndian.PutUint16(pcm[2*i:], uint16(v))
	}
	if !p.speaker.WriteVoice(pcm, epoch) {
		return errors.New("android: voice write stopped")
	}
	return ctx.Err()
}

func (p *Playback) Drain(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if !p.speaker.DrainVoice(p.epoch.Load()) {
		return errors.New("android: voice drain stopped")
	}
	return ctx.Err()
}

func (p *Playback) Flush() { p.speaker.FlushVoice(p.epoch.Add(1)) }
