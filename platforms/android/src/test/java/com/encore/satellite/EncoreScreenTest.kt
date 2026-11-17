package com.encore.satellite

import androidx.compose.foundation.layout.*
import androidx.compose.material3.Button
import androidx.compose.material3.Text
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.testTag
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
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import java.io.File
import kotlin.math.sin

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], qualifiers = "w1280dp-h800dp-mdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class EncoreScreenTest {
    @get:Rule val compose = createComposeRule()

    @Test fun voiceBlocksDashboardButNotStopOrPrivacy() {
        var dashboardTaps = 0
        var stops = 0
        var privacy = false
        val state = mutableStateOf(EncoreState(running = true, microphoneReady = true, connected = true, models = 2))
        compose.setContent {
            EncoreScreen(state.value, {}, {}, { stops++; state.value = state.value.copy(phase = "idle", voiceActive = false) },
                { privacy = it }, { "" }, { "" }, {}, {}, {}, {}, dashboardContent = {
                    Box(Modifier.fillMaxSize().padding(48.dp)) {
                        Button(onClick = { dashboardTaps++ }, modifier = Modifier.testTag("dashboard-tile")) { Text("Dashboard action") }
                    }
                })
        }
        compose.onNodeWithTag("dashboard-tile").performTouchInput { click() }
        assertEquals(1, dashboardTaps)
        compose.runOnIdle { state.value = state.value.copy(phase = "listening", voiceActive = true, heard = "Test transcript: switch on the lights") }
        compose.onNodeWithText("Test transcript: switch on the lights").assertIsDisplayed()
        compose.onNodeWithTag("dashboard-shield").assertExists()
        compose.onNodeWithTag("dashboard-tile").performTouchInput { click() }
        assertEquals("Dashboard consumed a touch while dimmed", 1, dashboardTaps)
        compose.onNodeWithTag("privacy").performClick()
        assertTrue(privacy)
        compose.onNodeWithContentDescription("Stop voice turn").performClick()
        assertEquals(1, stops)
        compose.onNodeWithTag("dashboard-shield").assertDoesNotExist()
        compose.onNodeWithTag("dashboard-tile").performTouchInput { click() }
        assertEquals(2, dashboardTaps)
    }

    @Test fun renderNativeDisplayStatesAndRespectMusicCapabilities() {
        val state = mutableStateOf(EncoreState())
        var musicCommand = ""
        compose.setContent {
            EncoreScreen(state.value, {}, {}, {}, {}, { "" }, { "" }, {}, {}, { musicCommand = it }, {})
        }
        capture("android-onboarding")
        compose.onNodeWithText("Settings").performClick()
        compose.onNodeWithText("Trusted HA origin").assertDoesNotExist()
        compose.onNodeWithText("Save trusted origin").assertDoesNotExist()
        compose.onNodeWithText("Set one full Dashboard URL", substring = true).assertIsDisplayed()
        capture("android-settings")
        compose.onNodeWithText("Done").performClick()
        compose.runOnIdle {
            state.value = EncoreState(running = true, microphoneReady = true, connected = true, models = 2, phase = "listening", voiceActive = true,
                heard = "Test transcript: turn on the kitchen lights", level = .6f,
                waveform = List(64) { (sin(it * .6) * .7 * sin(it * .11)).toFloat() },
                music = MusicState(true, "Connected", "Test audio · sample track", "UI test fixture", true, listOf("pause")))
        }
        compose.onNodeWithContentDescription("Live microphone waveform").assertExists()
        compose.onNodeWithText("Next").assertIsNotEnabled()
        compose.onNodeWithText("Pause").performClick()
        assertEquals("pause", musicCommand)
        capture("android-listening")
        compose.runOnIdle { state.value = state.value.copy(phase = "speaking", reply = "Test reply: the kitchen lights are on.") }
        compose.onNodeWithContentDescription("Microphone waveform inactive").assertExists()
        compose.onNodeWithText("Test reply: the kitchen lights are on.").assertIsDisplayed()
        capture("android-responding")
        compose.runOnIdle { state.value = state.value.copy(phase = "muted", voiceActive = false, muted = true, connected = false, models = 0, heard = "", reply = "") }
        compose.onNodeWithTag("dashboard-shield").assertDoesNotExist()
        compose.onNodeWithText("Unmute mic").assertIsDisplayed()
        capture("android-muted")
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
        compose.onNodeWithText("MICROPHONE OFF").assertIsDisplayed()
        compose.onNodeWithText("Talk").assertDoesNotExist()
        compose.onNodeWithText("Mute mic").assertDoesNotExist()
        capture("android-bluetooth-first")
        compose.onNodeWithText("Start microphone").performClick()
        assertEquals(1, starts)
        assertEquals(0, talks)
        assertNull("Bluetooth-only startup must not change saved mute intent", muteChoice)
        // Requesting permission is not successful capture: keep the action until ready.
        compose.onNodeWithText("Start microphone").assertIsDisplayed()
        compose.runOnIdle { state.value = state.value.copy(microphoneReady = true) }
        compose.onNodeWithText("Start microphone").assertDoesNotExist()
        compose.onNodeWithText("Talk").assertIsEnabled().performClick()
        compose.onNodeWithText("Mute mic").assertIsDisplayed()
        assertEquals(1, talks)
        capture("android-bluetooth-microphone-ready")
        compose.runOnIdle { state.value = state.value.copy(microphoneReady = false, muted = true) }
        compose.onNodeWithText("Talk").assertIsNotEnabled()
        compose.onNodeWithText("Unmute mic").performClick()
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
        compose.onNodeWithText("Announcement").assertIsDisplayed()
        compose.onNodeWithTag("dashboard-shield").assertExists()
        compose.onNodeWithText("Talk").assertIsNotEnabled()
        compose.onNodeWithText("Settings").assertIsNotEnabled()
        compose.onNodeWithText("Quiet for voice").assertIsDisplayed()
        compose.onNodeWithTag("dashboard-tile").performTouchInput { click() }
        assertEquals(0, taps)
        compose.onNodeWithTag("privacy").assertIsEnabled()
        compose.onNodeWithTag("stop").performClick()
        assertEquals(1, stops)
        capture("android-announcement")

        compose.runOnIdle { state.value = EncoreState.parse(EncoreStateTest.snapshot(announcing = false, output = "draining"), state.value) }
        compose.onNodeWithText("Announcement").assertIsDisplayed()
        compose.onNodeWithTag("dashboard-shield").assertExists()
        compose.onNodeWithText("Talk").assertIsNotEnabled()
        capture("android-announcement-draining")

        compose.runOnIdle { state.value = EncoreState.parse(EncoreStateTest.snapshot(active = false, announcing = false, output = "", owner = ""), state.value) }
        compose.onNodeWithTag("dashboard-shield").assertDoesNotExist()
        compose.onNodeWithText("Talk").assertIsNotEnabled()
        compose.onNodeWithText("Settings").assertIsEnabled()
        compose.runOnIdle { state.value = EncoreState.parse(EncoreStateTest.snapshot(active = false, announcing = false, output = "", owner = "", talkAllowed = true), state.value) }
        compose.onNodeWithText("Talk").assertIsEnabled()
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
    private fun capture(name: String) {
        compose.waitForIdle()
        val directory = File("build/reports/screenshots").also { it.mkdirs() }
        // Compose captureToImage's forced redraw deadlocks Robolectric's main looper.
        // Roborazzi captures the real native graphics hierarchy without that device-only path.
        compose.onAllNodes(isRoot()).onLast().captureRoboImage(File(directory, "$name.png"),
            RoborazziOptions(taskType = RoborazziTaskType.Record))
    }
}
