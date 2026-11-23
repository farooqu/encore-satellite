package com.encore.satellite

import android.Manifest
import android.app.role.RoleManager
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.content.pm.PackageManager.PERMISSION_DENIED
import android.content.pm.PackageManager.PERMISSION_GRANTED
import android.os.Process
import android.provider.Settings
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createEmptyComposeRule
import androidx.lifecycle.Lifecycle
import androidx.test.core.app.ActivityScenario
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
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import org.robolectric.shadows.ShadowRoleManager
import java.io.File

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34], qualifiers = "w360dp-h640dp-mdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class AndroidHomeTest {
    @get:Rule val compose = createEmptyComposeRule()

    @OptIn(ExperimentalRoborazziApi::class)
    @Suppress("DEPRECATION") // Deliver the result Android sends to the Activity Result Registry.
    @Test fun microphoneAndNotificationsAreSeparateAndDenialDoesNotRepeatOnRecreation() {
        val app = RuntimeEnvironment.getApplication()
        val android = shadowOf(app)
        android.declareComponentUnbindable(ComponentName(app, EncoreService::class.java))
        android.setUnbindServiceCallsOnServiceDisconnected(false)
        app.getSharedPreferences("setup", Context.MODE_PRIVATE).edit().putString("stage", SetupStage.CORE.name).commit()
        android.denyPermissions(Manifest.permission.RECORD_AUDIO, Manifest.permission.POST_NOTIFICATIONS)
        ActivityScenario.launch(MainActivity::class.java).use { scenario ->
            val microphone = compose.onNodeWithText("Allow microphone access")
            val appPermissions = compose.onNodeWithText("Open app permissions")
            appPermissions.assertDoesNotExist()
            microphone.performClick()
            scenario.onActivity { activity ->
                val request = shadowOf(activity).lastRequestedPermission
                activity.onRequestPermissionsResult(request.requestCode, emptyArray(), intArrayOf())
            }
            appPermissions.assertDoesNotExist()
            microphone.performClick()
            scenario.onActivity { activity ->
                val request = shadowOf(activity).lastRequestedPermission
                assertArrayEquals(arrayOf(Manifest.permission.RECORD_AUDIO), request.requestedPermissions)
                shadowOf(activity.packageManager).setShouldShowRequestPermissionRationale(Manifest.permission.RECORD_AUDIO, true)
                activity.onRequestPermissionsResult(request.requestCode, request.requestedPermissions, intArrayOf(PERMISSION_DENIED))
            }
            microphone.assertIsDisplayed()
            appPermissions.assertDoesNotExist()
            microphone.performClick()
            scenario.onActivity { activity ->
                val request = shadowOf(activity).lastRequestedPermission
                assertArrayEquals(arrayOf(Manifest.permission.RECORD_AUDIO), request.requestedPermissions)
                shadowOf(activity.packageManager).setShouldShowRequestPermissionRationale(Manifest.permission.RECORD_AUDIO, false)
                activity.onRequestPermissionsResult(request.requestCode, request.requestedPermissions, intArrayOf(PERMISSION_DENIED))
            }
            scenario.recreate()
            microphone.assertDoesNotExist()
            appPermissions.assertIsDisplayed()
            val directory = File("build/reports/screenshots").also { it.mkdirs() }
            compose.onAllNodes(isRoot()).onLast().captureRoboImage(File(directory, "android-microphone-settings-fallback.png"),
                RoborazziOptions(taskType = RoborazziTaskType.Record))
            appPermissions.performClick()
            assertEquals(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, android.nextStartedActivity.action)
            scenario.moveToState(Lifecycle.State.STARTED)
            android.grantPermissions(Manifest.permission.RECORD_AUDIO)
            scenario.moveToState(Lifecycle.State.RESUMED)
            scenario.onActivity { assertEquals(PERMISSION_GRANTED, it.checkSelfPermission(Manifest.permission.RECORD_AUDIO)) }
            compose.onNodeWithText("Allow notifications").performClick()
            scenario.onActivity { activity ->
                val request = shadowOf(activity).lastRequestedPermission
                assertArrayEquals(arrayOf(Manifest.permission.POST_NOTIFICATIONS), request.requestedPermissions)
                activity.onRequestPermissionsResult(request.requestCode, request.requestedPermissions, intArrayOf(PERMISSION_DENIED))
            }
            scenario.recreate()
            compose.onNodeWithText("Home Assistant setup").assertIsDisplayed()
            compose.onNodeWithText("Allow notifications").assertDoesNotExist()
        }
    }

    @Test fun androidCanFindTheDashboardAsHomeAndAsAnOrdinaryApp() {
        val app = RuntimeEnvironment.getApplication()
        val dashboard = ComponentName(app, MainActivity::class.java)
        fun candidates(category: String, flags: Int) = app.packageManager.queryIntentActivities(
            Intent(Intent.ACTION_MAIN).addCategory(category).setPackage(app.packageName), flags)
            .map { ComponentName(it.activityInfo.packageName, it.activityInfo.name) }

        assertEquals(listOf(dashboard), candidates(Intent.CATEGORY_HOME, PackageManager.MATCH_DEFAULT_ONLY))
        assertEquals(listOf(dashboard), candidates(Intent.CATEGORY_LAUNCHER, 0))
        assertTrue(candidates(Intent.CATEGORY_LAUNCHER, PackageManager.MATCH_DEFAULT_ONLY).isEmpty())
    }

    @Test fun setupRechecksHomeAfterAndroidSettingsAndRetainsChecklist() {
        val app = RuntimeEnvironment.getApplication()
        val android = shadowOf(app)
        android.declareComponentUnbindable(ComponentName(app, EncoreService::class.java))
        android.setUnbindServiceCallsOnServiceDisconnected(false)
        app.getSharedPreferences("setup", Context.MODE_PRIVATE).edit().putString("stage", SetupStage.EXTRAS.name).commit()
        ActivityScenario.launch(MainActivity::class.java).use { scenario ->
            val selected = compose.onNodeWithText("Encore is your Home app", substring = true)
            selected.assertDoesNotExist()
            compose.onNodeWithText("Choose Home app").performScrollTo().performClick()
            assertEquals(Settings.ACTION_HOME_SETTINGS, android.nextStartedActivity.action)
            scenario.moveToState(Lifecycle.State.STARTED)
            ShadowRoleManager.addRoleHolder(RoleManager.ROLE_HOME, app.packageName, Process.myUserHandle())
            scenario.moveToState(Lifecycle.State.RESUMED)
            compose.onNodeWithText("Finish setup").performScrollTo().assertIsDisplayed()
            selected.performScrollTo().assertIsDisplayed()
        }
    }

    @OptIn(ExperimentalRoborazziApi::class)
    @Test fun settingsLinksDirectlyToHomeAndNotificationControls() {
        val app = RuntimeEnvironment.getApplication()
        val android = shadowOf(app)
        android.declareComponentUnbindable(ComponentName(app, EncoreService::class.java))
        android.setUnbindServiceCallsOnServiceDisconnected(false)
        ActivityScenario.launch(MainActivity::class.java).use {
            fun openSettings() {
                compose.onNodeWithTag("satellite-display").performTouchInput { click(Offset(width * .9f, height * .2f)) }
                compose.onNodeWithTag("open-settings").performClick()
            }
            openSettings()
            val choose = compose.onNodeWithText("Choose Home app")
            val notificationSettings = compose.onNodeWithText("Manage notifications")
            choose.assertIsDisplayed()
            notificationSettings.assertIsDisplayed()
            compose.onNodeWithText("Open Android settings").assertDoesNotExist()
            compose.runOnIdle { assertNull("Opening Encore settings must not start the chooser", android.nextStartedActivity) }
            val directory = File("build/reports/screenshots").also { it.mkdirs() }
            compose.onAllNodes(isRoot()).onLast().captureRoboImage(File(directory, "android-home-recovery.png"),
                RoborazziOptions(taskType = RoborazziTaskType.Record))

            choose.performTouchInput { click() }
            compose.onNodeWithText("Android Home").assertDoesNotExist()
            compose.runOnIdle {
                val intent = android.nextStartedActivity
                assertEquals(Settings.ACTION_HOME_SETTINGS, intent.action)
                assertTrue(intent.flags and Intent.FLAG_ACTIVITY_NEW_TASK != 0)
            }
            openSettings()
            notificationSettings.performTouchInput { click() }
            compose.runOnIdle {
                val intent = android.nextStartedActivity
                assertEquals(Settings.ACTION_APP_NOTIFICATION_SETTINGS, intent.action)
                assertEquals(app.packageName, intent.getStringExtra(Settings.EXTRA_APP_PACKAGE))
            }
        }
    }
}
