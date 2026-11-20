package com.encore.satellite

import android.Manifest
import android.annotation.SuppressLint
import android.content.Context
import android.content.pm.PackageManager
import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioManager
import android.media.AudioRecord
import android.media.AudioTimestamp
import android.media.AudioTrack
import android.media.MediaRecorder
import android.os.Build
import android.os.SystemClock
import com.encore.engine.mobile.Audio
import kotlin.math.sqrt

/** One microphone feeds both the local model executor and the voice transport. */
class AudioIo internal constructor(private val context: Context, voiceAec: Boolean) : Audio {
    constructor(context: Context) : this(context, voiceAec = true)

    private val reference = if (voiceAec) VoiceReference() else null
    private val renderTimestamp = AudioTimestamp()
    @Volatile private var recording = false
    data class MicrophoneFrame(val at: Long = 0, val level: Float = 0f, val waveform: List<Float> = emptyList())
    @Volatile private var microphone = MicrophoneFrame()
    private val visualLock = Any()
    private var visualTurn: Pair<Long, Long>? = null
    private var visualEpoch = 0L
    val captureProblem: String get() = runCatching {
        when {
            context.checkSelfPermission(Manifest.permission.RECORD_AUDIO) != PackageManager.PERMISSION_GRANTED -> "Microphone permission required"
            context.getSystemService(AudioManager::class.java).isMicrophoneMute -> "Microphone blocked by Android"
            !recording || microphone.at == 0L || SystemClock.elapsedRealtimeNanos() - microphone.at !in 0..250_000_000L -> "Microphone unavailable"
            Build.VERSION.SDK_INT >= 29 -> when (recorder?.activeRecordingConfiguration?.isClientSilenced) {
                true -> "Microphone silenced by Android"
                false -> ""
                null -> "Microphone status unavailable"
            }
            else -> ""
        }
    }.getOrDefault("Microphone status unavailable")
    val captureReady: Boolean get() = captureProblem.isEmpty()

    // A new input turn cannot inherit a block whose read started before entry.
    // Clearing visualization never stops capture needed for wake/stop models.
    fun microphoneFrame(generation: Long, turn: Long, visible: Boolean): MicrophoneFrame {
        val ready = captureReady // Platform queries never hold the capture publication lock.
        return synchronized(visualLock) {
            val next = if (visible) generation to turn else null
            if (next != visualTurn) {
                visualTurn = next
                visualEpoch++
                microphone = microphone.copy(level = 0f, waveform = emptyList())
            }
            if (ready) microphone else MicrophoneFrame()
        }
    }

    fun clearVisualization() = synchronized(visualLock) {
        visualTurn = null
        visualEpoch++
        microphone = microphone.copy(level = 0f, waveform = emptyList())
    }
    @Volatile private var recorder: AudioRecord? = null
    private var captureThread: Thread? = null
    private val outputLock = Any()
    private var track: AudioTrack? = null
    private var epoch = 0L
    private var written = 0L
    private data class PlaybackSpan(val end: Long, val level: Float)
    private val playback = ArrayDeque<PlaybackSpan>()

    // Source PCM at the host playback head, not queued-write arrival or acoustic loudness.
    fun playbackLevel(): Float? = synchronized(outputLock) {
        val output = track ?: return@synchronized null
        if (output.playState != AudioTrack.PLAYSTATE_PLAYING) return@synchronized null
        val head = output.playbackHeadPosition.toLong() and 0xffffffffL
        while (playback.isNotEmpty() && playback.first().end <= head) playback.removeFirst()
        playback.firstOrNull()?.level
    }

