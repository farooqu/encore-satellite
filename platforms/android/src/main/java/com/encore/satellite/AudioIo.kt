package com.encore.satellite

import android.Manifest
import android.annotation.SuppressLint
import android.content.Context
import android.content.pm.PackageManager
import android.media.AudioAttributes
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.AudioTrack
import android.media.MediaRecorder
import com.encore.engine.mobile.Audio
import kotlin.math.sqrt

/** One microphone feeds both the local model executor and the voice transport. */
class AudioIo(private val context: Context) : Audio {
    @Volatile private var recording = false
    @Volatile var level = 0f
        private set
    @Volatile var waveform: List<Float> = emptyList()
        private set
    private var recorder: AudioRecord? = null
    private var captureThread: Thread? = null
    private val outputLock = Any()
    private var track: AudioTrack? = null
    private var epoch = 0L
    private var written = 0L

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
            try {
                val block = ByteArray(640)
                while (recording) {
                    val count = input.read(block, 0, block.size, AudioRecord.READ_BLOCKING)
                    if (!recording) break
                    check(count > 0 && count % 2 == 0)
                    var energy = 0.0
                    for (i in 0 until count step 2) {
                        val sample = ((block[i].toInt() and 255) or (block[i + 1].toInt() shl 8)).toShort().toDouble()
                        energy += sample * sample
                    }
                    level = (sqrt(energy / (count / 2)) / 7000).toFloat().coerceIn(0f, 1f)
                    waveform = List(64) { point ->
                        val at = (point * (count / 2) / 64) * 2
                        (((block[at].toInt() and 255) or (block[at + 1].toInt() shl 8)).toShort() / 10000f).coerceIn(-1f, 1f)
                    }
                    if (recording) consume(block.copyOf(count))
                }
            } catch (_: Exception) {
                if (recording) failure()
            } finally {
                level = 0f
                waveform = emptyList()
            }
        }, "encore-microphone").also { it.start() }
    }

    fun stopCapture() {
        recording = false
        level = 0f
        waveform = emptyList()
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
                output.write(pcm, offset, pcm.size - offset, AudioTrack.WRITE_NON_BLOCKING).also {
                    if (it > 0) written += it / 2
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
        track?.let { it.pause(); it.flush(); it.release() }
        track = null
        written = 0
    }

    fun release() {
        stopCapture()
        synchronized(outputLock) { track?.release(); track = null; epoch++ }
    }

    private fun format(channel: Int) = AudioFormat.Builder().setSampleRate(16000)
        .setEncoding(AudioFormat.ENCODING_PCM_16BIT).setChannelMask(channel).build()
}
