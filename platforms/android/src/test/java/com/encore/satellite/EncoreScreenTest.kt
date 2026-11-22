package com.encore.satellite

import android.graphics.BitmapFactory
import android.provider.Settings
import android.util.Base64
import androidx.activity.OnBackPressedDispatcher
import androidx.activity.compose.LocalOnBackPressedDispatcherOwner
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.material3.darkColorScheme
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
import androidx.compose.ui.unit.sp
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

    @Test fun dashboardEnrollmentNeedsLocalApprovalAndCanStartOverAfterLoss() {
        val dialog = mutableStateOf(false)
        val account = mutableStateOf<String?>(null)
        val error = mutableStateOf("")
        // Inert scan target for screenshots: https://home.example/auth/authorize
        val sample = Base64.decode("iVBORw0KGgoAAAANSUhEUgAAAEoAAABKAQMAAAAmHlAyAAAABlBMVEUAAAD///+l2Z/dAAAAAnRSTlP//8i138cAAAAJcEhZcwAACxIAAAsSAdLdfvwAAADKSURBVCiRtdKxikQxCAVQwTbgrwi2AX89kDbgrwTSCk6YnX0z8U25a3Wqi1yFuAb+loBr+QQ9qcusSSSyVA/BG43m+sZ6o67J8Jt7EVBEGBIjqpO+9r2IoFpwJfYopM30pM0pMjgRHHGgx0ntc3YcenJEW4qW6EJmUBKNijctcXK0qOUV9qZTI5KeWPtesnpit47NnmEfZPFlu6eTEbyLZj25SzWXkqiLA5USWbDSLjZzEDrfWOV5mIO6CEb5KfVNwDoLup78n69+AAAM3pCnFEKDAAAAAElFTkSuQmCC", Base64.DEFAULT)
        val qr = BitmapFactory.decodeByteArray(sample, 0, sample.size)
        var starts = 0
        var approvals = 0
        var fallbacks = 0
        compose.setContent {
            MaterialTheme(colorScheme = darkColorScheme(primary = androidx.compose.ui.graphics.Color(0xff97e6d4),
                background = androidx.compose.ui.graphics.Color(0xff0c141d), surface = androidx.compose.ui.graphics.Color(0xff152330))) {
                DashboardSignInNeeded(onPhone = { dialog.value = true; starts++ }, onTablet = { fallbacks++ })
                if (dialog.value) DashboardEnrollmentDialog("Kitchen satellite", "https://home.example", qr, account.value,
                    error.value, onCancel = { dialog.value = false }, onAccept = { approvals++; dialog.value = false },
                    onRetry = { starts++; error.value = "" })
            }
        }
        compose.onNodeWithText("Dashboard sign-in needed").assertIsDisplayed()
        compose.onNodeWithText("Sign in on tablet").assertIsDisplayed().performClick()
        capture("android-dashboard-auth-recovery")
        compose.onNodeWithText("Sign in with phone").performClick()
        compose.onNodeWithText("private browser tab", substring = true).assertIsDisplayed()
        compose.onNodeWithContentDescription("Home Assistant phone sign-in QR code").assertIsDisplayed()
        compose.onNodeWithText("Use this account").assertDoesNotExist()
        capture("android-dashboard-auth-qr")
        compose.runOnIdle { error.value = "The QR code expired." }
        compose.onNodeWithText("Try again").performClick()
        compose.onNodeWithText("Use this account").assertDoesNotExist()
        compose.runOnIdle { account.value = "Satellite lab"; assertEquals(0, approvals) }
        compose.onNodeWithText("signed in as Satellite lab", substring = true).assertIsDisplayed()
        capture("android-dashboard-auth-confirmation")
        compose.onNodeWithText("Use this account").performClick()
        compose.runOnIdle { assertEquals(2, starts); assertEquals(1, approvals); assertEquals(1, fallbacks) }
    }

    @Test fun homeAssistantPairingAppearsOnlyInInitialSetupAndCanRetry() {
        val state = mutableStateOf(EncoreState())
        val microphonePermission = mutableStateOf(false)
        var requests = 0
        var taps = 0
        compose.setContent {
            EncoreScreen(state.value, { requests++ }, {}, {}, {}, { "" }, { "" }, {}, {}, {}, {},
                microphonePermissionGranted = microphonePermission.value,
                onPairHomeAssistant = { taps++; state.value = state.value.copy(esphome = "waiting") })
        }
        val allow = compose.onNodeWithText("Allow microphone access")
        val pair = compose.onNodeWithText("Pair with Home Assistant")
        compose.onNodeWithText("Set up Encore").assertIsDisplayed()
        pair.assertDoesNotExist()
        allow.assertIsDisplayed().assertIsEnabled()
        capture("android-setup-permission")
        allow.performTouchInput { click() }
        compose.runOnIdle { assertEquals(1, requests) }
        allow.assertIsDisplayed()
        pair.assertDoesNotExist()
        compose.runOnIdle {
            state.value = state.value.copy(name = "encore-android-350ab2", espHomeFriendlyName = "Encore Android · 0AB2", esphome = "unpaired")
            microphonePermission.value = true
        }
        allow.assertDoesNotExist()
        compose.onNodeWithText("Starting Encore…").assertIsDisplayed()
        pair.assertDoesNotExist()
        capture("android-setup-starting")
        compose.runOnIdle { state.value = state.value.copy(running = true) }
        compose.onNodeWithText("Home Assistant setup").assertIsDisplayed()
        pair.assertIsDisplayed()
        capture("android-ha-setup-ready")
        pair.performClick()
        pair.assertDoesNotExist()
        compose.onNodeWithText("add Encore Android · 0AB2 under Discovered", substring = true).assertIsDisplayed()
        capture("android-ha-setup-waiting")
        compose.runOnIdle { state.value = state.value.copy(esphome = "expired") }
        compose.onNodeWithText("pairing window expired", substring = true).assertIsDisplayed()
        capture("android-ha-setup-expired")
        pair.assertIsDisplayed().performClick()
        compose.runOnIdle { assertEquals(2, taps); state.value = state.value.copy(esphome = "paired") }
        pair.assertDoesNotExist()
        compose.onNodeWithText("Your home, at a glance.").assertIsDisplayed()
        compose.runOnIdle { state.value = state.value.copy(running = false) }
        allow.assertDoesNotExist()
        compose.onNodeWithText("Your home, at a glance.").assertIsDisplayed()
        compose.onNodeWithContentDescription("Start satellite").assertDoesNotExist()
        openSettings()
        pair.assertDoesNotExist()
        compose.onNodeWithText("Reveal ESPHome encryption key").performScrollTo().assertIsDisplayed()
        capture("android-ha-existing-settings")
    }

    @Test fun remoteRequestWaitsForDrawnIndicatorAndFreshSamples() {
        compose.mainClock.autoAdvance = false
        val state = mutableStateOf(ready())
        val visibility = mutableListOf<Pair<Long, Boolean>>()
        compose.setContent {
            EncoreScreen(state.value, {}, {}, { state.value = ready() },
                { state.value = ready().copy(muted = it) }, { "" }, { "" }, {}, {}, {}, {},
                reducedMotion = false, dashboardContent = { Box(Modifier.fillMaxSize().background(androidx.compose.ui.graphics.Color.DarkGray)) },
                onMonitorOverlay = { id, visible -> visibility += id to visible })
        }
        compose.onNodeWithTag("voice-edge-glow").assertDoesNotExist()
        openSettings()
        compose.onNodeWithTag("settings-screen").assertIsDisplayed()
        compose.runOnIdle {
            state.value = ready().copy(voiceActive = true, monitorId = 41)
        }
        advance(96)
        compose.onNodeWithTag("settings-screen").assertDoesNotExist()
        assertVoiceStatusIsAccessibleOnly("Remote mic starting")
        compose.runOnIdle {
            ShadowLooper.idleMainLooper(32, TimeUnit.MILLISECONDS)
            assertTrue("Recording was acknowledged without a fresh block", visibility.isEmpty())
            state.value = state.value.copy(level = .18f, waveform = List(64) { .12f })
            assertTrue("Recording was acknowledged before drawing", visibility.isEmpty())
        }
        advance(320)
        compose.onNodeWithTag("voice-edge-glow").assertIsDisplayed()
        compose.onNodeWithTag("voice-transcript").assertDoesNotExist()
        assertVoiceStatusIsAccessibleOnly("Remote mic on")
        compose.onNodeWithTag("open-settings").assertIsNotEnabled()
        capture("android-monitor-edge-glow")
        compose.runOnIdle {
            ShadowLooper.idleMainLooper(32, TimeUnit.MILLISECONDS)
            assertEquals(listOf(41L to true), visibility)
        }
        compose.onNodeWithTag("stop").performClick()
        advance(32)
        compose.runOnIdle { assertTrue((41L to false) in visibility) }
        advance(300)
        compose.onNodeWithTag("voice-edge-glow").assertDoesNotExist()
        compose.onNodeWithTag("settings-screen").assertDoesNotExist()

        compose.runOnIdle { state.value = ready().copy(voiceActive = true, monitorId = 42) }
        advance(400)
        assertVoiceStatusIsAccessibleOnly("Remote mic starting")
        compose.runOnIdle {
            ShadowLooper.idleMainLooper(32, TimeUnit.MILLISECONDS)
            assertFalse((42L to true) in visibility)
        }
        compose.onNodeWithTag("privacy").performClick()
        advance(400)
        compose.onNodeWithTag("voice-edge-glow").assertDoesNotExist()
        compose.onNodeWithTag("privacy").assertIsOn()
        compose.runOnIdle { assertTrue((42L to false) in visibility) }
    }

    @OptIn(ExperimentalTestApi::class)
    @Test @Config(sdk = [30]) fun voiceLeavesDashboardAndDockUsable() {
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
        val talk = compose.onNodeWithTag("talk").assertContentDescriptionEquals("Trigger assistant")
        val mic = compose.onNodeWithTag("privacy").assertContentDescriptionEquals("Mute mic")
        val settings = compose.onNodeWithContentDescription("Settings").assertIsDisplayed()
        val talkBounds = talk.fetchSemanticsNode().boundsInRoot
        val privacyBounds = mic.fetchSemanticsNode().boundsInRoot
        val settingsBounds = settings.fetchSemanticsNode().boundsInRoot
        assertEquals("Right rail is not vertically centered", dashboardBounds.center.y,
            (talkBounds.center.y + settingsBounds.center.y) / 2, .5f)
        assertEquals(talkBounds.right, privacyBounds.right, .5f)
        assertEquals(talkBounds.right, settingsBounds.right, .5f)
        assertTrue(dashboardBounds.right - talkBounds.right < 56f)
        compose.onNodeWithTag("talk").performTouchInput { click() }
        assertEquals(1, talks)
        for (unavailable in listOf(ready().copy(muted = true), ready().copy(connected = false), ready().copy(captureReady = false), ready().copy(talkAllowed = false))) {
            compose.runOnIdle { state.value = unavailable }
            compose.onNodeWithTag("talk").assertIsNotEnabled()
        }
        compose.runOnIdle { state.value = ready().copy(running = false) }
        talk.assertIsNotEnabled().performTouchInput { click() }
        compose.onNodeWithContentDescription("Start satellite").assertDoesNotExist()
        assertEquals(0, starts)
        assertEquals(1, talks)
        compose.runOnIdle { state.value = ready() }
        compose.onNodeWithText("Talk").assertDoesNotExist()
        settings.performTouchInput { click() }
        advance(400)
        compose.onNodeWithText("Satellite settings").assertIsDisplayed()
        compose.onNodeWithText("Bluetooth proxy setup").assertIsDisplayed()
        compose.onNodeWithText("Talk").assertDoesNotExist()
        compose.onNodeWithText("Mute mic").assertDoesNotExist()
        assertEquals(1, talks)
        assertFalse(privacy)
        compose.onNodeWithText("Done").performClick()
        compose.runOnIdle { state.value = state.value.copy(phase = "listening", input = "listening", voiceActive = true,
            invocation = "manual", heard = "Switch on the lights.") }
        compose.onNodeWithText("Switch on the lights.").assertIsDisplayed()
        assertEquals("Voice resized the dashboard", dashboardBounds,
            compose.onNodeWithTag("dashboard-content").fetchSemanticsNode().boundsInRoot)
        compose.onNodeWithTag("voice-transcript").assertIsDisplayed()
        compose.onNodeWithTag("voice-edge-glow").assertDoesNotExist()
        compose.onNodeWithTag("dashboard-tile").performTouchInput { click() }
        assertEquals("Voice blocked a dashboard touch", 2, dashboardTaps)
        compose.onNodeWithTag("privacy").assertDoesNotExist()
        // HANGUP does not remove Stop while the host still drains.
        compose.runOnIdle { state.value = state.value.copy(phase = "idle", input = "", outputState = "draining") }
        assertTrue("Drain lost shared audio-focus ownership", state.value.voiceActive)
        assertVoiceStatusIsAccessibleOnly("Finishing response")
        compose.onNodeWithContentDescription("Stop voice turn").performTouchInput { click() }
        assertEquals(1, stops)
        assertFalse("Closing the panel changed mute", privacy)
        compose.onNodeWithTag("voice-transcript").assertDoesNotExist()
        compose.onNodeWithTag("privacy").assertContentDescriptionEquals("Mute mic").performTouchInput { click() }
        assertTrue(privacy)
        compose.onNodeWithTag("dashboard-tile").performTouchInput { click() }
        assertEquals(3, dashboardTaps)
        compose.runOnIdle { state.value = ready().copy(muted = true) }
        compose.onNodeWithTag("privacy").assertIsOn().assertContentDescriptionEquals("Unmute mic")
        compose.onNodeWithTag("privacy").performTouchInput { click() }
        assertFalse(privacy)
        openSettings()
        compose.onNodeWithText("Satellite settings").assertIsDisplayed()
        assertFalse("Opening settings changed mute intent", privacy)
    }

    @Test fun localWakeShowsOnlyAnAcceptedCallAndKeepsItsFollowUpsThroughDrain() {
        compose.mainClock.autoAdvance = false
        val state = mutableStateOf(ready())
        var stops = 0
        compose.setContent {
            EncoreScreen(state.value, {}, {}, { stops++; state.value = ready() }, {}, { "" }, { "" }, {}, {}, {}, {},
                dashboardContent = {
                    Column(Modifier.fillMaxSize().testTag("dashboard-content").padding(32.dp)) {
                        Text("Home", fontSize = 28.sp, color = androidx.compose.ui.graphics.Color.White)
                        Text("Living room · 21.5 °C", color = androidx.compose.ui.graphics.Color(0xffa0afbd))
                    }
                })
        }
        val panel = compose.onNodeWithTag("voice-transcript")
        val cue = compose.onNodeWithTag("voice-wake-cue")
        val oldIndicator = compose.onNodeWithTag("voice-edge-glow")
        val tentative = ready().copy(voiceActive = true, invocation = "wake", phase = "tentative", input = "tentative")
        compose.runOnIdle { state.value = tentative }
        advance(250)
        cue.assertDoesNotExist()
        panel.assertDoesNotExist()
        oldIndicator.assertDoesNotExist()
        compose.runOnIdle { state.value = ready().copy(reason = "answered_elsewhere") }
        advance(450)
        cue.assertDoesNotExist()
        panel.assertDoesNotExist()
        compose.runOnIdle { state.value = tentative.copy(turn = 2) }
        advance(440)
        cue.assertIsDisplayed().assertHasNoClickAction()
        capture("tablet-wake-cue")
        compose.runOnIdle { state.value = ready().copy(turn = 2, reason = "wake_rejected") }
        advance(64)
        cue.assertDoesNotExist()
        panel.assertDoesNotExist()
        oldIndicator.assertDoesNotExist()

        compose.runOnIdle { state.value = tentative.copy(turn = 3, accepted = true, phase = "listening", input = "listening") }
        advance(64)
        val compact = panel.assertIsDisplayed().fetchSemanticsNode().boundsInRoot
        compose.onNodeWithText("Listening…").assertIsDisplayed()
        compose.onNodeWithContentDescription("Encore Satellite").assertIsDisplayed()
        oldIndicator.assertDoesNotExist()
        capture("tablet-local-listening")
        val question = "What’s on my calendar?"
        val reply = "You have dinner with Sam at seven. Want me to set a reminder?"
        compose.runOnIdle { state.value = state.value.copy(phase = "working", input = "", heard = question) }
        advance(64)
        compose.onNodeWithText("Listening…").assertDoesNotExist()
        compose.onNodeWithContentDescription("You: $question").assertIsDisplayed()
        compose.runOnIdle { state.value = state.value.copy(phase = "speaking", outputState = "queued", outputOwner = "voice", reply = reply) }
        advance(64)
        compose.onNodeWithContentDescription("Encore: $reply").assertIsDisplayed()
        assertTrue(panel.fetchSemanticsNode().boundsInRoot.height > compact.height)
        compose.runOnIdle { state.value = state.value.copy(turn = 4, invocation = "follow_up", phase = "listening",
            input = "listening", accepted = false, outputState = "", outputOwner = "", heard = "", reply = "") }
        advance(64)
        compose.onNodeWithText(question).assertIsDisplayed()
        compose.onNodeWithText(reply).assertIsDisplayed()
        compose.onNodeWithText("Listening…").assertIsDisplayed()
        capture("tablet-local-follow-up")

        val longReply = "Reminder set. " + "This is a longer reply that should scroll inside the voice panel. ".repeat(32)
        compose.runOnIdle { state.value = state.value.copy(phase = "idle", input = "", outputState = "draining", outputOwner = "voice",
            heard = "Yes, please.", reply = longReply, reason = "done") }
        advance(64)
        val height = panel.fetchSemanticsNode().boundsInRoot.height
        val screen = compose.onNodeWithTag("dashboard-content").fetchSemanticsNode().boundsInRoot
        assertEquals(screen.height / 2, height, 1f)
        assertVoiceStatusIsAccessibleOnly("Finishing response")
        compose.onNodeWithText(question).assertIsNotDisplayed()
        val scroll = compose.onNode(hasScrollAction() and hasAnyAncestor(hasTestTag("voice-transcript")))
        repeat(3) {
            scroll.performTouchInput { swipeDown() }
            advance(500)
        }
        compose.onNodeWithText(question).assertIsDisplayed()
        compose.onNodeWithTag("stop").assertIsDisplayed()
        capture("tablet-local-capped-transcript")
        compose.runOnIdle { state.value = state.value.copy(voiceActive = false, invocation = "", outputState = "", outputOwner = "") }
        advance(5600)
        panel.assertIsDisplayed()
        compose.onNodeWithTag("close-voice-transcript").assertIsDisplayed()
        compose.runOnIdle { state.value = tentative.copy(turn = 5) }
        advance(64)
        panel.assertIsDisplayed()
        compose.runOnIdle { state.value = ready().copy(turn = 5, reason = "answered_elsewhere") }
        advance(64)
        panel.assertIsDisplayed()
        advance(550)
        panel.assertDoesNotExist()
        assertEquals(0, stops)
    }

    @Test fun renderNativeDisplayStatesAndRespectMusicCapabilities() {
        val state = mutableStateOf(EncoreState())
        var musicCommand = ""
        compose.setContent {
            EncoreScreen(state.value, {}, {}, {}, {}, { "" }, { "" }, {}, {}, { musicCommand = it }, {})
        }
        capture("android-onboarding")
        openSettings()
        compose.onNodeWithText("Trusted HA origin").assertDoesNotExist()
        compose.onNodeWithText("Save trusted origin").assertDoesNotExist()
        compose.onNodeWithText("Set one full Dashboard URL", substring = true).performScrollTo().assertIsDisplayed()
        capture("android-settings")
        compose.onNodeWithText("Done").performClick()
        advance(400)
        compose.mainClock.autoAdvance = false
        val music = MusicState(true, "Connected", "Test audio · sample track", "UI test fixture", true, listOf("pause"))
        compose.runOnIdle { state.value = ready().copy(music = music) }
        advance(16)
        capture("android-idle")
        openSettings()
        compose.onNodeWithText("Next").assertIsNotEnabled()
        compose.onNodeWithText("Pause").performClick()
        assertEquals("pause", musicCommand)
        compose.onNodeWithText("Done").performClick()
        advance(400)
        compose.runOnIdle {
            state.value = ready().copy(phase = "listening", input = "listening", voiceActive = true,
                heard = "Test transcript: turn on the kitchen lights", level = .6f,
                waveform = List(64) { (sin(it * .6) * .7 * sin(it * .11)).toFloat() },
                music = music)
        }
        advance(400)
        assertVoiceStatusIsAccessibleOnly("Listening")
        compose.onNodeWithTag("open-settings").assertIsNotEnabled()
        capture("android-listening")
        compose.runOnIdle { state.value = state.value.copy(phase = "working", input = "", speakerName = "Fixture speaker", speakerRGB = listOf(255, 0, 0)) }
        advance(400)
        assertVoiceStatusIsAccessibleOnly("Processing")
        capture("android-processing")
        compose.runOnIdle { state.value = state.value.copy(phase = "speaking", outputState = "queued", reply = "Test reply: the kitchen lights are on.") }
        advance(400)
        assertVoiceStatusIsAccessibleOnly("Waiting for response audio")
        compose.onNodeWithText("Test reply: the kitchen lights are on.").assertDoesNotExist()
        capture("android-queued")
        compose.runOnIdle { state.value = state.value.copy(outputState = "playing", playbackLevel = .7f) }
        advance(900)
        assertVoiceStatusIsAccessibleOnly("Playing response")
        capture("android-output")
        compose.runOnIdle { state.value = state.value.copy(phase = "idle", outputState = "draining") }
        advance(200)
        assertVoiceStatusIsAccessibleOnly("Finishing response")
        capture("android-drain")
        compose.runOnIdle { state.value = state.value.copy(alert = true) }
        advance(800)
        assertVoiceStatusIsAccessibleOnly("Alert")
        capture("android-alert")
        compose.runOnIdle { state.value = state.value.copy(alert = false, outputState = "", playbackLevel = null, phase = "listening", input = "listening",
            invocation = "follow_up", turn = 2, speakerName = "", speakerRGB = emptyList()) }
        advance(400)
        assertVoiceStatusIsAccessibleOnly("Listening for your answer")
        capture("android-follow-up")
        compose.runOnIdle { state.value = state.value.copy(phase = "muted", muted = true, connected = false, models = 0, heard = "", reply = "") }
        advance(400)
        compose.onNodeWithTag("voice-edge-glow").assertDoesNotExist()
        openSettings()
        compose.onNodeWithText("Microphone muted").assertIsDisplayed()
        capture("android-muted")
        compose.onNodeWithText("Done").performClick()
        advance(400)
        compose.runOnIdle { state.value = ready().copy(connected = false, wakeReady = false, talkReady = false) }
        advance(16)
        openSettings()
        compose.onNodeWithText("Voice disconnected").assertIsDisplayed()
        compose.onNodeWithTag("voice-edge-glow").assertDoesNotExist()
        capture("android-disconnected")
        compose.onNodeWithText("Done").performClick()
        advance(400)
        compose.runOnIdle { state.value = ready().copy(voiceActive = true, phase = "working", night = "on") }
        advance(400)
        capture("android-night-processing")
    }

    @Test fun parsesAdditiveFactsWithoutInventingReadinessPlaybackOrIdentity() {
        val base = """{"voice":{"phase":"speaking","connected":true,"muted":false,"alert":false},"models":3,"dashboard":"","reload":0,"problem":""}"""
        val missing = EncoreState.parse(base, ready().copy(speakerName = "Previous", speakerRGB = listOf(1, 2, 3)))
        assertFalse(missing.talkReady); assertFalse(missing.wakeReady); assertFalse(missing.captureReady)
        assertFalse(missing.accepted)
        assertFalse(missing.interactionVisible); assertEquals("none", missing.visual); assertEquals("", missing.speakerName)
        fun parse(fields: String) = EncoreState.parse(base.replace("\"phase\":\"speaking\"", fields), ready())
        val work = parse(""""phase":"working","active":true,"accepted":true,"capture_ready":true,"night":"off","speaker":{"name":"Fixture","color_known":true,"rgb":[255,0,0]}""")
        assertTrue(work.accepted)
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

    @Test fun quietMicAndReplyEachMoveTheGlowButSilenceAndQueuedAudioDoNot() {
        compose.mainClock.autoAdvance = false
        val listening = ready().copy(voiceActive = true, phase = "listening", input = "listening", waveform = List(64) { 0f })
        val state = mutableStateOf(listening)
        val reduced = mutableStateOf(false)
        compose.setContent {
            Box(Modifier.size(450.dp, 250.dp).background(androidx.compose.ui.graphics.Color.Black)) {
                VoiceEdgeGlow(state.value, reduced.value, Modifier.matchParentSize(), { _, _ -> })
            }
        }
        fun pixels(name: String, depth: IntRange = 0..7) = ImageIO.read(capture(name)).let {
            // Follow the whole perimeter. The deeper sample includes no text in the listening fixture.
            val width = it.width
            val height = it.height
            it.getRGB(0, 0, width, height, null, 0, width).filterIndexed { index, _ ->
                val x = index % width
                val y = index / width
                minOf(x, y, width - x - 1, height - y - 1) in depth
            }.toIntArray()
        }
        advance(320)
        val silent = pixels("glow-silent")
        assertTrue("Silence turned off the live mic band", silent.any { (it ushr 8 and 255) > 100 })
        advance(260)
        assertArrayEquals(silent, pixels("glow-silent-later"))
        compose.runOnIdle { state.value = listening.copy(level = .02f, waveform = List(64) { .014f }) }
        advance(520)
        val quiet = pixels("glow-quiet")
        assertTrue("Quiet input barely changed the band", quiet.indices.count { i ->
            (quiet[i] ushr 8 and 255) - (silent[i] ushr 8 and 255) >= 6
        } > 30)
        val bloom = pixels("glow-quiet", 10..16)
        assertTrue("Quiet input lit only the edge, with no inward glow", bloom.count { (it ushr 8 and 255) >= 4 } > 30)
        advance(240)
        assertFalse("Quiet input did not flow", quiet.contentEquals(pixels("glow-quiet-later")))
        val speaking = listening.copy(phase = "speaking", input = "", outputState = "playing", waveform = emptyList(), playbackLevel = .38f)
        compose.runOnIdle { state.value = speaking }
        advance(900)
        val reply = pixels("glow-reply")
        advance(670)
        assertFalse("Output-only audio did not flow", reply.contentEquals(pixels("glow-reply-later")))
        compose.runOnIdle { reduced.value = true }
        advance(64)
        val still = pixels("glow-reduced")
        advance(450)
        assertArrayEquals(still, pixels("glow-reduced-later"))
        compose.runOnIdle { reduced.value = false; state.value = speaking.copy(outputState = "queued") }
        advance(320)
        val queued = pixels("glow-queued")
        assertFalse("Reduced motion hid output intensity", still.contentEquals(queued))
        compose.runOnIdle { state.value = state.value.copy(playbackLevel = null) }
        advance(320)
        assertArrayEquals("Queued levels animated before playback", queued, pixels("glow-queued-no-level"))
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

    @Test @Config(qualifiers = "w360dp-h640dp-night-mdpi") fun narrowLargeTextKeepsCloseReachable() {
        compose.setContent {
            CompositionLocalProvider(LocalDensity provides Density(1f, 1.6f)) {
                EncoreScreen(ready().copy(voiceActive = true, invocation = "manual", phase = "working", heard = "Turn on the kitchen lights.",
                    problem = "Fixture notice: a separate music connection is unavailable."), {}, {}, {}, {}, { "" }, { "" }, {}, {}, {}, {}, reducedMotion = true)
            }
        }
        compose.onNodeWithContentDescription("Dismiss notice").assertIsDisplayed().assertHasClickAction()
        compose.onNodeWithTag("stop").assertIsDisplayed().assertHasClickAction()
        compose.onNodeWithTag("privacy").assertDoesNotExist()
        capture("android-narrow-large-text")
    }

    private fun ready() = EncoreState(running = true, microphoneReady = true, voiceKnown = true, connected = true, captureReady = true,
        talkAllowed = true, talkReady = true, wakeReady = true, phase = "idle", models = 2, night = "off", generation = 1, turn = 1)

    @Test fun landscapeGlowKeepsStopInRightDock() = checkGlowPlacement(landscape = true)

    @Test @Config(qualifiers = "w400dp-h700dp-mdpi") fun portraitGlowKeepsStopInBottomDock() = checkGlowPlacement(landscape = false)

    private fun checkGlowPlacement(landscape: Boolean) {
        compose.mainClock.autoAdvance = false
        val state = mutableStateOf(ready())
        lateinit var back: OnBackPressedDispatcher
        compose.setContent {
            back = checkNotNull(LocalOnBackPressedDispatcherOwner.current).onBackPressedDispatcher
            EncoreScreen(state.value, {}, {
                state.value = ready().copy(voiceActive = true, phase = "listening", input = "listening", level = .3f,
                    waveform = List(64) { (.3 * sin(it * .34)).toFloat() })
            }, { state.value = ready() }, {}, { "" }, { "" }, {}, {}, {}, {}, reducedMotion = false,
                dashboardContent = {
                    Box(Modifier.fillMaxSize().testTag("dashboard-content")) {
                        Text("Home", Modifier.padding(start = 24.dp, top = 56.dp), color = androidx.compose.ui.graphics.Color.White)
                    }
                })
        }
        val screen = compose.onNodeWithTag("dashboard-content").fetchSemanticsNode().boundsInRoot
        val talk = compose.onNodeWithTag("talk").fetchSemanticsNode().boundsInRoot
        val gear = compose.onNodeWithTag("open-settings")
        val settings = gear.fetchSemanticsNode().boundsInRoot
        val orientation = if (landscape) "landscape" else "portrait"
        if (landscape) {
            assertEquals(talk.right, settings.right, .5f)
            assertTrue(screen.right - talk.right < 56f && talk.bottom < settings.top)
        } else {
            assertEquals(talk.bottom, settings.bottom, .5f)
            assertTrue(screen.bottom - talk.bottom < 56f && talk.right < settings.left)
            assertEquals(screen.center.x, (talk.center.x + settings.center.x) / 2, .5f)
        }
        capture("edge-$orientation-idle")
        gear.performTouchInput { click() }
        advance(32)
        val settingsScreen = compose.onNodeWithTag("settings-screen").assertIsDisplayed()
        assertEquals(screen, settingsScreen.fetchSemanticsNode().boundsInRoot)
        gear.assertDoesNotExist()
        compose.onNodeWithText("Satellite settings").assertIsDisplayed()
        capture("fullscreen-settings-$orientation")
        if (landscape) compose.runOnIdle { back.onBackPressed() }
        else compose.onNodeWithText("Done").performTouchInput { click() }
        advance(32)
        settingsScreen.assertDoesNotExist()
        gear.assertIsDisplayed()

        val glow = compose.onNodeWithTag("voice-edge-glow")
        compose.onNodeWithTag("talk").performTouchInput { click() }
        advance(420)
        assertEquals(screen, glow.fetchSemanticsNode().boundsInRoot)
        assertVoiceStatusIsAccessibleOnly("Listening")
        assertEquals(talk, compose.onNodeWithTag("stop").assertIsDisplayed().fetchSemanticsNode().boundsInRoot)
        compose.onNodeWithTag("privacy").assertIsDisplayed()
        capture("edge-$orientation-listening")
        compose.runOnIdle { state.value = state.value.copy(phase = "speaking", input = "", outputState = "playing", playbackLevel = .55f) }
        advance(420)
        assertVoiceStatusIsAccessibleOnly("Playing response")
        capture("edge-$orientation-reply")
        compose.onNodeWithTag("stop").performTouchInput { click() }
        advance(32)
        glow.assertDoesNotExist()
        gear.assertIsDisplayed()
    }

    private fun assertVoiceStatusIsAccessibleOnly(status: String) {
        compose.onNodeWithContentDescription("Stop voice turn").assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, status))
        compose.onNodeWithText(status, useUnmergedTree = true).assertDoesNotExist()
    }

    private fun openSettings() {
        compose.onNodeWithTag("open-settings").performClick()
        advance(400)
    }

    private fun advance(milliseconds: Long) {
        // Apply pending snapshot invalidations before advancing the paused clock.
        compose.waitForIdle()
        compose.mainClock.advanceTimeBy(milliseconds)
        compose.waitForIdle()
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
        assertVoiceStatusIsAccessibleOnly("Playing announcement")
        compose.onNodeWithTag("voice-transcript").assertDoesNotExist()
        compose.onNodeWithTag("dashboard-tile").performTouchInput { click() }
        assertEquals(1, taps)
        compose.onNodeWithTag("open-settings").assertIsNotEnabled()
        compose.onNodeWithTag("privacy").assertIsEnabled()
        compose.onNodeWithTag("stop").performClick()
        assertEquals(1, stops)
        capture("android-announcement")

        compose.runOnIdle { state.value = EncoreState.parse(EncoreStateTest.snapshot(announcing = false, output = "draining"), state.value) }
        assertVoiceStatusIsAccessibleOnly("Finishing announcement")
        compose.onNodeWithTag("open-settings").assertIsNotEnabled()
        capture("android-announcement-draining")

        compose.runOnIdle { state.value = EncoreState.parse(EncoreStateTest.snapshot(active = false, announcing = false, output = "", owner = ""), state.value) }
        compose.onNodeWithTag("voice-edge-glow").assertDoesNotExist()
        compose.onNodeWithTag("talk").assertIsNotEnabled()
        compose.onNodeWithTag("open-settings").assertIsEnabled()
        compose.runOnIdle { state.value = EncoreState.parse(EncoreStateTest.snapshot(active = false, announcing = false, output = "", owner = "", talkAllowed = true), state.value) }
        // The canonical permission alone cannot bypass missing fresh capture health.
        compose.onNodeWithTag("talk").assertIsNotEnabled()
        compose.runOnIdle { state.value = state.value.copy(captureReady = true, talkReady = true) }
        compose.onNodeWithTag("talk").assertIsEnabled()
        compose.onNodeWithTag("dashboard-tile").performTouchInput { click() }
        assertEquals(2, taps)
    }

    @Test fun interruptedPlayingMusicCanResumeWithoutServerPlayCapability() {
        val state = mutableStateOf(EncoreState(running = true, microphoneReady = true, connected = true, talkAllowed = true, audioInterrupted = true,
            music = MusicState(enabled = true, status = "Connected", title = "Test audio · continuous stream", playing = true, commands = listOf("pause"))))
        var command = ""
        compose.setContent {
            EncoreScreen(state.value, {}, {}, {}, {}, { "" }, { "" }, {}, {}, { command = it }, {})
        }
        openSettings()
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
    private fun capture(name: String): File {
        compose.waitForIdle()
        compose.runOnIdle { ShadowLooper.idleMainLooper(32, TimeUnit.MILLISECONDS) }
        val directory = File("build/reports/screenshots").also { it.mkdirs() }
        // Compose captureToImage's forced redraw deadlocks Robolectric's main looper.
        // Roborazzi captures the real native graphics hierarchy without that device-only path.
        val file = File(directory, "$name.png")
        val node = compose.onAllNodes(isRoot()).onLast()
        node.captureRoboImage(file,
            RoborazziOptions(taskType = RoborazziTaskType.Record))
        return file
    }
}