    @SuppressLint("MissingPermission")
    fun startCapture(consume: (ByteArray) -> Unit, failure: () -> Unit) {
        if (recording) return
        check(context.checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED)
        val input = AudioRecord.Builder()
            // Do not advertise AEC/raw-reference capture on an unqualified tablet.
            .setAudioSource(MediaRecorder.AudioSource.VOICE_RECOGNITION)
            .setAudioFormat(format(AudioFormat.CHANNEL_IN_MONO))
            .setBufferSizeInBytes(maxOf(6400, AudioRecord.getMinBufferSize(16000,
                AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT)))
            .build()
        try {
            check(input.state == AudioRecord.STATE_INITIALIZED)
            input.startRecording()
            check(input.recordingState == AudioRecord.RECORDSTATE_RECORDING)
        } catch (error: Exception) {
            input.release()
            throw error
        }
        recorder = input
        recording = true
        captureThread = Thread({
            android.os.Process.setThreadPriority(android.os.Process.THREAD_PRIORITY_AUDIO)
            var aec: VoiceAec? = null
            try {
                if (reference != null) aec = VoiceAec()
                val block = ByteArray(if (aec != null) AEC_BLOCK_BYTES else 640)
                val render = ByteArray(AEC_BLOCK_BYTES)
                val captureTimestamp = AudioTimestamp()
                var framesRead = 0L
                var lastRead = 0L
                var lastStart = 0L
                var inputRoute: Int? = null
                var generation = -1L
                var reset = false
                while (recording && recorder === input) {
                    val observedEpoch = synchronized(visualLock) { visualEpoch }
                    var count = 0
                    do {
                        val n = input.read(block, count, block.size - count, AudioRecord.READ_BLOCKING)
                        if (!recording || recorder !== input) break
                        check(n > 0 && n % 2 == 0)
                        count += n
                    } while (aec != null && count < block.size)
                    val observedAt = SystemClock.elapsedRealtimeNanos()
                    if (!recording || recorder !== input) break
                    if (aec != null && reference != null) {
                        val now = System.nanoTime()
                        val frameCount = count / 2
                        val route = input.routedDevice?.id
                        // Map the first frame using the recorder's frame clock.
                        // Without a plausible timestamp, anchor reads to their
                        // completion without adding scheduler jitter every block.
                        val readAnchor = now - frameCount * VOICE_FRAME_NS
                        val expected = lastStart + frameCount * VOICE_FRAME_NS
                        val measured = if (input.getTimestamp(captureTimestamp, AudioTimestamp.TIMEBASE_MONOTONIC) == AudioRecord.SUCCESS)
                            captureTimestamp.nanoTime + (framesRead - captureTimestamp.framePosition) * VOICE_FRAME_NS
                        else Long.MIN_VALUE
                        val start = when {
                            measured in (readAnchor - 150_000_000L)..readAnchor -> measured
                            lastRead != 0L && expected in (readAnchor - 20_000_000L)..readAnchor -> expected
                            else -> readAnchor
                        }
                        val captureChanged = lastRead != 0L && (route != inputRoute ||
                            now - lastRead > 80_000_000L || kotlin.math.abs(start - expected) > 20_000_000L)
                        inputRoute = route
                        framesRead += frameCount
                        lastStart = start
                        lastRead = now
                        val (usable, currentGeneration) = synchronized(outputLock) {
                            track?.let { observeReference(it) }
                            reference.render(start, render) to reference.generation
                        }
                        if (!usable || captureChanged) {
                            block.fill(0)
                            reset = true
                        } else {
                            if (reset || generation != currentGeneration) aec.reset()
                            reset = false
                            aec.process(render, block)
                        }
                        generation = currentGeneration
                    }
                    var energy = 0.0
                    for (i in 0 until count step 2) {
                        val sample = ((block[i].toInt() and 255) or (block[i + 1].toInt() shl 8)).toShort().toDouble()
                        energy += sample * sample
                    }
                    val level = (sqrt(energy / (count / 2)) / 7000).toFloat().coerceIn(0f, 1f)
                    val waveform = List(64) { point ->
                        val at = (point * (count / 2) / 64) * 2
                        (((block[at].toInt() and 255) or (block[at + 1].toInt() shl 8)).toShort() / 10000f).coerceIn(-1f, 1f)
                    }
                    if (recording && recorder === input) consume(block.copyOf(count))
                    synchronized(visualLock) {
                        if (recording && recorder === input) microphone = if (observedEpoch == visualEpoch && visualTurn != null)
                            MicrophoneFrame(observedAt, level, waveform)
                        else MicrophoneFrame(observedAt)
                    }
                }
            } catch (_: Exception) {
                if (recording && recorder === input) failure()
            } catch (_: LinkageError) {
                // A missing or unusable native engine is a capture failure, not
                // permission to feed raw audio through the voice path.
                if (recording && recorder === input) failure()
            } finally {
                aec?.close()
                synchronized(visualLock) {
                    if (recorder === input) { microphone = MicrophoneFrame(); visualEpoch++; visualTurn = null }
                }
            }
        }, "encore-microphone").also { it.start() }
    }

