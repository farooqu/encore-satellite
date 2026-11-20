package com.encore.satellite

// The assistant track, recorder and AEC3 all use 16 kHz mono. The timeline is
// in CLOCK_MONOTONIC, not wall clock or time since the last successful write.
internal const val VOICE_FRAME_NS = 62_500L
internal const val AEC_BLOCK_BYTES = 320

internal class VoiceReference {
    private class Track {
        val pcm = ShortArray(16_000)
        var written = 0L
        var head = 0L
        var floor = 0L
        var anchorFrame = 0L
        var anchorTime: Long? = null
        var route: Int? = null
        var underruns = 0
        var clockLost = false

        fun copyAt(start: Long, block: ByteArray): Boolean {
            if (clockLost) return false
            val time = anchorTime ?: return written == floor
            val first = anchorFrame + Math.floorDiv(start - time, VOICE_FRAME_NS)
            val earliest = maxOf(floor, written - pcm.size)
            // Allow one block of observation jitter, but don't extrapolate a
            // stalled track through an arbitrarily long queued span.
            if (head < written && first >= head + 160) return false
            for (i in 0 until block.size / 2) {
                val frame = first + i
                if (frame in earliest until head) {
                    val sample = pcm[(frame % pcm.size).toInt()].toInt()
                    block[i * 2] = sample.toByte()
                    block[i * 2 + 1] = (sample shr 8).toByte()
                }
            }
            // Crossing the start of a segment is zero-padded. Crossing PCM
            // that belonged to this segment but aged out is not recoverable.
            return earliest == floor || first + block.size / 2 <= floor || first >= earliest
        }
    }

    private var track = Track()
    private var retired: Track? = null
    private var retiredUntil = Long.MIN_VALUE
    private var quietUntil = Long.MIN_VALUE

    var generation = 0L
        private set

    fun accepted(pcm: ByteArray, offset: Int, count: Int) {
        check(count % 2 == 0)
        for (i in offset until offset + count step 2) {
            track.pcm[(track.written % track.pcm.size).toInt()] =
                ((pcm[i].toInt() and 255) or (pcm[i + 1].toInt() shl 8)).toShort()
            track.written++
        }
    }

    fun observe(head: Long, now: Long, route: Int?, underruns: Int, timestamp: Pair<Long, Long>?) {
        val t = track
        val lastHead = t.head
        if (t.clockLost) return
        if (head < lastHead) {
            // A driver reset/wrap cannot be mapped back to queued frames.
            // Do not assume the old queue belongs to the new clock.
            t.clockLost = true
            retired = null
            invalidate(now)
            return
        }
        t.head = minOf(head, t.written)
        val hardware = timestamp?.takeIf { (frame, time) ->
            frame > 0 && frame in t.floor..minOf(t.head + 160, t.written) && now - time in 0..150_000_000L
        }
        val nextFrame = hardware?.first ?: t.head
        val nextTime = hardware?.second ?: now
        val clockJump = t.anchorTime?.let {
            t.head > lastHead && kotlin.math.abs(nextTime - it - (nextFrame - t.anchorFrame) * VOICE_FRAME_NS) > 35_000_000L
        } ?: false
        // An underrun after the whole queue played must not erase its echo tail.
        val underrunWithQueuedPcm = underruns != t.underruns && t.head < t.written
        if (lastHead > 0 && (route != t.route || underrunWithQueuedPcm || clockJump)) {
            // The old mapping must not stretch through a route transition or
            // through silence inserted by an underrun.
            t.floor = t.head
            t.anchorTime = null
            retired = null
            invalidate(now)
        }
        t.route = route
        t.underruns = underruns
        if (t.head > t.floor && (hardware != null || t.head > lastHead)) {
            // No usable hardware timestamp: anchor to the latest advancing
            // playback-head observation. Never advance the anchor on a stall.
            t.anchorFrame = nextFrame
            t.anchorTime = nextTime
        }
    }

    fun flush(now: Long) {
        if (track.written == 0L) return
        if (track.clockLost || track.anchorTime == null) {
            invalidate(now)
        } else if (track.head > track.floor) {
            // Only the played prefix survives flush. AEC3 may still hear it.
            if (retired != null && now < retiredUntil) invalidate(now)
            track.floor = maxOf(track.floor, track.written - track.pcm.size)
            track.written = track.head
            retired = track
            retiredUntil = now + 250_000_000L
        }
        track = Track()
    }

    private fun invalidate(now: Long) {
        quietUntil = now + 120_000_000L
        generation++
    }

    // False means timing is not usable. The caller must never substitute raw
    // microphone data for an uncleanable block during playback.
    fun render(start: Long, block: ByteArray): Boolean {
        block.fill(0)
        if (start >= retiredUntil) retired = null
        if (start < quietUntil) return false
        return (retired?.copyAt(start, block) != false) && track.copyAt(start, block)
    }
}

// Owned exclusively by the capture thread (including destruction).
internal class VoiceAec : AutoCloseable {
    private val handle: Long

    init {
        System.loadLibrary("encore_aec")
        handle = create()
        check(handle != 0L) { "AEC3 could not start" }
    }

    fun reset() = check(reset(handle)) { "AEC3 could not reset" }
    fun process(render: ByteArray, capture: ByteArray) =
        check(process(handle, render, capture)) { "AEC3 processing failed" }

    override fun close() = destroy(handle)

    private external fun create(): Long
    private external fun reset(handle: Long): Boolean
    private external fun process(handle: Long, render: ByteArray, capture: ByteArray): Boolean
    private external fun destroy(handle: Long)
}
