package com.encore.satellite

import android.graphics.BitmapFactory
import android.provider.Settings
import android.util.Base64
import android.view.MotionEvent
import android.view.View
import androidx.activity.OnBackPressedDispatcher
import androidx.activity.compose.LocalOnBackPressedDispatcherOwner
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.material3.Button
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.CompositionLocalProvider
import androidx.compose.runtime.DisposableEffect
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.semantics.SemanticsActions
import androidx.compose.ui.semantics.SemanticsProperties
import androidx.compose.ui.text.TextLayoutResult
import androidx.compose.ui.unit.Density
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
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
import java.awt.image.BufferedImage
import java.io.ByteArrayOutputStream
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
            EncoreScreen(state.value, { requests++ }, {}, {}, {}, { "test encryption key" }, { "" }, {}, {}, {}, {},
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
        compose.onNodeWithTag("display-clock").assertIsDisplayed()
        compose.runOnIdle { state.value = state.value.copy(running = false) }
        allow.assertDoesNotExist()
        compose.onNodeWithTag("display-clock").assertIsDisplayed()
        compose.onNodeWithContentDescription("Start satellite").assertDoesNotExist()
        openSettings()
        pair.assertDoesNotExist()
        val reveal = compose.onNodeWithText("Reveal ESPHome encryption key")
        val key = compose.onNodeWithText("test encryption key")
        val details = compose.onNodeWithText("Connection details")
        reveal.assertDoesNotExist()
        details.performScrollTo().performTouchInput { click() }
        reveal.performScrollTo().assertIsDisplayed()
        key.assertDoesNotExist()
        compose.onNodeWithText("Reveal Sendspin pairing token").performScrollTo().assertIsDisplayed()
        capture("android-ha-existing-settings")
        reveal.performScrollTo().performClick()
        key.performScrollTo().assertIsDisplayed()
        compose.onNodeWithText("Hide connection details").performScrollTo().performClick()
        details.performScrollTo().performClick()
        key.assertDoesNotExist()
    }

    @Test fun setupSurvivesDeferralsAndPairedUpgradesDoNotReplayIt() {
        val state = mutableStateOf(EncoreState(esphome = "unpaired", name = "encore-android-350ab2", espHomeFriendlyName = "Encore Android · 0AB2"))
        val microphone = mutableStateOf(false)
        val notificationsReviewed = mutableStateOf(false)
        val stage = mutableStateOf(SetupStage.NEW)
        compose.setContent {
            val current = state.value
            val step = setupStep(stage.value, current, microphone.value, !notificationsReviewed.value, true)
            val content: (@androidx.compose.runtime.Composable () -> Unit)? = if (step != null) {
                { SatelliteSetup(step, current, AndroidSetupStatus(home = false, remoteInstalls = "Off"),
                    onMicrophone = {}, onAppPermissions = null, onNotifications = {}, onSkipNotifications = { notificationsReviewed.value = true },
                    onPair = { state.value = current.copy(esphome = "waiting") },
                    onVoiceContinue = { stage.value = SetupStage.MUSIC },
                    onMusicEnable = { state.value = current.copy(music = MusicState(enabled = true, awaitingServer = true)) },
                    onMusicDisable = { state.value = current.copy(music = MusicState()) },
                    onMusicContinue = { stage.value = SetupStage.BLUETOOTH },
                    onBluetooth = {}, onExtras = { stage.value = SetupStage.EXTRAS },
                    onChooseHome = {}, onRemoteInstalls = {}, onDone = { stage.value = SetupStage.DONE }) }
            } else null
            EncoreScreen(current, {}, {}, {}, {}, { "" }, { "" }, {}, {}, {}, {}, dashboardContent = content,
                onSetup = { stage.value = SetupStage.EXTRAS }, microphonePermissionGranted = microphone.value)
        }
        compose.onNodeWithText("Allow microphone access").assertIsDisplayed().performClick()
        compose.onNodeWithText("Open app permissions").assertDoesNotExist()
        compose.onNodeWithText("Allow notifications").assertDoesNotExist()
        capture("android-first-run-microphone")
        compose.runOnIdle { microphone.value = true; stage.value = SetupStage.CORE; state.value = state.value.copy(running = true) }
        compose.onNodeWithText("Notifications (optional)").assertIsDisplayed()
        compose.onNodeWithText("Pair with Home Assistant").assertDoesNotExist()
        capture("android-first-run-notifications")
        compose.onNodeWithText("Not now").performClick()
        compose.onNodeWithText("Pair with Home Assistant").performClick()
        compose.onNodeWithText("two minutes", substring = true).assertIsDisplayed()
        capture("android-first-run-pairing")
        compose.runOnIdle { state.value = state.value.copy(esphome = "paired", voiceKnown = true,
            music = MusicState(enabled = true, awaitingServer = true)) }
        compose.onNodeWithText("Voice Incubator not connected").assertIsDisplayed()
        capture("android-first-run-voice")
        compose.onNodeWithText("Continue setup").performClick()
        compose.onNodeWithText("Music (optional)").assertIsDisplayed()
        compose.onNodeWithText("Turn off music").assertIsDisplayed()
        capture("android-first-run-music-enabled")
        compose.onNodeWithText("Continue setup").performClick() // No server has been discovered yet.
        compose.onNodeWithText("Set up Bluetooth proxy").assertIsDisplayed()
        compose.onNodeWithText("Not now").performClick()
        compose.onNodeWithText("Finish setup").assertIsDisplayed()
        compose.onNodeWithText("Encore shows a clock", substring = true).assertIsDisplayed()
        compose.onNodeWithText("Manage notifications").assertDoesNotExist()
        capture("android-first-run-checklist")
        compose.onNodeWithText("Done").performScrollTo().performClick()
        compose.onNodeWithTag("display-clock").assertIsDisplayed()
        openSettings()
        compose.onNodeWithText("Device setup").performClick()
        compose.onNodeWithText("Finish setup").assertIsDisplayed()

        // An upgraded, already paired installation has no onboarding cursor.
        compose.runOnIdle { stage.value = SetupStage.NEW; notificationsReviewed.value = false }
        compose.onNodeWithTag("display-clock").assertIsDisplayed()
        compose.onNodeWithText("Allow notifications").assertDoesNotExist()
        compose.onNodeWithText("Set up Bluetooth proxy").assertDoesNotExist()
        compose.onNodeWithText("Music (optional)").assertDoesNotExist()

        compose.runOnIdle { stage.value = SetupStage.MUSIC }
        compose.onNodeWithText("Turn off music").performClick()
        compose.onNodeWithText("Enable music").assertIsDisplayed()
        capture("android-first-run-music-disabled")
        compose.onNodeWithText("Continue setup").performClick()
        compose.onNodeWithText("Set up Bluetooth proxy").assertIsDisplayed()
        assertFalse(state.value.music.enabled)
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
        revealDock()
        compose.onNodeWithTag("privacy").assertIsOn()
        compose.runOnIdle { assertTrue((42L to false) in visibility) }
    }

    @Test fun idleDockAppearsOnlyOnTapAndHidesAfterInactivity() {
        compose.mainClock.autoAdvance = false
        val state = mutableStateOf(ready())
        compose.setContent {
            EncoreScreen(state.value, {}, {}, {}, { state.value = ready().copy(muted = it) }, { "" }, { "" }, {}, {}, {}, {})
        }
        val display = compose.onNodeWithTag("satellite-display")
        val gear = compose.onNodeWithTag("open-settings")
        gear.assertDoesNotExist()
        display.performTouchInput { swipeLeft() }
        advance(32)
        gear.assertDoesNotExist()
        capture("android-idle-dock-hidden")

        revealDock()
        gear.assertIsDisplayed()
        capture("android-idle-dock-revealed")
        advance(4_000)
        compose.onNodeWithTag("privacy").performTouchInput { click() }
        advance(4_000)
        compose.onNodeWithTag("privacy").assertIsOn()
        gear.assertIsDisplayed()
        advance(1_100)
        gear.assertDoesNotExist()
        compose.onNodeWithTag("privacy").assertDoesNotExist()
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
        compose.onNodeWithText("Bluetooth proxy").assertIsDisplayed()
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
        revealDock()
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

    @Test fun localWakeShowsAnImmediateCueAndOnlyAnAcceptedCallKeepsItsTranscript() {
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
        cue.assertDoesNotExist()
        capture("tablet-wake-idle")
        compose.runOnIdle { state.value = tentative }
        advance(64)
        cue.assertIsDisplayed().assertHasNoClickAction()
        capture("tablet-wake-cue")
        panel.assertDoesNotExist()
        oldIndicator.assertDoesNotExist()
        compose.runOnIdle { state.value = ready().copy(reason = "answered_elsewhere") }
        advance(64)
        cue.assertDoesNotExist()
        panel.assertDoesNotExist()
        compose.runOnIdle { state.value = tentative.copy(turn = 2) }
        advance(64)
        cue.assertIsDisplayed()
        compose.runOnIdle { state.value = ready().copy(turn = 2, reason = "wake_rejected") }
        advance(64)
        cue.assertDoesNotExist()
        panel.assertDoesNotExist()
        oldIndicator.assertDoesNotExist()

        compose.runOnIdle { state.value = tentative.copy(turn = 3) }
        advance(64)
        cue.assertIsDisplayed()
        compose.runOnIdle { state.value = tentative.copy(turn = 3, accepted = true, phase = "listening", input = "listening") }
        advance(64)
        cue.assertDoesNotExist()
        val compact = panel.assertIsDisplayed().fetchSemanticsNode().boundsInRoot
        compose.onNodeWithText("Listening…").assertIsDisplayed()
        compose.onNodeWithContentDescription("Encore Satellite").assertIsDisplayed()
        oldIndicator.assertDoesNotExist()
        capture("tablet-local-listening")
        val early = "What’s on my…"
        val question = "What’s on my calendar?"
        val prefix = "You have dinner with Sam"
        val reply = "You have dinner with Sam at seven. Want me to set a reminder?"
        compose.runOnIdle { state.value = state.value.copy(phase = "working", input = "", heard = early) }
        advance(64)
        compose.onNodeWithText("Listening…").assertDoesNotExist()
        compose.onNodeWithContentDescription("You: $early").assertIsDisplayed()
        assertVoiceStatusIsAccessibleOnly("Processing")
        capture("tablet-early-transcript")
        compose.runOnIdle { state.value = state.value.copy(phase = "speaking", outputState = "queued", outputOwner = "voice", heard = question, reply = prefix) }
        advance(64)
        compose.onNodeWithContentDescription("You: $early").assertDoesNotExist()
        compose.onNodeWithContentDescription("You: $question").assertIsDisplayed()
        compose.onNodeWithContentDescription("Encore: $prefix").assertIsDisplayed()
        assertVoiceStatusIsAccessibleOnly("Waiting for response audio")
        capture("tablet-streaming-reply")
        compose.runOnIdle { state.value = state.value.copy(reply = reply) }
        advance(64)
        compose.onNodeWithContentDescription("Encore: $reply").assertIsDisplayed()
        compose.onNodeWithContentDescription("Encore: $prefix").assertDoesNotExist()
        assertVoiceStatusIsAccessibleOnly("Waiting for response audio")
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
        compose.onNodeWithText("Choose dashboards").performScrollTo().assertIsDisplayed()
        compose.onNodeWithText("Home Assistant address").assertIsDisplayed()
        compose.onNodeWithText("Add dashboard URL").assertDoesNotExist()
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
        compose.onNodeWithText("Alert ringing").assertIsDisplayed()
        compose.onNodeWithTag("stop-alert").assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "Alert ringing"))
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

    @Test @Config(qualifiers = "w400dp-h700dp-night-mdpi") fun ringingSheetStaysTheSameWhenMicIsMuted() {
        val idle = ready().copy(timers = listOf(TimerCountdown(278), TimerCountdown(552)))
        val muted = idle.copy(phase = "muted", muted = true, inhibited = true, microphoneReady = false, captureReady = false,
            talkAllowed = false, talkReady = false, wakeReady = false)
        val ringing = idle.copy(alert = true, voiceActive = true, outputOwner = "alert", outputState = "playing",
            ring = RingingAlert(41, "Pasta", 2))
        val state = mutableStateOf(ringing.copy(
            ring = RingingAlert(40, "Tea", 3, listOf("Pasta", "", "Tea")),
            timers = listOf(TimerCountdown(278, "Rice"), TimerCountdown(552, "Roast"))))
        val stops = mutableListOf<Long>()
        compose.setContent {
            EncoreScreen(state.value, { fail("Opened microphone") }, { fail("Started voice turn") },
                { fail("Cancelled voice instead of stopping the alert") }, { checked ->
                    check(checked)
                    state.value = ringing.copy(phase = muted.phase, muted = true, inhibited = true, microphoneReady = false, captureReady = false)
                }, { "" }, { "" }, {}, {}, {}, {},
                onStopAlert = { stops += it; state.value = muted })
        }
        compose.onNodeWithTag("voice-edge-glow").assertIsDisplayed()
        for ((name, time) in listOf("Rice" to "4:38", "Roast" to "9:12")) {
            val rowLabel = compose.onNodeWithText(name).assertIsDisplayed().fetchSemanticsNode().boundsInRoot
            val rowTime = compose.onNodeWithText(time).assertIsDisplayed().fetchSemanticsNode().boundsInRoot
            assertEquals("$name should label $time", rowTime.center.y, rowLabel.center.y, 1f)
        }
        compose.onNodeWithText("Pasta, Timer, Tea").assertIsDisplayed()
        compose.onNodeWithText("3 alerts ringing").assertIsDisplayed()
        compose.onNodeWithTag("stop-alert").assertIsDisplayed()
        capture("android-timer-ringing-named")
        compose.runOnIdle { state.value = ringing }
        compose.onNodeWithText("Pasta, Timer, Tea").assertDoesNotExist()
        compose.onNodeWithText("Rice").assertDoesNotExist()
        compose.onNodeWithText("Roast").assertDoesNotExist()
        compose.onNodeWithText("Pasta").assertIsDisplayed()
        compose.onNodeWithText("2 alerts ringing").assertIsDisplayed()
        compose.onNodeWithText("9:12").assertIsDisplayed()
        val stop = compose.onNodeWithTag("stop-alert")
        val sheet = compose.onNodeWithTag("timer-sheet")
        val bounds = sheet.fetchSemanticsNode().boundsInRoot
        val buttonBounds = stop.fetchSemanticsNode().boundsInRoot
        capture("android-timer-ringing-unmuted")
        compose.onNodeWithContentDescription("Mute mic").performTouchInput { click() }
        compose.onNodeWithContentDescription("Unmute mic").assertIsDisplayed()
        compose.onNode(hasText("Microphone muted") and hasAnyAncestor(hasTestTag("timer-sheet"))).assertDoesNotExist()
        assertEquals(bounds, sheet.fetchSemanticsNode().boundsInRoot)
        assertEquals(buttonBounds, stop.fetchSemanticsNode().boundsInRoot)
        stop.assertIsEnabled().assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, "2 alerts ringing"))
        capture("android-timer-ringing-muted")
        stop.performTouchInput { click() }
        compose.onNodeWithTag("timer-sheet").assertDoesNotExist()
        compose.onNodeWithText("4:38 · 2 timers").assertIsDisplayed()
        revealDock()
        compose.onNodeWithTag("talk").assertIsNotEnabled()
        assertEquals(listOf(41L), stops)
    }

    @Test fun runningCountdownsShareTheListeningSheet() {
        val idle = ready().copy(timers = listOf(TimerCountdown(93, "Tea"), TimerCountdown(3725)))
        val state = mutableStateOf(idle)
        compose.setContent {
            EncoreScreen(state.value, {}, {}, { fail("Closing timers cancelled a voice turn") }, {}, { "" }, { "" }, {}, {}, {}, {})
        }
        val chip = compose.onNodeWithTag("timer-chip")
        compose.onNodeWithText("1:33 · 2 timers").assertIsDisplayed()
        capture("android-timer-chip")
        chip.performTouchInput { click() }
        compose.onNodeWithText("Tea").assertIsDisplayed()
        compose.onNodeWithText("Timer").assertIsDisplayed()
        compose.onNodeWithText("1:02:05").assertIsDisplayed()
        capture("android-timer-running")
        compose.runOnIdle { state.value = idle.copy(timers = listOf(TimerCountdown(92), TimerCountdown(3724))) }
        compose.onNodeWithText("1:32").assertIsDisplayed()
        compose.runOnIdle {
            state.value = state.value.copy(phase = "tentative", invocation = "wake", input = "tentative", voiceActive = true)
        }
        compose.onNodeWithTag("voice-wake-cue").assertIsDisplayed()
        compose.runOnIdle { state.value = idle }
        compose.onNodeWithContentDescription("Close timers").performTouchInput { click() }
        chip.assertIsDisplayed()
        compose.runOnIdle {
            state.value = idle.copy(phase = "listening", invocation = "wake", accepted = true, input = "listening", voiceActive = true)
        }
        compose.onNodeWithTag("voice-transcript").assertIsDisplayed()
        compose.onNodeWithText("Listening…").assertIsDisplayed()
        compose.onNodeWithText("1:02:05").assertIsDisplayed()
        capture("android-timer-listening")
        compose.runOnIdle { state.value = idle.copy(connected = false) }
        chip.assertDoesNotExist()
        compose.onNodeWithTag("voice-transcript").assertDoesNotExist()
    }

    @Test fun parsesAdditiveFactsWithoutInventingReadinessPlaybackOrIdentity() {
        val base = """{"voice":{"phase":"speaking","connected":true,"muted":false,"alert":false},"models":3,"reload":0,"problem":""}"""
        val missing = EncoreState.parse(base, ready().copy(speakerName = "Previous", speakerRGB = listOf(1, 2, 3)))
        assertFalse(missing.talkReady); assertFalse(missing.wakeReady); assertFalse(missing.captureReady)
        assertFalse(missing.accepted)
        assertFalse(missing.interactionVisible); assertEquals("none", missing.visual); assertEquals("", missing.speakerName)
        fun parse(fields: String) = EncoreState.parse(base.replace("\"phase\":\"speaking\"", fields), ready())
        val timerState = parse(""""phase":"speaking","timers":[{"seconds":92},{"seconds":0}],"ring":{"sequence":4,"name":"Pasta","count":2}""")
        assertEquals(listOf(TimerCountdown(92)), timerState.timers)
        assertEquals(RingingAlert(4, "Pasta", 2), timerState.ring)
        val named = parse(""""phase":"idle","timers":[{"seconds":0,"name":"Expired"},{"seconds":91,"name":"Tea"},{"seconds":77}],"ring":{"sequence":5,"name":"Bread","count":3,"names":["Pasta","","Bread"]}""")
        assertEquals(listOf(TimerCountdown(91, "Tea"), TimerCountdown(77)), named.timers)
        assertEquals(RingingAlert(5, "Bread", 3, listOf("Pasta", "", "Bread")), named.ring)
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
        assertEquals("alert", drain.copy(muted = true, alert = true).visual)
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

    @Test @Config(qualifiers = "w400dp-h700dp-night-mdpi") fun portraitGlowKeepsStopInBottomDock() = checkGlowPlacement(landscape = false)

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
        revealDock()
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
        gear.assertDoesNotExist()
        revealDock()

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
        gear.assertDoesNotExist()
    }

    private fun assertVoiceStatusIsAccessibleOnly(status: String) {
        compose.onNodeWithContentDescription("Stop voice turn").assert(SemanticsMatcher.expectValue(SemanticsProperties.StateDescription, status))
        compose.onNodeWithText(status, useUnmergedTree = true).assertDoesNotExist()
    }

    private fun openSettings() {
        revealDock()
        compose.onNodeWithTag("open-settings").performClick()
        advance(400)
    }

    private fun revealDock() {
        compose.onNodeWithTag("satellite-display").performTouchInput { click(Offset(width * .9f, height * .2f)) }
        advance(32)
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
        revealDock()
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
        revealDock()
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

    @Test fun sendspinDiscoveryRequiresExplicitEnableAndRevocationRequiresConfirmation() {
        val state = mutableStateOf(EncoreState(running = true))
        var enabled = 0
        var revoked = 0
        compose.setContent {
            EncoreScreen(state.value, {}, {}, {}, {}, { "" }, { "fixture-token" }, {
                enabled++
                state.value = state.value.copy(music = MusicState(enabled = true, awaitingServer = true, status = "Looking for a server…"))
            }, { state.value = state.value.copy(music = MusicState(), musicProblem = "") }, {}, {}, onMusicRevoke = {
                revoked++
                state.value = state.value.copy(music = MusicState())
            })
        }
        openSettings()
        val enable = compose.onNodeWithText("Enable music")
        enable.assertDoesNotExist()
        compose.onNodeWithText("Specify server").assertDoesNotExist()
        capture("android-sendspin-settings-collapsed")
        compose.onNodeWithText("Connection details").performScrollTo().performTouchInput { click() }
        compose.onNodeWithText("Specify server").performScrollTo().assertIsDisplayed()
        compose.runOnIdle { assertEquals(0, enabled) }
        capture("android-sendspin-auto-off")
        enable.performScrollTo().assertIsEnabled().performClick()
        compose.onNodeWithText("Sendspin · Looking for a server…").performScrollTo().assertIsDisplayed()
        compose.onNodeWithText("Stop music").assertDoesNotExist()
        capture("android-sendspin-auto-searching")
        val conflict = "Multiple Sendspin servers found. You can specify a server below."
        compose.runOnIdle { state.value = state.value.copy(music = state.value.music.copy(status = "Waiting for a server"), musicProblem = conflict) }
        compose.onNodeWithText(conflict).performScrollTo().assertIsDisplayed()
        capture("android-sendspin-auto-conflict")
        compose.onNodeWithText("Turn off music").performScrollTo().performClick()
        compose.onNodeWithText(conflict).assertDoesNotExist()
        enable.performScrollTo().performClick()
        compose.onNodeWithText("Revoke all Sendspin trust").performScrollTo().performClick()
        compose.onNodeWithText("Revoke Sendspin pairing?").assertIsDisplayed()
        capture("android-sendspin-auto-revoke")
        compose.onNodeWithText("Cancel").performClick()
        compose.runOnIdle { assertEquals(2, enabled); assertEquals(0, revoked) }
        compose.onNodeWithText("Revoke all Sendspin trust").performClick()
        compose.onNodeWithText("Revoke pairing").performClick()
        compose.runOnIdle { assertEquals(1, revoked) }
        compose.onNodeWithText("Enable music").assertExists()
    }

    @Test fun manualSendspinServerCanReplaceAmbiguousDiscoveryAndReturnToAuto() {
        val conflict = "Multiple Sendspin servers found. You can specify a server below."
        val state = mutableStateOf(EncoreState(running = true,
            music = MusicState(enabled = true, awaitingServer = true, status = "Waiting for a server"), musicProblem = conflict))
        val requests = mutableListOf<String?>()
        compose.setContent {
            EncoreScreen(state.value, {}, {}, {}, {}, { "" }, { "" }, { server ->
                requests += server
                state.value = state.value.copy(manualMusicServer = server, musicProblem = "",
                    music = state.value.music.copy(status = if (server == null) "Looking for a server…" else "Connecting…"))
            }, {}, {}, {})
        }
        openSettings()
        compose.onNodeWithText("Connection details").performScrollTo().performTouchInput { click() }
        compose.onNodeWithText("Specify server").performScrollTo().performClick()
        val field = compose.onNode(hasSetTextAction() and hasText("Host and port"))
        field.performTextInput("music.local")
        compose.onNodeWithText("Save and connect").assertIsNotEnabled()
        compose.onNodeWithText("Cancel").performClick()
        compose.runOnIdle { assertTrue(requests.isEmpty()) }
        compose.onNodeWithText(conflict).performScrollTo().assertIsDisplayed()

        compose.onNodeWithText("Specify server").performScrollTo().performClick()
        field.performTextInput("music.local:8931/music/ws")
        capture("android-sendspin-manual-entry")
        compose.onNodeWithText("Save and connect").assertIsEnabled().performClick()
        compose.onNodeWithText("Server: music.local:8931/music/ws").performScrollTo().assertIsDisplayed()
        capture("android-sendspin-manual-selected")
        compose.onNodeWithText("Edit server").performScrollTo().performClick()
        compose.onNodeWithText("music.local:8931/music/ws").assertExists()
        compose.onNodeWithText("Cancel").performClick()
        compose.onNodeWithText("Connect by auto-discovery").performScrollTo().performClick()
        compose.onNodeWithText("Sendspin · Looking for a server…").performScrollTo().assertIsDisplayed()
        compose.runOnIdle { assertEquals(listOf("music.local:8931/music/ws", null), requests) }
    }

    @Test fun interruptedPlayingMusicCanResumeWithoutServerPlayCapability() {
        val state = mutableStateOf(EncoreState(running = true, microphoneReady = true, connected = true, talkAllowed = true, audioInterrupted = true,
            music = MusicState(enabled = true, status = "Connected", title = "Test audio · continuous stream", playing = true, commands = listOf("pause"))))
        var command = ""
        compose.setContent {
            EncoreScreen(state.value, {}, {}, {}, {}, { "" }, { "" }, {}, {}, { command = it }, {})
        }
        openSettings()
        compose.onNodeWithText("Pause").assertDoesNotExist()
        compose.onNodeWithText("Silent on this tablet").assertIsDisplayed()
        val resume = compose.onNodeWithText("Resume on this tablet").assertIsEnabled()
        capture("android-focus-lost")
        resume.performClick()
        assertEquals("resume", command)
        compose.runOnIdle { state.value = state.value.copy(audioInterrupted = false) }
        resume.assertDoesNotExist()
        compose.onNodeWithText("Pause").assertIsDisplayed()
        capture("android-focus-resumed")
        compose.onNodeWithText("Stop music").performClick()
        assertEquals("stop", command)
        compose.runOnIdle { state.value = state.value.copy(music = state.value.music.copy(stopped = true)) }
        compose.onNodeWithText("Stopped on this tablet").assertIsDisplayed()
        compose.onNodeWithText("Pause").assertDoesNotExist()
        compose.onNodeWithText("Stop music").assertIsNotEnabled()
        capture("android-sendspin-stopped")
        compose.onNodeWithText("Play").assertIsEnabled().performClick()
        assertEquals("play", command)
    }

    @Test fun nowPlayingCanMinimizeExpandAndDisappearWhenStopped() {
        val state = mutableStateOf(ready().copy(music = MusicState(enabled = true, status = "Connected")))
        val commands = mutableListOf<String>()
        compose.setContent {
            EncoreScreen(state.value, {}, {}, {}, {}, { "" }, { "" }, {}, {}, { commands += it }, {}, reducedMotion = true)
        }
        val card = compose.onNodeWithTag("now-playing")
        card.assertDoesNotExist()
        val artwork = checkNotNull(decodeSendspinArtwork(exampleCover()))
        compose.runOnIdle {
            state.value = state.value.copy(music = state.value.music.copy(playing = true,
                title = "Northern Lights", artist = "The Satellite Sessions", commands = listOf("pause", "next"), artwork = artwork))
        }
        val clock = compose.onNodeWithTag("display-clock")
        val mini = compose.onNodeWithTag("floating-player")
        clock.assertDoesNotExist()
        card.assertIsDisplayed()
        compose.onNodeWithTag("now-playing-time").assertIsDisplayed()
        compose.onNodeWithText("Northern Lights").assertIsDisplayed()
        compose.onNodeWithText("The Satellite Sessions").assertIsDisplayed()
        compose.onNodeWithContentDescription("Previous").assertIsNotEnabled()
        compose.onNodeWithContentDescription("Next").assertIsEnabled().performClick()
        compose.onNodeWithContentDescription("Pause").assertIsEnabled().performClick()
        capture("android-now-playing-landscape")
        compose.runOnIdle { state.value = state.value.copy(audioInterrupted = true) }
        val resume = compose.onNodeWithContentDescription("Resume on this tablet")
        val minimize = compose.onNodeWithContentDescription("Minimize")
        compose.onNodeWithText("Silent on this tablet").assertIsDisplayed()
        compose.onNodeWithContentDescription("Pause").assertDoesNotExist()
        compose.onNodeWithText("Resume audio").assertDoesNotExist()
        capture("android-now-playing-interrupted")
        resume.assertIsEnabled().performTouchInput { click() }
        minimize.performTouchInput { click() }
        mini.assertIsDisplayed()
        resume.assertIsEnabled().performTouchInput { click() }
        capture("android-floating-player-interrupted")
        compose.onNodeWithContentDescription("Expand Now Playing").performTouchInput { click() }
        compose.runOnIdle {
            assertEquals(listOf("next", "pause", "resume", "resume"), commands)
            state.value = state.value.copy(audioInterrupted = false,
                music = state.value.music.copy(playing = false, groupPlayback = "paused", commands = listOf("play")))
        }
        compose.onNodeWithText("Paused").assertIsDisplayed()
        compose.onNodeWithContentDescription("Play").assertIsEnabled().performClick()
        capture("android-now-playing-paused")
        compose.runOnIdle { state.value = state.value.copy(music = state.value.music.copy(playing = true, groupPlayback = "stopped")) }
        card.assertDoesNotExist()
        compose.onNodeWithText("Northern Lights").assertDoesNotExist()
        clock.assertIsDisplayed()
        capture("android-now-playing-stopped")
        compose.runOnIdle { state.value = state.value.copy(music = state.value.music.copy(groupPlayback = "playing")) }
        minimize.performTouchInput { click() }
        clock.assertIsDisplayed()
        mini.assertIsDisplayed()
        compose.onNodeWithContentDescription("Expand Now Playing").performTouchInput { click() }
        card.assertIsDisplayed()
        mini.assertDoesNotExist()
        minimize.performTouchInput { click() }
        compose.onNodeWithContentDescription("Stop music").assertIsEnabled().performClick()
        compose.runOnIdle {
            assertEquals(listOf("next", "pause", "resume", "resume", "play", "stop"), commands)
            state.value = state.value.copy(music = state.value.music.copy(stopped = true))
        }
        card.assertDoesNotExist()
        mini.assertDoesNotExist()
        compose.runOnIdle {
            state.value = state.value.copy(music = state.value.music.copy(stopped = false))
            state.value = state.value.copy(voiceActive = true, phase = "listening", inputOpen = true)
        }
        card.assertDoesNotExist()
        compose.onNodeWithTag("stop").assertIsDisplayed()
        compose.runOnIdle { state.value = ready().copy(music = MusicState(enabled = true, status = "Reconnecting")) }
        card.assertDoesNotExist()
    }

    private fun exampleCover(): ByteArray {
        val cover = BufferedImage(1024, 1024, BufferedImage.TYPE_INT_RGB)
        cover.createGraphics().apply {
            scale(2.0, 2.0)
            color = java.awt.Color(37, 33, 86)
            fillRect(0, 0, 512, 512)
            color = java.awt.Color(255, 170, 107)
            fillOval(140, 72, 232, 232)
            color = java.awt.Color(86, 184, 170)
            fillPolygon(intArrayOf(0, 160, 289, 424, 512, 512, 0), intArrayOf(324, 208, 328, 245, 318, 512, 512), 7)
            dispose()
        }
        return ByteArrayOutputStream().apply { ImageIO.write(cover, "jpg", this) }.toByteArray()
    }

    @Test fun nowPlayingShowsLargeDecodedCoverAndFallsBackWhenArtworkIsCleared() {
        val artwork = checkNotNull(decodeSendspinArtwork(exampleCover()))
        assertEquals(512, artwork.width)
        assertEquals(512, artwork.height)
        assertNull(decodeSendspinArtwork(byteArrayOf(1, 2, 3)))
        val state = mutableStateOf(ready().copy(music = MusicState(enabled = true, playing = true,
            title = "Northern Lights", artist = "The Satellite Sessions", commands = listOf("pause", "next"), artwork = artwork)))
        compose.setContent { EncoreScreen(state.value, {}, {}, {}, {}, { "" }, { "" }, {}, {}, {}, {}) }
        val bounds = compose.onNodeWithContentDescription("Album cover").assertIsDisplayed().fetchSemanticsNode().boundsInRoot
        assertTrue("The page should visibly feature the cover", bounds.width >= 320 && bounds.height >= 320)
        assertNowPlayingHeader()
        capture("android-album-cover")
        compose.runOnIdle { state.value = state.value.copy(music = state.value.music.copy(artwork = null)) }
        compose.onNodeWithContentDescription("Album cover").assertDoesNotExist()
        compose.onNodeWithTag("now-playing").assertIsDisplayed()
        capture("android-album-cover-fallback")
    }

    @Test @Config(qualifiers = "w360dp-h640dp-mdpi") fun portraitPlayersLeaveRoomForTimersAndVoiceDock() {
        val artwork = checkNotNull(decodeSendspinArtwork(exampleCover()))
        compose.setContent {
            EncoreScreen(ready().copy(timers = listOf(TimerCountdown(273)),
                music = MusicState(enabled = true, playing = true, title = "Northern Lights",
                artist = "The Satellite Sessions", commands = listOf("pause", "previous", "next"), artwork = artwork)),
                {}, {}, {}, {}, { "" }, { "" }, {}, {}, {}, {})
        }
        val pause = compose.onNodeWithContentDescription("Pause").assertIsDisplayed().fetchSemanticsNode().boundsInRoot
        val next = compose.onNodeWithContentDescription("Next").assertIsDisplayed().fetchSemanticsNode().boundsInRoot
        compose.onNodeWithTag("talk").assertDoesNotExist()
        revealDock()
        val dock = compose.onNodeWithTag("talk").assertIsDisplayed().fetchSemanticsNode().boundsInRoot
        val chip = compose.onNodeWithTag("timer-chip").assertIsDisplayed()
        val chipBounds = chip.fetchSemanticsNode().boundsInRoot
        val card = compose.onNodeWithTag("now-playing").assertIsDisplayed()
        assertNowPlayingHeader()
        assertEquals("Transport controls should stay in one row", pause.center.y, next.center.y, 1f)
        assertTrue("The music card must stay above the timer chip", card.fetchSemanticsNode().boundsInRoot.bottom <= chipBounds.top)
        assertTrue("The timer chip must stay above the voice dock", chipBounds.bottom <= dock.top)
        compose.onNodeWithTag("privacy").assertIsDisplayed()
        compose.onNodeWithTag("open-settings").assertIsDisplayed()
        capture("android-now-playing-timer-portrait")
        compose.onNodeWithContentDescription("Minimize").performTouchInput { click() }
        val mini = compose.onNodeWithTag("floating-player").assertIsDisplayed()
        assertTrue("The mini-player must stay above the timer chip", mini.fetchSemanticsNode().boundsInRoot.bottom <= chipBounds.top)
        capture("android-floating-player-timer-portrait")
        chip.performTouchInput { click() }
        compose.onNodeWithTag("timer-sheet").assertIsDisplayed()
        card.assertDoesNotExist()
        mini.assertDoesNotExist()
        compose.onNodeWithContentDescription("Close timers").performTouchInput { click() }
        mini.assertIsDisplayed()
    }

    private fun assertNowPlayingHeader() {
        val page = compose.onNodeWithTag("now-playing").fetchSemanticsNode().boundsInRoot
        val clock = compose.onNodeWithTag("now-playing-time").assertIsDisplayed().fetchSemanticsNode().boundsInRoot
        val minimize = compose.onNodeWithContentDescription("Minimize").assertIsEnabled().assertIsDisplayed()
            .fetchSemanticsNode().boundsInRoot
        val cover = compose.onNodeWithContentDescription("Album cover").assertIsDisplayed().fetchSemanticsNode().boundsInRoot
        assertEquals("Clock should be centered on the page", page.center.x, clock.center.x, 1f)
        assertEquals("Minimize should be under the clock", clock.center.x, minimize.center.x, 1f)
        assertTrue("Minimize must be between the clock and artwork", clock.bottom <= minimize.top && minimize.bottom <= cover.top)
        assertTrue("Minimize needs a full 48dp touch target", minimize.width >= 48f && minimize.height >= 48f)
    }

    @Test fun dashboardNavigationUsesOnlyTheConfiguredOrigin() {
        val origin = "https://ha.example:8123"
        assertTrue(trustedNavigation(origin, "$origin/auth/authorize?client_id=encore"))
        for (url in listOf("https://evil.example/", "http://ha.example:8123/", "https://ha.example/", "file:///data/local", "intent://ha.example", "https://user@ha.example:8123/")) {
            assertFalse(url, trustedNavigation(origin, url))
        }
        val moved = "http://new-ha.example:8124"
        assertTrue(trustedNavigation(moved, "$moved/auth/authorize"))
        assertFalse(trustedNavigation(moved, "$origin/lovelace/home"))
    }

    @Test fun landscapeClockFillsDisplayWithoutVoiceStatus() = checkClock("24", "landscape")

    @Test @Config(qualifiers = "w400dp-h700dp-mdpi")
    fun portraitClockFitsTimeWithAmPm() = checkClock("12", "portrait")

    private fun checkClock(format: String, orientation: String) {
        Settings.System.putString(RuntimeEnvironment.getApplication().contentResolver, Settings.System.TIME_12_24, format)
        compose.setContent {
            EncoreScreen(ready(), {}, {}, {}, {}, { "" }, { "" }, {}, {}, {}, {})
        }
        val clock = compose.onNodeWithTag("display-clock").assertIsDisplayed()
        compose.onNodeWithText("Wake ready").assertDoesNotExist()
        val layouts = mutableListOf<TextLayoutResult>()
        compose.onNodeWithTag("display-clock-time").assertIsDisplayed()
            .performSemanticsAction(SemanticsActions.GetTextLayoutResult) { assertTrue(it(layouts)) }
        val layout = layouts.single()
        assertTrue("Time must fit on one line", layout.lineCount == 1 && !layout.hasVisualOverflow)
        val timeWidth = layout.getLineRight(0) - layout.getLineLeft(0)
        assertTrue("Time must fill most of the screen width", timeWidth > clock.fetchSemanticsNode().size.width * .70f)
        capture("android-clock-$orientation")
    }

    @Test fun dashboardPickerCapsOrderedSelectionAndCarouselSwipesToClockAndOtherViews() {
        val available = (1..6).map { DashboardPage("/lovelace/room-$it", "Room $it") }
        val selected = mutableStateOf(emptyList<DashboardPage>())
        val choosing = mutableStateOf(true)
        val dashboardTouches = mutableListOf<Int>()
        val dashboardCreations = mutableListOf<String>()
        compose.setContent {
            MaterialTheme(colorScheme = darkColorScheme(primary = Color(0xff97e6d4), surface = Color(0xff152330))) {
                Surface(Modifier.fillMaxSize()) {
                    if (choosing.value) Box(contentAlignment = Alignment.TopCenter) {
                        DashboardChoices(available, selected.value) { selected.value = it }
                    } else DashboardCarousel(selected.value, swipeEnabled = true) { page, _ ->
                        Box(Modifier.fillMaxSize().testTag(page.path)
                            .background(if (page == available.first()) Color(0xff153a38) else Color(0xff152330))) {
                            Text("Showing ${page.title}", Modifier.align(Alignment.Center))
                            AndroidView(factory = { context -> View(context).apply {
                                dashboardCreations += page.path
                                setOnTouchListener { view, event ->
                                    dashboardTouches += event.actionMasked
                                    view.parent.requestDisallowInterceptTouchEvent(true)
                                    true
                                }
                            } }, modifier = Modifier.fillMaxSize())
                        }
                    }
                }
            }
        }
        for (index in listOf(4, 1, 5, 2, 3)) compose.onNodeWithText("Room $index").performClick()
        compose.onNodeWithText("5 of 5 selected").assertIsDisplayed()
        compose.onNodeWithText("Room 6").assertIsNotEnabled()
        capture("android-dashboard-picker")
        compose.runOnIdle { choosing.value = false }
        compose.onNodeWithText("Showing Room 4").assertIsDisplayed()
        val display = compose.onNodeWithTag("dashboard-carousel")
        compose.onNodeWithContentDescription("Previous display page").assertDoesNotExist()
        compose.onNodeWithText("2 / 6").assertDoesNotExist()
        display.performTouchInput { click() }
        compose.runOnIdle {
            assertEquals(listOf(MotionEvent.ACTION_DOWN, MotionEvent.ACTION_UP), dashboardTouches)
            dashboardTouches.clear()
        }
        display.performTouchInput { swipeUp() }
        compose.onNodeWithText("Showing Room 4").assertIsDisplayed()
        compose.runOnIdle {
            assertTrue(MotionEvent.ACTION_MOVE in dashboardTouches)
            assertEquals(MotionEvent.ACTION_UP, dashboardTouches.last())
            assertFalse(MotionEvent.ACTION_CANCEL in dashboardTouches)
            dashboardTouches.clear()
        }
        compose.mainClock.autoAdvance = false
        display.performTouchInput { swipeLeft() }
        advance(96)
        val arriving = compose.onNodeWithTag(available.first().path).assertIsDisplayed()
        assertTrue("Incoming dashboard should still be sliding into view",
            arriving.fetchSemanticsNode().boundsInRoot.width in 1f..<display.fetchSemanticsNode().boundsInRoot.width)
        capture("android-dashboard-slide")
        compose.mainClock.autoAdvance = true
        compose.onNodeWithText("Showing Room 1").assertIsDisplayed()
        compose.runOnIdle {
            assertEquals(MotionEvent.ACTION_DOWN, dashboardTouches.first())
            assertEquals(MotionEvent.ACTION_CANCEL, dashboardTouches.last())
            assertFalse(MotionEvent.ACTION_UP in dashboardTouches)
        }
        capture("android-dashboard-carousel")
        display.performTouchInput { swipeRight() }
        compose.onNodeWithText("Showing Room 4").assertIsDisplayed()
        display.performTouchInput { swipeRight() }
        compose.onNodeWithTag("display-clock").assertIsDisplayed()
        display.performTouchInput { swipeRight() }
        compose.onNodeWithTag("display-clock").assertIsDisplayed()
        capture("android-clock-carousel")
        display.performTouchInput { swipeLeft() }
        compose.onNodeWithText("Showing Room 4").assertIsDisplayed()
        compose.runOnIdle {
            assertEquals("Swiping must not recreate any native dashboard view",
                selected.value.map { it.path }.sorted(), dashboardCreations.sorted())
        }
        compose.runOnIdle { selected.value = emptyList() }
        display.assertDoesNotExist()
        compose.onNodeWithTag("display-clock").assertIsDisplayed()
        capture("android-dashboard-clock")
    }

    @Test fun dashboardSelectionPersistsAnExplicitClockChoiceAndRejectsOtherOrigins() {
        val origin = "https://ha.example:8123"
        val clock = DashboardSelection(origin, emptyList())
        assertEquals(clock, DashboardSelection.decode(clock.encode()))
        val page = DashboardPage("/lovelace/kitchen", "Kitchen")
        val invalid = listOf("$origin/lovelace/home", "//elsewhere.example/lovelace", "/lovelace/%2e%2e/auth").map {
            DashboardPage(it, "Other")
        }
        assertEquals(DashboardSelection(origin, listOf(page)), DashboardSelection.decode(
            DashboardSelection(origin, listOf(page) + invalid + page).encode()))
        assertNull(homeAssistantOrigin("$origin/lovelace/kitchen"))
        assertNull(homeAssistantOrigin("https://user@ha.example"))
        assertEquals(origin, homeAssistantOrigin(" $origin/ "))
    }

    @Test fun carouselReturnsToMusicClockOnlyWhenIdleFocusedAndNotMinimized() {
        val pages = listOf(DashboardPage("/kitchen", "Kitchen"), DashboardPage("/living", "Living room"))
        val music = mutableStateOf(true)
        val focused = mutableStateOf(true)
        val navigation = mutableStateOf(true)
        var touches = 0
        val mounted = mutableListOf<String>()
        compose.setContent {
            DashboardCarousel(pages, swipeEnabled = navigation.value, reducedMotion = true,
                nowPlaying = if (music.value) ({ minimize -> Button(onClick = minimize) { Text("Minimize music") } }) else null,
                miniPlayer = { expand -> Button(onClick = expand) { Text("Expand music") } },
                musicFocused = focused.value) { dashboard, _ ->
                DisposableEffect(dashboard.path) {
                    mounted += dashboard.path
                    onDispose {}
                }
                Box(Modifier.fillMaxSize(), contentAlignment = Alignment.Center) {
                    Button(onClick = { touches++ }) { Text(dashboard.title) }
                }
            }
        }
        val carousel = compose.onNodeWithTag("dashboard-carousel")
        val full = compose.onNodeWithText("Minimize music")
        val mini = compose.onNodeWithText("Expand music")
        val kitchen = compose.onNodeWithText("Kitchen").assertIsDisplayed()
        val living = compose.onNodeWithText("Living room")
        val clock = compose.onNodeWithTag("display-clock")
        compose.mainClock.advanceTimeBy(31_000)
        kitchen.assertIsDisplayed() // A new track never steals the opening page.
        carousel.performTouchInput { swipeRight() }
        full.assertIsDisplayed()
        carousel.performTouchInput { swipeLeft() }
        mini.assertIsDisplayed()
        compose.mainClock.advanceTimeBy(25_000)
        kitchen.performTouchInput { click() }
        assertEquals(1, touches)
        compose.mainClock.advanceTimeBy(10_000)
        kitchen.assertIsDisplayed() // Past the original deadline, but the tap reset it.
        compose.runOnIdle { focused.value = false }
        compose.waitForIdle()
        compose.mainClock.advanceTimeBy(31_000)
        kitchen.assertIsDisplayed()
        compose.runOnIdle { navigation.value = false }
        compose.runOnIdle { focused.value = true }
        compose.waitForIdle()
        compose.mainClock.advanceTimeBy(31_000)
        kitchen.assertIsDisplayed()
        compose.runOnIdle { navigation.value = true }
        kitchen.assertIsDisplayed()
        compose.mainClock.advanceTimeBy(31_000)
        full.assertIsDisplayed()
        full.performClick()
        clock.assertIsDisplayed()
        mini.assertIsDisplayed()
        carousel.performTouchInput { swipeLeft() }
        kitchen.assertIsDisplayed()
        carousel.performTouchInput { swipeLeft() }
        living.assertIsDisplayed()
        carousel.performTouchInput { swipeLeft() }
        living.assertIsDisplayed() // No extra music page.
        compose.mainClock.advanceTimeBy(31_000)
        mini.assertIsDisplayed()
        living.assertIsDisplayed()
        mini.performClick()
        full.assertIsDisplayed()
        carousel.performTouchInput { swipeLeft() }
        compose.runOnIdle { music.value = false }
        mini.assertDoesNotExist()
        compose.mainClock.advanceTimeBy(31_000)
        kitchen.assertIsDisplayed()
        full.assertDoesNotExist()
        compose.runOnIdle { assertEquals(pages.map { it.path }.sorted(), mounted.sorted()) }
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
