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
        val state = mutableStateOf(EncoreState(running = true, connected = true, models = 2))
        compose.setContent {
            EncoreScreen(state.value, {}, {}, { stops++; state.value = state.value.copy(phase = "idle") },
                { privacy = it }, { "" }, { "" }, {}, {}, {}, {}, dashboardContent = {
                    Box(Modifier.fillMaxSize().padding(48.dp)) {
                        Button(onClick = { dashboardTaps++ }, modifier = Modifier.testTag("dashboard-tile")) { Text("Dashboard action") }
                    }
                })
        }
        compose.onNodeWithTag("dashboard-tile").performTouchInput { click() }
        assertEquals(1, dashboardTaps)
        compose.runOnIdle { state.value = state.value.copy(phase = "listening", heard = "Test transcript: switch on the lights") }
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
            state.value = EncoreState(running = true, connected = true, models = 2, phase = "listening",
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
        compose.runOnIdle { state.value = state.value.copy(phase = "muted", muted = true, connected = false, models = 0, heard = "", reply = "") }
        compose.onNodeWithTag("dashboard-shield").assertDoesNotExist()
        compose.onNodeWithText("Unmute mic").assertIsDisplayed()
        capture("android-muted")
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
