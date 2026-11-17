package linux

// TryCue queues at most 50 ms of PCM16/16 kHz mono with its finite filter tail.
// Admission is best-effort and never waits for an operation or mixer lock. It
// does no PCM I/O. Busy voice/music, privacy, Stop and failed outputs reject it.
// A later voice may follow this bounded cue; this is not a preemption API.
func (m *Mixer) TryCue(samples []int16) bool {
	if len(samples) == 0 || len(samples) > 800 {
		return false
	}
	select {
	case m.serial <- struct{}{}:
	default:
		return false
	}
	defer func() { <-m.serial }()
	if !m.mu.TryLock() {
		return false
	}
	defer m.mu.Unlock()
	if m.err != nil || m.active.Load() || m.voicePending != 0 || len(m.voice) != 0 || m.epoch != 0 || len(m.music) != 0 || m.privacy || m.stopped {
		return false
	}
	m.resample.reset()
	m.voice = m.resample.append(m.voice, samples)
	m.voice = m.resample.append(m.voice, make([]int16, 21))
	m.voicePending += (len(samples) + 21) * 3
	m.tail = true
	m.resample.reset()
	return true
}