    fun stopCapture() {
        recording = false
        synchronized(visualLock) { microphone = MicrophoneFrame(); visualEpoch++; visualTurn = null }
        recorder?.let { runCatching { it.stop() } }
        captureThread?.join(1000)
        captureThread = null
        recorder?.release()
        recorder = null
    }

    override fun writeVoice(pcm: ByteArray, epoch: Long): Boolean {
        var offset = 0
        while (offset < pcm.size) {
            val count = synchronized(outputLock) {
                if (epoch != this.epoch) return false
                val output = track ?: AudioTrack.Builder()
                    .setAudioAttributes(AudioAttributes.Builder().setUsage(AudioAttributes.USAGE_ASSISTANT)
                        .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH).build())
                    .setAudioFormat(format(AudioFormat.CHANNEL_OUT_MONO))
                    .setBufferSizeInBytes(maxOf(6400, AudioTrack.getMinBufferSize(16000,
                        AudioFormat.CHANNEL_OUT_MONO, AudioFormat.ENCODING_PCM_16BIT)))
                    .setTransferMode(AudioTrack.MODE_STREAM).build().also { track = it; it.play() }
                if (reference != null) observeReference(output)
                output.write(pcm, offset, pcm.size - offset, AudioTrack.WRITE_NON_BLOCKING).also {
                    if (it > 0) {
                        reference?.accepted(pcm, offset, it)
                        val head = output.playbackHeadPosition.toLong() and 0xffffffffL
                        while (playback.isNotEmpty() && playback.first().end <= head) playback.removeFirst()
                        // Retain only accepted PCM, in at most 20 ms envelope bins.
                        var start = offset
                        val end = offset + it
                        while (start < end) {
                            val next = minOf(start + 640, end)
                            var energy = 0.0
                            for (i in start until next - 1 step 2) {
                                val sample = ((pcm[i].toInt() and 255) or (pcm[i + 1].toInt() shl 8)).toShort().toDouble()
                                energy += sample * sample
                            }
                            val level = (sqrt(energy / ((next - start) / 2)) / 10000).toFloat().coerceIn(0f, 1f)
                            playback.addLast(PlaybackSpan(written + (next - offset) / 2, level))
                            start = next
                        }
                        written += it / 2
                    } else if (it < 0 && reference != null) {
                        // A dead track cannot supply clocks or consume the queue.
                        releaseTrack()
                    }
                }
            }
            if (count < 0) return false
            offset += count
            if (count == 0) Thread.sleep(5)
        }
        return true
    }

    override fun drainVoice(epoch: Long): Boolean {
        // AudioTrack.write only queues audio. Wait for the playback head instead.
        val deadline = System.nanoTime() + 30_000_000_000L
        while (System.nanoTime() < deadline) {
            synchronized(outputLock) {
                if (epoch != this.epoch) return false
                val output = track ?: return true
                if ((output.playbackHeadPosition.toLong() and 0xffffffffL) >= written) return true
            }
            Thread.sleep(5)
        }
        return false
    }

    override fun flushVoice(epoch: Long) = synchronized(outputLock) {
        this.epoch = epoch
        releaseTrack()
    }

    private fun observeReference(output: AudioTrack, route: Int? = output.routedDevice?.id) {
        val tap = reference ?: return
        val timestamp = if (output.getTimestamp(renderTimestamp)) renderTimestamp.framePosition to renderTimestamp.nanoTime else null
        val head = output.playbackHeadPosition.toLong() and 0xffffffffL
        tap.observe(head, System.nanoTime(), route, output.underrunCount, timestamp)
    }

    private fun releaseTrack() {
        track?.let {
            val route = if (reference != null) it.routedDevice?.id else null
            it.pause()
            if (reference != null) {
                // Pause freezes the head. Flush resets it and discards the queued tail.
                observeReference(it, route)
                reference.flush(System.nanoTime())
            }
            it.flush()
            it.release()
        }
        track = null
        written = 0
        playback.clear()
    }

    fun release() {
        stopCapture()
        synchronized(outputLock) { epoch++; releaseTrack() }
    }

    private fun format(channel: Int) = AudioFormat.Builder().setSampleRate(16000)
        .setEncoding(AudioFormat.ENCODING_PCM_16BIT).setChannelMask(channel).build()
}
