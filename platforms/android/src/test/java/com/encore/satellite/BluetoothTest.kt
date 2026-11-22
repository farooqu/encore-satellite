package com.encore.satellite

import android.Manifest
import android.app.Application
import android.content.Context
import android.content.pm.PackageManager
import androidx.compose.runtime.mutableStateOf
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createComposeRule
import androidx.test.core.app.ApplicationProvider
import com.github.takahirom.roborazzi.*
import org.junit.Assert.*
import org.junit.Rule
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.RobolectricTestRunner
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import java.io.File

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], qualifiers = "w1280dp-h800dp-mdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class BluetoothTest {
    @get:Rule val compose = createComposeRule()

    private fun state(sdk: Int = 35, enabled: Boolean = false, permission: Boolean = true,
        radio: Boolean = true, visible: Boolean = true, scanning: Boolean = false, received: Long = 0,
        failure: String = "") = BluetoothState.evaluate(sdk, true, enabled, permission, radio,
            true, visible, true, scanning, received, failure)

    @Test @Config(sdk = [34]) fun api34SupportsScanOnlyAndRequestsNearbyDevicesAndLocation() {
        val context = ApplicationProvider.getApplicationContext<Application>()
        shadowOf(context.packageManager).setSystemFeature(PackageManager.FEATURE_BLUETOOTH_LE, true)
        val host = BluetoothHost(context)
        assertEquals(setOf(Manifest.permission.BLUETOOTH_SCAN, Manifest.permission.BLUETOOTH_CONNECT,
            Manifest.permission.ACCESS_COARSE_LOCATION, Manifest.permission.ACCESS_FINE_LOCATION), host.permissions().toSet())
        val android = shadowOf(context)
        android.denyPermissions(Manifest.permission.ACCESS_FINE_LOCATION)
        android.grantPermissions(Manifest.permission.BLUETOOTH_SCAN, Manifest.permission.BLUETOOTH_CONNECT, Manifest.permission.ACCESS_COARSE_LOCATION)
        host.refresh()
        assertFalse("Approximate location must not satisfy scan permission", host.state.value.permitted)
        android.grantPermissions(Manifest.permission.ACCESS_FINE_LOCATION)
        host.refresh()
        assertTrue(host.state.value.permitted)
        val tablet = state(sdk = 34, enabled = true, scanning = true, received = 12)
        assertTrue(tablet.supported)
        assertEquals("Receiving advertisements", tablet.status)
        assertTrue(tablet.detail.contains("explicitly unknown"))
    }

    @Test @Config(sdk = [29]) fun olderAndroidNeedsLocationNotNearbyDeviceRuntimeGrants() {
        val context = ApplicationProvider.getApplicationContext<Context>()
        shadowOf(context.packageManager).setSystemFeature(PackageManager.FEATURE_BLUETOOTH_LE, true)
        assertArrayEquals(arrayOf(Manifest.permission.ACCESS_FINE_LOCATION), BluetoothHost(context).permissions())
        assertTrue(state(sdk = 29).supported)
    }

    @Test fun grantsAreNotReadinessAndLossDoesNotChangeCapabilitiesOrIntent() {
        val context = ApplicationProvider.getApplicationContext<Context>()
        shadowOf(context.packageManager).setSystemFeature(PackageManager.FEATURE_BLUETOOTH_LE, true)
        assertTrue(BluetoothHost(context).permissions().contains(Manifest.permission.ACCESS_FINE_LOCATION))
        assertEquals("Waiting for encrypted controller", state(enabled = true).status)
        assertEquals("Permission needed", state(enabled = true, permission = false).status)
        assertEquals("Bluetooth is off", state(enabled = true, radio = false).status)
        assertEquals("Paused in background", state(enabled = true, visible = false).status)
        assertEquals("Scan requested; awaiting advertisements", state(enabled = true, scanning = true).status)
        assertEquals("Receiving advertisements", state(enabled = true, scanning = true, received = 3).status)
        for (sample in listOf(state(enabled = true, permission = false), state(enabled = true, radio = false),
            state(enabled = true, visible = false), state(enabled = true, failure = "Scan failed"))) {
            assertTrue(sample.supported)
            assertTrue(sample.enabled)
        }
        val noHardware = BluetoothState.evaluate(35, false, true, true, true, true, true, true, true, 5, "")
        assertFalse(noHardware.supported)
    }

    @Test fun hostWithoutBLEReportsUnsupportedAndRequestsNoPermissions() {
        val context = ApplicationProvider.getApplicationContext<Context>()
        shadowOf(context.packageManager).setSystemFeature(PackageManager.FEATURE_BLUETOOTH_LE, false)
        val host = BluetoothHost(context)
        host.refresh()
        assertFalse(host.state.value.supported)
        assertEquals("Unavailable on this device", host.state.value.status)
        assertTrue(host.permissions().isEmpty())
    }

    @OptIn(ExperimentalRoborazziApi::class)
    @Test @Config(sdk = [34]) fun firstRunDenialDefersWithoutTurningOffAndReentryCanApprove() {
        val sample = mutableStateOf(BluetoothState.evaluate(34, true, true, false,
            false, false, true, true, false, 0, "", setupPending = true))
        var requests = 0
        val promptBlocked = mutableStateOf(false)
        var settingsOpened = false
        var approved = false
        var deferred = false
        compose.setContent {
            BluetoothFirstRun(sample.value, "", onRequest = { requests++ },
                onPermissionRecovery = if (promptBlocked.value) { { settingsOpened = true } } else null, onRadio = {}, onLocation = {},
                onApprove = { approved = true }, onBack = { deferred = true },
                onEnabled = { sample.value = sample.value.copy(enabled = it) })
        }
        compose.onNodeWithText("encrypted Home Assistant ESPHome controller", substring = true).assertIsDisplayed()
        val request = compose.onNodeWithText("Allow Bluetooth and location access")
        val recovery = compose.onNodeWithText("Open app permissions")
        recovery.assertDoesNotExist()
        request.performClick()
        compose.onNodeWithText("On · Permission needed").assertIsDisplayed()
        compose.onNodeWithText("Allow scanning").assertDoesNotExist()
        val directory = File("build/reports/screenshots").also { it.mkdirs() }
        compose.onAllNodes(isRoot()).onLast().captureRoboImage(File(directory, "android-first-run-bluetooth-denied.png"),
            RoborazziOptions(taskType = RoborazziTaskType.Record))
        compose.runOnIdle { promptBlocked.value = true }
        request.assertDoesNotExist()
        recovery.assertIsDisplayed().performClick()
        compose.runOnIdle { assertTrue(settingsOpened) }
        compose.onAllNodes(isRoot()).onLast().captureRoboImage(File(directory, "android-first-run-bluetooth-settings-fallback.png"),
            RoborazziOptions(taskType = RoborazziTaskType.Record))
        compose.onNodeWithText("Not now").performClick()
        compose.runOnIdle {
            assertTrue(deferred)
            assertTrue(sample.value.enabled)
            assertFalse(approved)
            sample.value = sample.value.copy(permitted = true)
        }
        compose.onNodeWithText("Turn on Bluetooth in Android").assertIsDisplayed()
        compose.onNodeWithText("Turn on Location services in Android").assertDoesNotExist()
        compose.runOnIdle { sample.value = sample.value.copy(radioOn = true, status = "Location services needed for beacon coverage") }
        compose.onNodeWithText("Turn on Bluetooth in Android").assertDoesNotExist()
        compose.onNodeWithText("Turn on Location services in Android").assertIsDisplayed()
        compose.onNodeWithText("Allow scanning").assertDoesNotExist()
        compose.onAllNodes(isRoot()).onLast().captureRoboImage(File(directory, "android-first-run-bluetooth-location-off.png"),
            RoborazziOptions(taskType = RoborazziTaskType.Record))
        compose.runOnIdle { sample.value = sample.value.copy(radioOn = true, locationOn = true, status = "Finish Bluetooth setup to start scanning") }
        compose.onNodeWithText("Allow scanning").assertIsDisplayed()
        compose.onAllNodes(isRoot()).onLast().captureRoboImage(File(directory, "android-first-run-bluetooth-ready.png"),
            RoborazziOptions(taskType = RoborazziTaskType.Record))
        compose.runOnIdle { assertFalse(approved); assertEquals(1, requests) }
        compose.onNodeWithText("Allow scanning").performClick()
        compose.runOnIdle { assertTrue(approved) }
        compose.onNodeWithText("Turn off proxy").performClick()
        compose.onNodeWithText("Off · Not scanning").assertIsDisplayed()
    }

    @OptIn(ExperimentalRoborazziApi::class)
    @Test fun renderCapabilitiesAndKeepUnsupportedEnableDisabled() {
        val sample = mutableStateOf(state(sdk = 34))
        var enabled = false
        compose.setContent { BluetoothSettings(sample.value, onEnabled = { enabled = it }, onRetry = {},
            onRequest = {}, onPermissionRecovery = null, onRadio = {}, onLocation = {}, onBack = {}) }
        val samples = listOf(
            "android-bluetooth-api34" to state(sdk = 34),
            "android-bluetooth-api29" to state(sdk = 29, enabled = true, permission = false),
            "android-bluetooth-disabled" to state(),
            "android-bluetooth-permission" to state(enabled = true, permission = false),
            "android-bluetooth-background" to state(enabled = true, visible = false),
            "android-bluetooth-receiving" to state(sdk = 34, enabled = true, scanning = true, received = 17),
            "android-bluetooth-no-hardware" to BluetoothState.evaluate(34, false, false, false, false, false, true, true, false, 0, ""),
        )
        for ((name, value) in samples) {
            compose.runOnIdle { sample.value = value }
            compose.onNodeWithText(value.status).assertIsDisplayed()
            if (!value.supported) {
                compose.onNode(isToggleable()).assertIsNotEnabled()
                compose.onNodeWithText("Open app permissions").assertDoesNotExist()
                compose.onNodeWithText("Allow Bluetooth and location access").assertDoesNotExist()
            }
            compose.waitForIdle()
            val directory = File("build/reports/screenshots").also { it.mkdirs() }
            compose.onAllNodes(isRoot()).onLast().captureRoboImage(File(directory, "$name.png"),
                RoborazziOptions(taskType = RoborazziTaskType.Record))
        }
        compose.runOnIdle { sample.value = state() }
        compose.onNode(isToggleable()).performClick()
        assertTrue(enabled)
        compose.runOnIdle { sample.value = state(enabled = true) }
        compose.onNode(isToggleable()).performClick()
        assertFalse(enabled)
    }
}
