package com.encore.satellite

import android.Manifest
import android.content.ComponentName
import android.content.Intent
import android.media.AudioManager
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.shadows.ShadowLooper

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class EncoreServiceTest {
    private val manager = RuntimeEnvironment.getApplication().getSystemService(AudioManager::class.java)
    private val platform = shadowOf(manager)

    @Test fun openingEncoreStartsSatelliteOnlyWithMicrophonePermission() {
        val app = RuntimeEnvironment.getApplication()
        val android = shadowOf(app)
        val serviceIntent = Intent(app, EncoreService::class.java)
        val service = Robolectric.buildService(EncoreService::class.java).create()
        android.setComponentNameAndServiceForBindService(ComponentName(app, EncoreService::class.java), service.get().onBind(serviceIntent))

        android.denyPermissions(Manifest.permission.RECORD_AUDIO)
        val beforeGrant = Robolectric.buildActivity(MainActivity::class.java).setup()
        ShadowLooper.idleMainLooper()
        assertFalse("Binding is not a foreground start", android.nextStartedService.hasExtra("unmute"))
        assertNull(android.nextStartedService)
        beforeGrant.pause().stop().destroy()

        android.grantPermissions(Manifest.permission.RECORD_AUDIO)
        val afterGrant = Robolectric.buildActivity(MainActivity::class.java).setup()
        ShadowLooper.idleMainLooper()
        assertFalse(android.nextStartedService.hasExtra("unmute"))
        val foregroundStart = android.nextStartedService
        assertEquals(serviceIntent.component, foregroundStart.component)
        assertFalse("Automatic start must preserve the saved mute choice", foregroundStart.getBooleanExtra("unmute", true))
        assertNull(android.nextStartedService)
        afterGrant.pause().stop().destroy()
        service.destroy()
    }

    @Test fun permanentLossWhileMusicKeepsPlayingRequiresExplicitResume() {
        var interrupted = true
        val focus = SatelliteAudioFocus(manager) { interrupted = !it }
        val music = MusicState(playing = true)
        focus.update(music.playing)
        assertFalse(interrupted)
        val listener = platform.lastAudioFocusRequest.listener
        listener.onAudioFocusChange(AudioManager.AUDIOFOCUS_LOSS)
        assertTrue(interrupted)
        assertNotNull(platform.lastAbandonedAudioFocusRequest)

        repeat(10) { focus.update(music.playing) }
        assertTrue("Polling must not steal focus back", interrupted)
        focus.update(music.playing, resume = true)
        assertTrue("Server playback never had to pause", music.playing)
        assertTrue(focus.focused)
        assertFalse("Local resume must clear the output interruption", interrupted)
    }

    @Test fun transientLossWaitsForGainAndShutdownIgnoresLateGain() {
        var interrupted = true
        val focus = SatelliteAudioFocus(manager) { interrupted = !it }
        focus.update(true)
        val listener = platform.lastAudioFocusRequest.listener
        for (loss in listOf(AudioManager.AUDIOFOCUS_LOSS_TRANSIENT, AudioManager.AUDIOFOCUS_LOSS_TRANSIENT_CAN_DUCK)) {
            listener.onAudioFocusChange(loss)
            assertTrue(interrupted)
            repeat(10) { focus.update(true) }
            assertTrue(interrupted)
            assertNull(platform.lastAbandonedAudioFocusRequest)
            listener.onAudioFocusChange(AudioManager.AUDIOFOCUS_GAIN)
            assertFalse(interrupted)
        }
        focus.update(false)
        listener.onAudioFocusChange(AudioManager.AUDIOFOCUS_GAIN)
        assertFalse(focus.focused)
        assertTrue(interrupted)
    }

    @Test fun deniedRequestCanRetryWithoutChangingDesiredPlayback() {
        var interrupted = false
        val focus = SatelliteAudioFocus(manager) { interrupted = !it }
        platform.setNextFocusRequestResponse(AudioManager.AUDIOFOCUS_REQUEST_FAILED)
        focus.update(true)
        assertTrue(interrupted)
        platform.setNextFocusRequestResponse(AudioManager.AUDIOFOCUS_REQUEST_GRANTED)
        repeat(10) { focus.update(true) }
        assertTrue(interrupted)
        focus.update(true, resume = true)
        assertFalse(interrupted)
        focus.update(false)
        assertFalse(focus.focused)
        assertNotNull(platform.lastAbandonedAudioFocusRequest)
    }

    @Test fun idleAnnouncementRequestsFocusUntilSharedDrainEnds() {
        var interrupted = true
        val focus = SatelliteAudioFocus(manager) { interrupted = !it }
        var state = EncoreState.parse(EncoreStateTest.snapshot(), EncoreState())
        focus.update(state.voiceActive || state.music.playing)
        assertFalse(interrupted)
        state = EncoreState.parse(EncoreStateTest.snapshot(announcing = false, output = "draining"), state)
        focus.update(state.voiceActive || state.music.playing)
        assertTrue(focus.focused)
        state = EncoreState.parse(EncoreStateTest.snapshot(active = false, announcing = false, output = "", owner = ""), state)
        focus.update(state.voiceActive || state.music.playing)
        assertFalse(focus.focused)
    }
}
