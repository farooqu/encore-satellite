package com.encore.satellite

import android.provider.Settings
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Button
import androidx.compose.material3.Text
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.unit.Density
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import com.github.takahirom.roborazzi.ExperimentalRoborazziApi
import com.github.takahirom.roborazzi.RoborazziOptions
import com.github.takahirom.roborazzi.RoborazziTaskType
import com.github.takahirom.roborazzi.captureRoboImage
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import org.robolectric.shadows.ShadowLooper
import java.util.concurrent.TimeUnit
import java.io.File
import javax.imageio.ImageIO
import kotlin.math.sin

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], qualifiers = "w1280dp-h800dp-mdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class EncoreScreenTest {
    @get:Rule val compose = createComposeRule()

    @Test fun homeAssistantPairingAppearsOnlyInInitialSetupAndCanRetry() {
        val state = mutableStateOf(EncoreState(name = "encore-abc", esphome = "unpaired"))
        var taps = 0
        compose.setContent {
            EncoreScreen(state.value, { state.value = state.value.copy(running = true) }, {}, {}, {}, { "" }, { "" }, {}, {}, {}, {},
                onPairHomeAssistant = { taps++; state.value = state.value.copy(esphome = "waiting") })
        }
        val pair = compose.onNodeWithText("Pair with Home Assistant")
        compose.onNodeWithText("Home Assistant setup").assertIsDisplayed()
        pair.assertDoesNotExist()
        compose.onNodeWithContentDescription("Start satellite").performClick()
        pair.assertIsDisplayed()
        capture("android-ha-setup-ready")
        pair.performClick()
        pair.assertDoesNotExist()
        compose.onNodeWithText("add encore-abc under Discovered", substring = true).assertIsDisplayed()
        capture("android-ha-setup-waiting")
        compose.runOnIdle { state.value = state.value.copy(esphome = "expired") }
        compose.onNodeWithText("pairing window expired", substring = true).assertIsDisplayed()
        capture("android-ha-setup-expired")
        pair.assertIsDisplayed().performClick()
        compose.runOnIdle { assertEquals(2, taps); state.value = state.value.copy(esphome = "paired") }
        pair.assertDoesNotExist()
        compose.onNodeWithText("Your home, at a glance.").assertIsDisplayed()
        openControls()
        compose.onNodeWithText("Settings").performClick()
        advance(400)
        pair.assertDoesNotExist()
        compose.onNodeWithText("Reveal ESPHome encryption key").performScrollTo().assertIsDisplayed()
        capture("android-ha-existing-settings")
    }

    @Test fun remoteRequestShowsOverlayWithoutATapAndStopAndMuteDismissIt() {
        val state = mutableStateOf(ready())
        val visibility = mutableListOf<Pair<Long, Boolean>>()
        compose.setContent {
            EncoreScreen(state.value, {}, {}, { state.value = ready() },
                { state.value = ready().copy(muted = it) }, { "" }, { "" }, {}, {}, {}, {},
                reducedMotion = true, dashboardContent = { Box(Modifier.fillMaxSize().background(androidx.compose.ui.graphics.Color.DarkGray)) },
                onMonitorOverlay = { id, visible -> visibility += id to visible })
        }
        compose.onNodeWithTag("voice-overlay").assertDoesNotExist()
        compose.runOnIdle {
            state.value = ready().copy(voiceActive = true, monitorId = 41,
                waveform = List(64) { (0.6 * sin(it * 0.29)).toFloat() })
        }
        compose.onNodeWithTag("voice-visual").assertIsDisplayed().assertContentDescriptionEquals("Live microphone waveform")
        compose.onNodeWithText("Remote microphone active").assertDoesNotExist()
        capture("android-monitor-waveform")
        compose.runOnIdle {
            ShadowLooper.idleMainLooper(32, TimeUnit.MILLISECONDS)
            assertEquals(listOf(41L to true), visibility)
        }
        compose.onNodeWithTag("stop").performClick()
        compose.onNodeWithTag("voice-overlay").assertDoesNotExist()
        compose.runOnIdle { assertTrue((41L to false) in visibility) }

        compose.runOnIdle { state.value = ready().copy(voiceActive = true, monitorId = 42) }
        compose.onNodeWithTag("voice-visual").assertIsDisplayed().assertContentDescriptionEquals("Microphone waveform awaiting samples")
        compose.runOnIdle {
            ShadowLooper.idleMainLooper(32, TimeUnit.MILLISECONDS)
            assertFalse((42L to true) in visibility)
        }
        compose.onNodeWithTag("privacy").performClick()
        compose.onNodeWithTag("voice-overlay").assertDoesNotExist()
        compose.onNodeWithTag("privacy").assertIsOn()
        compose.runOnIdle { assertTrue((42L to false) in visibility) }
    }

    @OptIn(ExperimentalTestApi::class)
    @Test @Config(sdk = [30]) fun voiceBlocksDashboardButNotStopOrPrivacy() {
        var dashboardTaps = 0
        var talks = 0
        var starts = 0
        var stops = 0
        var privacy = false
        val state = mutableStateOf(ready())
        compose.setContent {
            EncoreScreen(state.value, { starts++ }, { talks++ }, { stops++; state.value = state.value.copy(phase = "idle", voiceActive = false, input = "", outputState = "") },
                { privacy = it }, { "" }, { "" }, {}, {}, {}, {}, reducedMotion = true, dashboardContent = {
                    Box(Modifier.fillMaxSize().testTag("dashboard-content").padding(48.dp)) {
                        Button(onClick = { dashboardTaps++ }, modifier = Modifier.testTag("dashboard-tile")) { Text("Dashboard action") }
                    }
                })
        }
        compose.onNodeWithTag("dashboard-tile").performTouchInput { click() }
        assertEquals(1, dashboardTaps)
        val dashboardBounds = compose.onNodeWithTag("dashboard-content").fetchSemanticsNode().boundsInRoot
        assertEquals("Dashboard does not fill the screen", compose.onAllNodes(isRoot()).onLast().fetchSemanticsNode().boundsInRoot,
            dashboardBounds)
        compose.onNodeWithTag("controls").assertDoesNotExist()
        val talkBounds = compose.onNodeWithTag("talk").fetchSemanticsNode().boundsInRoot
        val privacyBounds = compose.onNodeWithTag("privacy").fetchSemanticsNode().boundsInRoot
        assertEquals("Voice controls are not centered", dashboardBounds.center.x,
            (talkBounds.center.x + privacyBounds.center.x) / 2, .5f)
        assertEquals(talkBounds.bottom, privacyBounds.bottom, .5f)
        compose.onNodeWithTag("talk").performTouchInput { click() }
        assertEquals(1, talks)
        for (unavailable in listOf(ready().copy(muted = true), ready().copy(connected = false), ready().copy(captureReady = false), ready().copy(talkAllowed = false))) {
            compose.runOnIdle { state.value = unavailable }
            compose.onNodeWithTag("talk").assertIsNotEnabled()
        }
        compose.runOnIdle { state.value = EncoreState() }
        compose.onNodeWithContentDescription("Start satellite").performTouchInput { click() }
        assertEquals(1, starts)
        compose.runOnIdle { state.value = ready() }
        compose.onNodeWithText("Talk").assertDoesNotExist()
        compose.onNodeWithTag("controls-edge").performTouchInput {
            swipe(start = center, end = center + androidx.compose.ui.geometry.Offset(180f, 0f))
        }
        compose.onNodeWithText("Talk").assertIsDisplayed()
        compose.onNodeWithText("Close controls").performClick()
        compose.runOnIdle { state.value = state.value.copy(phase = "listening", input = "listening", voiceActive = true, heard = "Switch on the lights.") }
        compose.onNodeWithText("Switch on the lights.").assertIsDisplayed()
        assertEquals("Voice resized the dashboard", dashboardBounds,
            compose.onNodeWithTag("dashboard-content").fetchSemanticsNode().boundsInRoot)
        compose.onNodeWithTag("dashboard-shield").assertExists()
        capture("android-frost-fallback")
        compose.onNodeWithTag("dashboard-tile").performTouchInput { click() }
        assertEquals("Dashboard consumed a touch while dimmed", 1, dashboardTaps)
        compose.onNodeWithTag("privacy").performClick()
        assertTrue(privacy)
        // HANGUP does not release the shield while the host still drains.
        compose.runOnIdle { state.value = state.value.copy(phase = "idle", input = "", outputState = "draining") }
        assertTrue("Drain lost shared audio-focus ownership", state.value.voiceActive)
        compose.onNodeWithTag("dashboard-shield").assertExists()
        compose.onNodeWithContentDescription("Stop voice turn").performClick()
        assertEquals(1, stops)
        compose.onNodeWithTag("dashboard-shield").assertDoesNotExist()
        compose.onNodeWithTag("dashboard-tile").performTouchInput { click() }
        assertEquals(2, dashboardTaps)
        compose.runOnIdle { state.value = ready().copy(muted = true) }
        compose.onNodeWithTag("privacy").assertIsOn().assertContentDescriptionEquals("Unmute mic")
        compose.onNodeWithTag("privacy").performTouchInput { click() }
        assertFalse(privacy)
        compose.onNodeWithTag("privacy").performCustomAccessibilityActionWithLabel("Open satellite controls")
        compose.onNodeWithText("Settings").assertIsDisplayed()
        assertFalse("Opening accessible controls changed mute intent", privacy)
    }

    @Test fun renderNativeDisplayStatesAndRespectMusicCapabilities() {
        val state = mutableStateOf(EncoreState())
        var musicCommand = ""
        compose.setContent {
            EncoreScreen(state.value, {}, {}, {}, {}, { "" }, { "" }, {}, {}, { musicCommand = it }, {})
        }
        capture("android-onboarding")
        openControls()
        compose.onNodeWithText("Settings").performClick()
        advance(400)
        compose.onNodeWithText("Trusted HA origin").assertDoesNotExist()
        compose.onNodeWithText("Save trusted origin").assertDoesNotExist()
        compose.onNodeWithText("Set one full Dashboard URL", substring = true).performScrollTo().assertIsDisplayed()
        capture("android-settings")
        compose.onNodeWithText("Done").performClick()
        advance(400)
        compose.mainClock.autoAdvance = false
        compose.runOnIdle { state.value = ready() }
        advance(16)
        capture("android-idle")
        compose.runOnIdle {
            state.value = ready().copy(phase = "listening", input = "listening", voiceActive = true,
                heard = "Test transcript: turn on the kitchen lights", level = .6f,
                waveform = List(64) { (sin(it * .6) * .7 * sin(it * .11)).toFloat() },
                music = MusicState(true, "Connected", "Test audio · sample track", "UI test fixture", true, listOf("pause")))
        }
        advance(400)
        compose.onNodeWithContentDescription("Live microphone waveform").assertExists()
        compose.onNodeWithText("Listening").assertDoesNotExist()
        compose.onNodeWithText("Actual microphone input").assertDoesNotExist()
        openControls()
        advance(400)
        capture("android-music-drawer")
        compose.onNodeWithText("Next").assertIsNotEnabled()
        compose.onNodeWithText("Pause").performClick()
        assertEquals("pause", musicCommand)
        compose.onNodeWithText("Close controls").performClick()
        advance(400)
        capture("android-listening")
        compose.runOnIdle { state.value = state.value.copy(phase = "working", input = "", speakerName = "Fixture speaker", speakerRGB = listOf(255, 0, 0)) }
        advance(400)
        compose.onNodeWithText("Processing").assertDoesNotExist()
        compose.onNodeWithText("Speaker: Fixture speaker · server reported").assertDoesNotExist()
        compose.onNodeWithContentDescription("Processing indicator").assert(SemanticsMatcher.expectValue(
            SemanticsProperties.StateDescription, "Processing · input speaker reported by server: Fixture speaker"))
        capture("android-processing")
        compose.runOnIdle { state.value = state.value.copy(phase = "speaking", outputState = "queued", reply = "Test reply: the kitchen lights are on.") }
        advance(400)
        compose.onNodeWithContentDescription("Waiting for playback indicator").assertExists()
        compose.onNodeWithText("Waiting for response audio").assertDoesNotExist()
        compose.onNodeWithText("Test reply: the kitchen lights are on.").assertIsDisplayed()
        capture("android-queued")
        compose.runOnIdle { state.value = state.value.copy(outputState = "playing", playbackLevel = .7f) }
        advance(900)
        compose.onNodeWithContentDescription("Playback status indicator").assertExists()
        compose.onNodeWithText("Playing response").assertDoesNotExist()
        compose.onNodeWithText("Playback status · not measured audio level").assertDoesNotExist()
        capture("android-output")
        compose.runOnIdle { state.value = state.value.copy(phase = "idle", outputState = "draining") }
        advance(200)
        compose.onNodeWithText("Finishing response").assertDoesNotExist()
        compose.onNodeWithContentDescription("Playback status indicator").assert(SemanticsMatcher.expectValue(
            SemanticsProperties.StateDescription, "Finishing response · input speaker reported by server: Fixture speaker"))
        capture("android-drain")
        compose.runOnIdle { state.value = state.value.copy(alert = true) }
        advance(800)
        compose.onNodeWithContentDescription("Alert status indicator").assertExists()
        compose.onNodeWithText("Alert").assertDoesNotExist()
        capture("android-alert")
        compose.runOnIdle { state.value = state.value.copy(alert = false, outputState = "", playbackLevel = null, phase = "listening", input = "listening",
            invocation = "follow_up", turn = 2, speakerName = "", speakerRGB = emptyList()) }
        advance(400)
        compose.onNodeWithText("Listening for your answer").assertDoesNotExist()
        compose.onNodeWithContentDescription("Live microphone waveform").assert(SemanticsMatcher.expectValue(
            SemanticsProperties.StateDescription, "Listening for your answer"))
        capture("android-follow-up")
        compose.runOnIdle { state.value = state.value.copy(phase = "muted", muted = true, connected = false, models = 0, heard = "", reply = "") }
        advance(16)
        compose.onNodeWithTag("dashboard-shield").assertDoesNotExist()
        openControls()
        advance(400)
        compose.onNodeWithText("Unmute mic").assertIsDisplayed()
        capture("android-muted")
        compose.onNodeWithText("Close controls").performClick()
        advance(400)
        compose.runOnIdle { state.value = ready().copy(connected = false, wakeReady = false, talkReady = false) }
        advance(16)
        openControls()
        advance(400)
        compose.onNodeWithText("Voice disconnected").assertIsDisplayed()
        compose.onNodeWithTag("voice-visual").assertDoesNotExist()
        capture("android-disconnected")
        compose.onNodeWithText("Close controls").performClick()
        advance(400)
        compose.runOnIdle { state.value = ready().copy(voiceActive = true, phase = "working", night = "on") }
        advance(400)
        capture("android-night-processing")
    }

    @Test fun parsesAdditiveFactsWithoutInventingReadinessPlaybackOrIdentity() {
        val base = """{"voice":{"phase":"speaking","connected":true,"muted":false,"alert":false},"models":3,"dashboard":"","reload":0,"problem":""}"""
        val missing = EncoreState.parse(base, ready().copy(speakerName = "Previous", speakerRGB = listOf(1, 2, 3)))
        assertFalse(missing.talkReady); assertFalse(missing.wakeReady); assertFalse(missing.captureReady)
        assertFalse(missing.interactionVisible); assertEquals("none", missing.visual); assertEquals("", missing.speakerName)
        fun parse(fields: String) = EncoreState.parse(base.replace("\"phase\":\"speaking\"", fields), ready())
        val work = parse(""""phase":"working","active":true,"capture_ready":true,"night":"off","speaker":{"name":"Fixture","color_known":true,"rgb":[255,0,0]}""")
        assertEquals(listOf(255, 0, 0), work.speakerRGB)
        assertEquals("processing", work.visual)
        for (rgb in listOf("[0,0,0]", "[256,0,0]", "[0.5,0,0]", "[255,0]", "[\"255\",0,0]")) {
            assertTrue(EncoreState.parse(base.replace("\"phase\":\"speaking\"", """"phase":"working","active":true,"speaker":{"name":"Fixture","color_known":true,"rgb":$rgb}"""), work).speakerRGB.isEmpty())
        }
        val queued = parse(""""phase":"speaking","active":true,"capture_ready":true,"output":{"state":"queued","owner":"voice"}""")
        assertEquals("queued", queued.visual); assertEquals("Waiting for response audio", queued.status)
        val drain = queued.copy(phase = "idle", outputState = "draining")
        assertEquals("output", drain.visual); assertTrue(drain.interactionVisible); assertTrue(drain.voiceActive)
        assertEquals("alert", drain.copy(outputOwner = "alert").visual)
        assertEquals("Finishing alert", drain.copy(outputOwner = "alert").status)
        assertEquals("none", drain.copy(muted = true, alert = true).visual)
        assertEquals("none", work.copy(captureReady = false).visual)
        assertEquals("none", work.copy(inhibited = true).visual)
        assertEquals("none", work.copy(voiceKnown = false).visual)
        assertTrue("Unknown status hid Stop for last-known ownership", work.copy(voiceKnown = false).interactionVisible)
        assertEquals("Microphone muted", work.copy(muted = true).status)
        assertEquals("Microphone unavailable", work.copy(captureReady = false).status)
        val declined = parse(""""phase":"idle","reason":"answered_elsewhere","capture_ready":true""")
        assertEquals("Wake declined", declined.status)
        assertEquals("", EncoreState.parse(base, declined).reason)
    }

    @Test fun audioSettlesThroughSilenceAndSafetyInterruptsWithoutWaiting() {
        compose.mainClock.autoAdvance = false
        val state = mutableStateOf(ready().copy(voiceActive = true, phase = "listening", input = "listening", waveform = List(64) { .9f }))
        val reduced = mutableStateOf(false)
        compose.setContent { VoiceVisualization(state.value, reduced.value, Modifier.width(600.dp).height(94.dp).testTag("voice-visual")) }
        advance(400)
        val inputHeight = traceHeight("transition-input")
        assertTrue("Actual PCM did not displace the braid", inputHeight > 20)
        compose.runOnIdle { state.value = state.value.copy(input = "", phase = "working", waveform = emptyList()) }
        advance(160)
        compose.onNodeWithContentDescription("Processing indicator").assertExists()
        val flat = traceHeight("transition-flat")
        assertTrue("No-audio processing invented waveform displacement ($flat vs $inputHeight)", flat <= 3)
        compose.runOnIdle { state.value = state.value.copy(phase = "speaking", outputState = "playing", playbackLevel = .7f) }
        advance(400)
        assertTrue("Actual output envelope did not displace the braid", traceHeight("transition-output") > 15)
        compose.runOnIdle { reduced.value = true }
        advance(16)
        val still = ImageIO.read(capture("transition-reduced-a", true))
        advance(800)
        val later = ImageIO.read(capture("transition-reduced-b", true))
        assertArrayEquals(still.getRGB(0, 0, still.width, still.height, null, 0, still.width), later.getRGB(0, 0, later.width, later.height, null, 0, later.width))
        compose.runOnIdle { reduced.value = false; state.value = state.value.copy(outputState = "", playbackLevel = null, phase = "working") }
        advance(48)
        compose.runOnIdle { state.value = state.value.copy(inhibited = true, muted = true, alert = true) }
        advance(16)
        assertEquals("Safety left a trace during an interrupted morph", 0, traceHeight("transition-privacy"))
        compose.onNodeWithContentDescription("Microphone waveform inactive").assertExists()
    }

    @Test fun inputAndOutputBraidsAreIndependentAndProfileColorBelongsOnlyToInput() {
        val state = mutableStateOf(ready().copy(voiceActive = true, phase = "speaking", inputOpen = true,
            outputState = "playing", playbackLevel = .65f, waveform = List(64) { .55f }, speakerName = "Fixture",
            speakerRGB = listOf(245, 110, 80)))
        compose.setContent { VoiceVisualization(state.value, true, Modifier.width(600.dp).height(94.dp).testTag("voice-visual")) }
        fun counts(name: String): Pair<Int, Int> {
            val image = ImageIO.read(capture(name, true))
            var input = 0; var output = 0
            for (y in 0 until image.height) for (x in 0 until image.width) {
                if (y in image.height / 2 - 2..image.height / 2 + 2) continue // Exclude flat resting lines.
                val rgb = image.getRGB(x, y)
                val r = (rgb shr 16) and 255; val g = (rgb shr 8) and 255; val b = rgb and 255
                if (r > g * 1.4 && r > b * 1.3) input++
                if (b > g + 15 && r > g + 15) output++
            }
            return input to output
        }
        val both = counts("duplex-both")
        assertTrue("One stream hid or replaced the other", both.first > 50 && both.second > 50)
        compose.runOnIdle { state.value = state.value.copy(waveform = List(64) { 0f }) }
        val outputOnly = counts("duplex-output-only")
        assertTrue("Microphone silence controlled response motion", outputOnly.first == 0 && outputOnly.second > 50)
        compose.runOnIdle { state.value = state.value.copy(waveform = List(64) { .55f }, playbackLevel = 0f) }
        val inputOnly = counts("duplex-input-only")
        assertTrue("Response silence controlled microphone motion", inputOnly.first > 50 && inputOnly.second == 0)
        compose.runOnIdle { state.value = state.value.copy(outputState = "queued", playbackLevel = .9f) }
        assertEquals("Queued audio drew an output waveform", 0, counts("duplex-queued").second)
        compose.runOnIdle { state.value = state.value.copy(inputOpen = false, outputState = "playing", playbackLevel = null) }
        assertEquals("Closed/unknown streams retained waveform displacement", 0 to 0, counts("duplex-unknown"))
        compose.runOnIdle { state.value = state.value.copy(inputOpen = true, playbackLevel = .8f, muted = true) }
        assertEquals("Privacy left an audio layer", 0 to 0, counts("duplex-private"))
    }

    @Test fun microphoneBraidMovesBetweenSnapshotsButNotWithoutInput() {
        compose.mainClock.autoAdvance = false
        val state = mutableStateOf(ready().copy(voiceActive = true, phase = "listening", input = "listening", waveform = List(64) { .8f }))
        val reduced = mutableStateOf(false)
        compose.setContent { VoiceVisualization(state.value, reduced.value, Modifier.width(600.dp).height(94.dp).testTag("voice-visual")) }
        fun pixels(name: String): IntArray = ImageIO.read(capture(name, true)).let {
            it.getRGB(0, 0, it.width, it.height, null, 0, it.width)
        }
        advance(400)
        val first = pixels("braid-frame-a")
        advance(16)
        assertFalse("Braid waited for another 40 ms snapshot to move", first.contentEquals(pixels("braid-frame-b")))
        compose.runOnIdle { reduced.value = true }
        advance(16)
        val held = pixels("braid-reduced-a")
        advance(800)
        assertArrayEquals("Reduced-motion input drifted with a held level", held, pixels("braid-reduced-b"))
        val loud = traceHeight("braid-loud")
        compose.runOnIdle { state.value = state.value.copy(waveform = List(64) { .1f }) }
        advance(16)
        assertTrue("Input strength did not control ribbon displacement", traceHeight("braid-soft") < loud / 2)
        compose.runOnIdle { reduced.value = false; state.value = state.value.copy(waveform = List(64) { .8f }) }
        advance(400)
        compose.runOnIdle { state.value = state.value.copy(waveform = List(64) { 0f }) }
        advance(800)
        assertTrue("Silence retained invented speech displacement", traceHeight("braid-silence") <= 3)
        compose.runOnIdle { state.value = state.value.copy(waveform = List(64) { .8f }) }
        advance(400)
        assertTrue(traceHeight("braid-before-missing") > 20)
        compose.runOnIdle { state.value = state.value.copy(waveform = emptyList()) }
        advance(16)
        assertTrue("Missing samples retained a microphone tail", traceHeight("braid-missing") <= 3)
        compose.onNodeWithContentDescription("Microphone waveform awaiting samples").assertExists()
        compose.runOnIdle { state.value = state.value.copy(waveform = List(64) { .8f }) }
        advance(400)
        compose.runOnIdle { state.value = state.value.copy(turn = 2, waveform = List(64) { 0f }) }
        advance(16)
        assertTrue("New turn inherited the previous input envelope", traceHeight("braid-new-turn") <= 3)
    }

    @Test fun followsAndroidDisabledAnimationsSetting() {
        val resolver = RuntimeEnvironment.getApplication().contentResolver
        Settings.Global.putFloat(resolver, Settings.Global.ANIMATOR_DURATION_SCALE, 0f)
        compose.setContent { Text(if (systemReducedMotion()) "Motion paused" else "Motion enabled") }
        compose.onNodeWithText("Motion paused").assertIsDisplayed()
        Settings.Global.putFloat(resolver, Settings.Global.ANIMATOR_DURATION_SCALE, 1f)
        resolver.notifyChange(Settings.Global.getUriFor(Settings.Global.ANIMATOR_DURATION_SCALE), null)
        compose.waitForIdle()
        compose.onNodeWithText("Motion enabled").assertIsDisplayed()
    }

    @Test @Config(qualifiers = "w360dp-h640dp-mdpi") fun narrowLargeTextKeepsStopAndPrivacyReachable() {
        var stops = 0
        var privacy = false
        compose.setContent {
            CompositionLocalProvider(LocalDensity provides Density(1f, 1.6f)) {
                EncoreScreen(ready().copy(voiceActive = true, phase = "working", heard = "Turn on the kitchen lights.",
                    problem = "Fixture notice: a separate music connection is unavailable."), {}, {}, { stops++ }, { privacy = it }, { "" }, { "" }, {}, {}, {}, {}, reducedMotion = true)
            }
        }
        compose.onNodeWithTag("stop").assertIsDisplayed().assertHasClickAction()
        compose.onNodeWithTag("privacy").assertIsDisplayed().assertHasClickAction()
        compose.onNodeWithTag("stop").performTouchInput { click() }
        compose.onNodeWithTag("privacy").performTouchInput { click() }
        assertEquals(1, stops); assertTrue(privacy)
        capture("android-narrow-large-text")
    }

    @Test @Config(qualifiers = "w800dp-h360dp-mdpi") fun shortLandscapeKeepsControlsOutsideScrollableTranscript() {
        var stops = 0
        var privacy = false
        val state = mutableStateOf(ready().copy(voiceActive = true, phase = "listening", inputOpen = true, waveform = List(64) { .5f }))
        compose.setContent {
            EncoreScreen(state.value, {}, {}, { stops++ }, { privacy = it }, { "" }, { "" }, {}, {}, {}, {}, reducedMotion = true)
        }
        val traceBounds = compose.onNodeWithTag("voice-visual").fetchSemanticsNode().boundsInRoot
        compose.runOnIdle { state.value = state.value.copy(heard = "Turn on the kitchen lights.") }
        assertEquals("A short transcript moved the braid", traceBounds, compose.onNodeWithTag("voice-visual").fetchSemanticsNode().boundsInRoot)
        compose.runOnIdle {
            state.value = state.value.copy(phase = "speaking", outputState = "playing", playbackLevel = .7f,
                heard = "Please turn on the kitchen lights. ".repeat(12), reply = "The kitchen lights are on.")
        }
        assertEquals("A reply moved the braid", traceBounds, compose.onNodeWithTag("voice-visual").fetchSemanticsNode().boundsInRoot)
        compose.onNodeWithText("The kitchen lights are on.").performScrollTo().assertIsDisplayed()
        assertEquals("Scrolling text moved/clipped the trace", traceBounds, compose.onNodeWithTag("voice-visual").fetchSemanticsNode().boundsInRoot)
        val reply = compose.onNodeWithText("The kitchen lights are on.")
        val fullReply = reply.getUnclippedBoundsInRoot()
        assertEquals("Compact layout clipped the reply", fullReply.bottom.value - fullReply.top.value, reply.fetchSemanticsNode().boundsInRoot.height, .5f)
        compose.onNodeWithTag("stop").assertIsDisplayed()
        compose.onNodeWithTag("privacy").assertIsDisplayed()
        compose.onNodeWithTag("stop").performTouchInput { click() }
        compose.onNodeWithTag("privacy").performTouchInput { click() }
        assertEquals(1, stops); assertTrue(privacy)
        capture("android-short-landscape")
    }

    private fun ready() = EncoreState(running = true, microphoneReady = true, voiceKnown = true, connected = true, captureReady = true,
        talkAllowed = true, talkReady = true, wakeReady = true, phase = "idle", models = 2, night = "off", generation = 1, turn = 1)

    private fun openControls() {
        compose.onNodeWithTag("controls-edge").performTouchInput {
            swipe(start = center, end = center + androidx.compose.ui.geometry.Offset(180f, 0f))
        }
        advance(400)
    }

    private fun advance(milliseconds: Long) {
        // Apply pending snapshot invalidations before advancing the paused clock.
        compose.waitForIdle()
        compose.mainClock.advanceTimeBy(milliseconds)
        compose.waitForIdle()
    }

    private fun traceHeight(name: String): Int {
        val image = ImageIO.read(capture(name, true))
        // Measure colored audio traces, not the neutral processing ellipsis.
        val rows = (0 until image.height).filter { y -> (0 until image.width).any { x ->
            val rgb = image.getRGB(x, y)
            val r = (rgb shr 16) and 255; val g = (rgb shr 8) and 255; val b = rgb and 255
            maxOf(r, g, b) > 100 && maxOf(r, g, b) - minOf(r, g, b) > 40
        } }
        return if (rows.isEmpty()) 0 else rows.last() - rows.first() + 1
    }

    @Test @Config(sdk = [34]) fun bluetoothFirstKeepsVoiceUnavailableUntilMicrophoneStarts() {
        val state = mutableStateOf(EncoreState(running = true, connected = true, models = 2, talkAllowed = true))
        var starts = 0
        var talks = 0
        var muteChoice: Boolean? = null
        compose.setContent {
            EncoreScreen(state.value, { starts++ }, { talks++ }, {}, { muteChoice = it },
                { "" }, { "" }, {}, {}, {}, {}, dashboardContent = {
                    Text("Bluetooth-first startup · API 34 test fixture", color = androidx.compose.ui.graphics.Color.White)
                })
        }
        compose.onNodeWithContentDescription("Start microphone").assertIsEnabled()
        compose.onNodeWithTag("privacy").assertIsNotEnabled()
        capture("android-bluetooth-first")
        compose.onNodeWithContentDescription("Start microphone").performClick()
        assertEquals(1, starts)
        assertEquals(0, talks)
        assertNull("Bluetooth-only startup must not change saved mute intent", muteChoice)
        // Requesting permission is not successful capture: keep the action until ready.
        compose.onNodeWithContentDescription("Start microphone").assertIsDisplayed()
        compose.runOnIdle { state.value = state.value.copy(microphoneReady = true) }
        compose.onNodeWithContentDescription("Start microphone").assertDoesNotExist()
        compose.onNodeWithTag("talk").assertIsNotEnabled()
        compose.runOnIdle { state.value = ready() }
        compose.onNodeWithTag("talk").assertIsEnabled().performClick()
        compose.onNodeWithTag("privacy").assertIsEnabled()
        assertEquals(1, talks)
        capture("android-bluetooth-microphone-ready")
        compose.runOnIdle { state.value = state.value.copy(microphoneReady = false, muted = true) }
        compose.onNodeWithTag("talk").assertIsNotEnabled()
        compose.onNodeWithContentDescription("Unmute mic").performClick()
        assertEquals(false, muteChoice)
        capture("android-bluetooth-saved-muted")
    }

    @Test fun announcementOwnsControlsThroughDrainAndHonorsTalkPermission() {
        var stops = 0
        var taps = 0
        val state = mutableStateOf(EncoreState.parse(EncoreStateTest.snapshot(), EncoreState(running = true, microphoneReady = true,
            music = MusicState(enabled = true, status = "Connected", title = "Test audio · continuous stream", playing = true, commands = listOf("pause")))))
        compose.setContent {
            EncoreScreen(state.value, {}, {}, { stops++ }, {}, { "" }, { "" }, {}, {}, {}, {}, dashboardContent = {
                Box(Modifier.fillMaxSize().padding(48.dp)) {
                    Button(onClick = { taps++ }, modifier = Modifier.testTag("dashboard-tile")) { Text("Dashboard action") }
                }
            })
        }
        compose.onNodeWithText("Announcement").assertDoesNotExist()
        compose.onNodeWithTag("dashboard-shield").assertExists()
        compose.onNodeWithContentDescription("Stop voice turn").assert(SemanticsMatcher.expectValue(
            SemanticsProperties.StateDescription, "Playing announcement"))
        compose.onNodeWithTag("dashboard-tile").performTouchInput { click() }
        assertEquals(0, taps)
        openControls()
        compose.onNodeWithText("Talk").assertIsNotEnabled()
        compose.onNodeWithText("Settings").assertIsNotEnabled()
        compose.onNodeWithText("Quiet for voice").assertIsDisplayed()
        compose.onNodeWithText("Close controls").performClick()
        compose.onNodeWithTag("privacy").assertIsEnabled()
        compose.onNodeWithTag("stop").performClick()
        assertEquals(1, stops)
        capture("android-announcement")

        compose.runOnIdle { state.value = EncoreState.parse(EncoreStateTest.snapshot(announcing = false, output = "draining"), state.value) }
        compose.onNodeWithTag("dashboard-shield").assertExists()
        compose.onNodeWithContentDescription("Stop voice turn").assert(SemanticsMatcher.expectValue(
            SemanticsProperties.StateDescription, "Finishing announcement"))
        openControls()
        compose.onNodeWithText("Talk").assertIsNotEnabled()
        capture("android-announcement-draining")
        compose.onNodeWithText("Close controls").performClick()

        compose.runOnIdle { state.value = EncoreState.parse(EncoreStateTest.snapshot(active = false, announcing = false, output = "", owner = ""), state.value) }
        compose.onNodeWithTag("dashboard-shield").assertDoesNotExist()
        compose.onNodeWithTag("talk").assertIsNotEnabled()
        openControls()
        compose.onNodeWithText("Settings").assertIsEnabled()
        compose.onNodeWithText("Close controls").performClick()
        compose.runOnIdle { state.value = EncoreState.parse(EncoreStateTest.snapshot(active = false, announcing = false, output = "", owner = "", talkAllowed = true), state.value) }
        // The canonical permission alone cannot bypass missing fresh capture health.
        compose.onNodeWithTag("talk").assertIsNotEnabled()
        compose.runOnIdle { state.value = state.value.copy(captureReady = true, talkReady = true) }
        compose.onNodeWithTag("talk").assertIsEnabled()
        compose.onNodeWithTag("dashboard-tile").performTouchInput { click() }
        assertEquals(1, taps)
    }

    @Test fun interruptedPlayingMusicCanResumeWithoutServerPlayCapability() {
        val state = mutableStateOf(EncoreState(running = true, microphoneReady = true, connected = true, talkAllowed = true, audioInterrupted = true,
            music = MusicState(enabled = true, status = "Connected", title = "Test audio · continuous stream", playing = true, commands = listOf("pause"))))
        var command = ""
        compose.setContent {
            EncoreScreen(state.value, {}, {}, {}, {}, { "" }, { "" }, {}, {}, { command = it }, {})
        }
        openControls()
        compose.onNodeWithText("Pause").assertIsEnabled()
        compose.onNodeWithText("Resume audio").assertIsEnabled()
        capture("android-focus-lost")
        compose.onNodeWithText("Resume audio").performClick()
        assertEquals("resume", command)
        compose.runOnIdle { state.value = state.value.copy(audioInterrupted = false) }
        compose.onNodeWithText("Resume audio").assertDoesNotExist()
        compose.onNodeWithText("Pause").assertIsDisplayed()
        capture("android-focus-resumed")
    }

    @Test fun dashboardNavigationUsesOnlyTheConfiguredURL() {
        val state = EncoreState(dashboard = "https://ha.example:8123/lovelace/home")
        assertTrue(trustedNavigation(state.origin, "https://ha.example:8123/auth/authorize?client_id=encore"))
        for (url in listOf("https://evil.example/", "http://ha.example:8123/", "https://ha.example/", "file:///data/local", "intent://ha.example", "https://user@ha.example:8123/")) {
            assertFalse(url, trustedNavigation(state.origin, url))
        }
        val moved = state.copy(dashboard = "http://new-ha.example:8124/dashboard-main")
        assertTrue(trustedNavigation(moved.origin, "http://new-ha.example:8124/auth/authorize"))
        assertFalse(trustedNavigation(moved.origin, state.dashboard))
    }

    @OptIn(ExperimentalRoborazziApi::class)
    private fun capture(name: String, visualOnly: Boolean = false): File {
        compose.waitForIdle()
        compose.runOnIdle { ShadowLooper.idleMainLooper(32, TimeUnit.MILLISECONDS) }
        val directory = File("build/reports/screenshots").also { it.mkdirs() }
        // Compose captureToImage's forced redraw deadlocks Robolectric's main looper.
        // Roborazzi captures the real native graphics hierarchy without that device-only path.
        val file = File(directory, "$name.png")
        val node = if (visualOnly) compose.onNodeWithTag("voice-visual") else compose.onAllNodes(isRoot()).onLast()
        node.captureRoboImage(file,
            RoborazziOptions(taskType = RoborazziTaskType.Record))
        return file
    }
}
