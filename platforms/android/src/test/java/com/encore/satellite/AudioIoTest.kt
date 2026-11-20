package com.encore.satellite

import android.Manifest
import android.media.AudioTrack
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.annotation.Implementation
import org.robolectric.annotation.Implements
import org.robolectric.shadows.ShadowAudioRecord
import org.robolectric.shadows.ShadowAudioTrack
import org.robolectric.shadows.ShadowSystemClock
import java.time.Duration
import java.util.concurrent.CountDownLatch
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicBoolean

@RunWith(RobolectricTestRunner::class)
// API 35's simulated audio service returns a null recording-config list.
// Exercise the PCM/epoch contract on API 28; OS capture silencing needs a device.
@Config(sdk = [28])
class AudioIoTest {
    @Implements(AudioTrack::class)
    class PositionedAudioTrack : ShadowAudioTrack() {
        companion object { var head = 0 }
        @Implementation override fun getPlaybackHeadPosition(): Int = head
    }

    @Test @Config(shadows = [PositionedAudioTrack::class])
    fun responseEnvelopeFollowsPlaybackPositionNotWriteArrivalAndClearsOnFlush() {
        val audio = AudioIo(RuntimeEnvironment.getApplication())
        fun block(sample: Int) = ByteArray(640) { if (it % 2 == 0) sample.toByte() else (sample shr 8).toByte() }
        PositionedAudioTrack.head = 0
        try {
            assertNull(audio.playbackLevel())
            assertTrue(audio.writeVoice(block(8000) + block(0) + block(-2000), 0))
            assertEquals(.8f, audio.playbackLevel()!!, .001f)
            PositionedAudioTrack.head = 319
            assertEquals(.8f, audio.playbackLevel()!!, .001f)
            PositionedAudioTrack.head = 320
            assertEquals("Queued later audio replaced a silent playback span", 0f, audio.playbackLevel()!!, .001f)
            PositionedAudioTrack.head = 640
            assertEquals(.2f, audio.playbackLevel()!!, .001f)
            PositionedAudioTrack.head = 960
            assertNull("Exhausted playback retained an amplitude", audio.playbackLevel())
            audio.flushVoice(1)
            PositionedAudioTrack.head = 0
            assertNull(audio.playbackLevel())
            assertFalse(audio.writeVoice(block(9000), 0))
            assertNull("Stale epoch repopulated the response envelope", audio.playbackLevel())
            assertTrue(audio.writeVoice(block(1000), 1))
            assertEquals(.1f, audio.playbackLevel()!!, .001f)
        } finally { audio.release() }
    }

    @Test fun defaultCaptureFailsClosedWithoutTheAndroidNativeLibrary() {
        val application = RuntimeEnvironment.getApplication()
        shadowOf(application).grantPermissions(Manifest.permission.RECORD_AUDIO)
        val read = AtomicBoolean()
        val failure = CountDownLatch(1)
        ShadowAudioRecord.setSourceProvider {
            object : ShadowAudioRecord.AudioRecordSource {
                override fun readInByteArray(buffer: ByteArray, offset: Int, size: Int, blocking: Boolean): Int {
                    read.set(true)
                    return -1
                }
            }
        }
        val audio = AudioIo(application)
        try {
            audio.startCapture({ fail("Capture delivered PCM without AEC3") }, { failure.countDown() })
            assertTrue("Missing native library did not fail capture", failure.await(3, TimeUnit.SECONDS))
            assertFalse("Default capture attempted a raw read", read.get())
            assertFalse(audio.captureReady)
        } finally {
            audio.release()
            ShadowAudioRecord.clearSource()
        }
    }

    @Test fun microphoneHistoryCannotCrossInputEpochsAndHealthExpires() {
        val application = RuntimeEnvironment.getApplication()
        shadowOf(application).grantPermissions(Manifest.permission.RECORD_AUDIO)
        val blocks = LinkedBlockingQueue<ByteArray>()
        val reads = LinkedBlockingQueue<Unit>()
        val closing = AtomicBoolean()
        val failed = AtomicBoolean()
        val consumed = LinkedBlockingQueue<ByteArray>()
        val source = object : ShadowAudioRecord.AudioRecordSource {
            override fun readInByteArray(buffer: ByteArray, offset: Int, size: Int, blocking: Boolean): Int {
                reads.offer(Unit)
                while (!closing.get()) {
                    val block = blocks.poll(100, TimeUnit.MILLISECONDS) ?: continue
                    block.copyInto(buffer, offset)
                    return block.size
                }
                return -1
            }
        }
        ShadowAudioRecord.setSourceProvider { source }
        fun nextRead() { assertNotNull("Capture did not reach the next read", reads.poll(3, TimeUnit.SECONDS)) }
        fun block(sample: Int) = ByteArray(640) { if (it % 2 == 0) sample.toByte() else (sample shr 8).toByte() }
        // This host test supplies raw fixture PCM without the Android JNI library.
        val audio = AudioIo(application, voiceAec = false)
        ShadowSystemClock.advanceBy(Duration.ofMillis(1))
        try {
            assertFalse(audio.captureReady)
            audio.startCapture({ consumed.offer(it) }, { failed.set(true) })
            nextRead()
            blocks.offer(block(8000)); nextRead()
            assertTrue("Delivered PCM did not establish capture health: ${audio.captureProblem}", audio.captureReady)
            assertTrue(audio.microphoneFrame(1, 1, true).waveform.isEmpty())
            // This read began before entering input. It must not become a new-turn sample.
            blocks.offer(block(8000)); nextRead()
            assertTrue(audio.microphoneFrame(1, 1, true).waveform.isEmpty())
            blocks.offer(block(8000)); nextRead()
            assertEquals(.8f, audio.microphoneFrame(1, 1, true).waveform.first(), .001f)
            audio.clearVisualization()
            assertTrue(audio.captureReady)
            assertTrue(audio.microphoneFrame(1, 2, true).waveform.isEmpty())
            blocks.offer(block(8000)); nextRead()
            assertTrue("An in-flight old read resurfaced after Stop/new input", audio.microphoneFrame(1, 2, true).waveform.isEmpty())
            blocks.offer(block(-2000)); nextRead()
            assertEquals(-.2f, audio.microphoneFrame(1, 2, true).waveform.first(), .001f)
            assertTrue("A replacement connection inherited samples", audio.microphoneFrame(2, 1, true).waveform.isEmpty())
            assertTrue(audio.microphoneFrame(2, 1, false).waveform.isEmpty())
            assertEquals("Clearing display history stopped actual delivery", 5, consumed.size)
            ShadowSystemClock.advanceBy(Duration.ofMillis(251))
            assertFalse("A historical frame claimed a healthy microphone", audio.captureReady)
            assertTrue(audio.microphoneFrame(2, 1, true).waveform.isEmpty())
            shadowOf(application).denyPermissions(Manifest.permission.RECORD_AUDIO)
            assertEquals("Microphone permission required", audio.captureProblem)
            assertFalse(failed.get())
        } finally {
            closing.set(true)
            audio.stopCapture()
            ShadowAudioRecord.clearSource()
        }
    }
}
