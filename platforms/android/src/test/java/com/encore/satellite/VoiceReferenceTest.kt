package com.encore.satellite

import org.junit.Assert.*
import org.junit.Test

class VoiceReferenceTest {
    private val reference = VoiceReference()
    private val block = ByteArray(AEC_BLOCK_BYTES)
    private val origin = 2_000_000_000L
    private fun at(frame: Int) = origin + VOICE_FRAME_NS * frame
    private fun pcm(vararg samples: Int) = ByteArray(samples.size * 2) {
        (samples[it / 2] shr (it % 2 * 8)).toByte()
    }

    @Test fun onlyAcceptedAndPresentedSamplesSurviveCancellation() {
        // Simulate a partial write at a nonzero offset. The head later advances
        // two frames, leaving one enqueued sample and another never accepted.
        reference.accepted(pcm(12345, -1313, 625, 1138, 5555), offset = 2, count = 6)
        reference.observe(0, at(-40), 7, 0, null)
        assertFalse(reference.render(at(-2), block))
        reference.observe(2, at(55), 7, 0, 2L to at(2))
        reference.flush(at(56))
        assertTrue(reference.render(at(-1), block))
        assertArrayEquals(pcm(0, -1313, 625, 0, 0), block.copyOf(10))

        // A replacement starts at frame zero, on a separate presentation clock.
        reference.accepted(pcm(-812, 47), offset = 0, count = 4)
        reference.observe(2, at(82), 7, 0, null)
        assertTrue(reference.render(at(79), block))
        assertArrayEquals(pcm(0, -812, 47, 0), block.copyOf(8))
    }

    @Test fun routeAndUnderrunChangesDoNotReuseAnOldClock() {
        val pcm = pcm(*(IntArray(640) { 1000 + it }))
        reference.accepted(pcm, 0, pcm.size)
        reference.observe(160, at(160), 7, 0, null)
        assertTrue(reference.render(at(0), block))
        assertArrayEquals(pcm.copyOfRange(0, 320), block)
        assertFalse(reference.render(at(330), block)) // Head stalled with queued data.

        reference.observe(210, at(220), 11, 0, 209L to at(219))
        assertEquals(1L, reference.generation)
        assertFalse(reference.render(at(200), block))
        reference.observe(230, at(250), 11, 1, null)
        assertEquals(2L, reference.generation)
        assertFalse(reference.render(at(220), block))

        reference.observe(640, at(2600), 11, 1, null)
        assertTrue(reference.render(at(2440), block))
        assertArrayEquals(pcm.copyOfRange(960, 1280), block)
        reference.observe(640, at(2601), 11, 2, null) // Natural drain, no stale queued data.
        assertTrue(reference.render(at(2440), block))
        assertArrayEquals(pcm.copyOfRange(960, 1280), block)
    }

    @Test fun aRegressedHeadCannotBorrowTheOldTrackClock() {
        val pcm = pcm(*(IntArray(320) { 100 + it }))
        reference.accepted(pcm, 0, pcm.size)
        reference.observe(160, at(160), 7, 0, null)
        reference.observe(10, at(200), 7, 0, null)
        reference.observe(320, at(320), 7, 0, 320L to at(320))
        assertFalse(reference.render(at(2500), block))

        reference.flush(at(2660))
        reference.accepted(pcm, 0, 320)
        reference.observe(160, at(4760), 11, 0, null)
        assertTrue(reference.render(at(4600), block))
        assertArrayEquals(pcm.copyOfRange(0, 320), block)
    }
}
