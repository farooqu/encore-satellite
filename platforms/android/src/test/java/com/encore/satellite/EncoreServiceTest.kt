package com.encore.satellite

import android.Manifest
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.media.AudioManager
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import com.sendspindroid.sendspin.crypto.TrustStore
import com.sendspindroid.sendspin.protocol.message.MessageBuilder
import org.json.JSONObject
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.shadows.ShadowLooper
import java.net.InetAddress
import java.time.Duration
import javax.crypto.KeyGenerator

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35])
class EncoreServiceTest {
    private val manager = RuntimeEnvironment.getApplication().getSystemService(AudioManager::class.java)
    private val platform = shadowOf(manager)

    @Test fun manualSendspinServerRequiresHostAndPortAndAcceptsAnOptionalPath() {
        assertEquals(MusicEndpoint("music.local:8927", "/sendspin"), MusicEndpoint.manual(" music.local:8927 "))
        assertEquals(MusicEndpoint("[2001:db8::5]:8931", "/music/ws"), MusicEndpoint.manual("[2001:db8::5]:8931/music/ws"))
        assertEquals("192.0.2.5:8931/music/ws", MusicEndpoint.manual("192.0.2.5:8931/music/ws")?.setting)
        for (invalid in listOf("music.local", "music.local:0", "music.local:65536", "ws://music.local:8927",
            "user:secret@music.local:8927", "music.local:8927/sendspin?token=secret", "music.local:8927/sendspin#fragment")) {
            assertNull(invalid, MusicEndpoint.manual(invalid))
        }
    }

    @Suppress("DEPRECATION")
    @Test fun discoveryWaitsForTheWholeBrowseAndOnlyReturnsOneCompatibleEndpoint() {
        val app = RuntimeEnvironment.getApplication()
        val nsd = shadowOf(app.getSystemService(NsdManager::class.java))
        val looper = ShadowLooper.shadowMainLooper()
        val type = "_sendspin-server._tcp."
        val results = mutableListOf<Pair<MusicEndpoint?, String>>()
        val discovery = MusicDiscovery(app) { endpoint, message -> results.add(endpoint to message) }
        fun advertise(name: String, port: Int, path: String) {
            val info = NsdServiceInfo().apply {
                serviceName = name
                serviceType = type
                setHost(InetAddress.getByName("192.0.2.4"))
                setPort(port)
                setAttribute("path", path)
            }
            nsd.getDiscoveryListeners(type).orEmpty().single().onServiceFound(info)
            looper.idle()
            nsd.getResolveListeners(info).orEmpty().single().onServiceResolved(info)
            looper.idle()
        }
        discovery.start()
        advertise("single", 8931, "/music/ws")
        advertise("invalid", 8927, "/sendspin?auth=none")
        assertTrue("Do not dial the first response", results.isEmpty())
        looper.idleFor(Duration.ofSeconds(5))
        assertEquals(MusicEndpoint("192.0.2.4:8931", "/music/ws") to "", results.single())

        discovery.start()
        advertise("one", 8931, "/sendspin")
        advertise("two", 8932, "/sendspin")
        looper.idleFor(Duration.ofSeconds(5))
        assertNull("Ambiguous discovery must not dial either server", results.last().first)
        assertTrue(results.last().second.contains("Multiple"))

        discovery.start()
        advertise("cancelled", 8933, "/sendspin")
        discovery.stop()
        looper.idleFor(Duration.ofSeconds(5))
        assertEquals("Turning off cancels pending discovery", 2, results.size)
    }

    @Test fun sendspinHelloIdentifiesTheEsphomeDeviceWithoutReplacingPairingIdentity() {
        val mac = "02:7a:12:93:df:01"
        val host = MusicHost(RuntimeEnvironment.getApplication(), "Encore Android · DF01", mac,
            "Tab 10.1 Pro", "ACME Inc.", "1.2.3-dev.4", KeyGenerator.getInstance("AES").generateKey())
        val hello = JSONObject(MessageBuilder.buildClientHello(
            deviceName = host.deviceName,
            bufferCapacity = 1_680_000,
            manufacturer = host.manufacturer,
            supportedFormats = listOf(MessageBuilder.FormatEntry("pcm", 48000, 2, 16)),
            softwareVersion = host.softwareVersion,
            productName = host.productName,
            macAddress = host.macAddress,
        )).getJSONObject("payload")
        val device = hello.getJSONObject("device_info")
        assertEquals("Encore Android · DF01", hello.getString("name"))
        assertEquals(mac, device.getString("mac_address"))
        assertEquals("Tab 10.1 Pro", device.getString("product_name"))
        assertEquals("ACME Inc.", device.getString("manufacturer"))
        assertEquals("1.2.3-dev.4", device.getString("software_version"))
        assertNotEquals(mac, host.pairingClientId)
    }

    @Test fun revocationKeepsIdentityAndAnOldConnectionCannotReinstateTrust() {
        val context = RuntimeEnvironment.getApplication()
        val key = KeyGenerator.getInstance("AES").generateKey()
        val mac = "02:7a:12:93:df:01"
        fun host() = MusicHost(context, "Encore Android · DF01", mac, "Tab 10.1 Pro", "ACME Inc.", "1.2.3", key)
        val before = host()
        val token = before.pairingToken()
        val psk = ByteArray(32) { 7 }
        assertTrue(before.trustStore().addRecord(psk, "server-a") is TrustStore.AddRecordResult.Ok)
        val persisted = host()
        assertTrue("Pairing token must survive restart", token == persisted.pairingToken())
        assertEquals("server-a", persisted.trustStore().listRecords().single().serverId)

        persisted.revokeAll()
        assertEquals(TrustStore.AddRecordResult.StorageFailed,
            persisted.trustStore().addRecord(ByteArray(32) { 9 }, "server-b"))
        val after = host()
        assertEquals(before.clientIdentity().clientId, after.clientIdentity().clientId)
        assertTrue("Previously revealed token must stop working", token != after.pairingToken())
        assertTrue(after.trustStore().listRecords().isEmpty())
        assertFalse(after.pairingConfigStore().load().unpairedAccessEnabled)
    }

    @Test fun reopeningEncoreWithoutMicrophonePermissionOnlyStartsAnEnabledPlayer() {
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

        val music = app.getSharedPreferences("sendspin", Context.MODE_PRIVATE)
        music.edit().putBoolean("enabled", true).commit()
        val musicOnly = Robolectric.buildActivity(MainActivity::class.java).setup()
        ShadowLooper.idleMainLooper()
        assertFalse(android.nextStartedService.hasExtra("unmute"))
        assertEquals("A saved player does not need microphone permission", serviceIntent.component, android.nextStartedService.component)
        assertNull(android.nextStartedService)
        musicOnly.pause().stop().destroy()
        music.edit().clear().commit()

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
