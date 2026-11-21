package com.encore.satellite

import android.content.ComponentName
import android.content.Intent
import android.content.pm.PackageManager
import android.provider.Settings
import androidx.compose.ui.test.*
import androidx.compose.ui.test.junit4.createEmptyComposeRule
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
import java.io.File

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [34], qualifiers = "w360dp-h640dp-mdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class AndroidHomeTest {
    @get:Rule val compose = createEmptyComposeRule()

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

    @OptIn(ExperimentalRoborazziApi::class)
    @Test fun settingsOpensBothSystemRecoveryPathsWithoutChoosingHomeOnItsOwn() {
        val app = RuntimeEnvironment.getApplication()
        val android = shadowOf(app)
        android.declareComponentUnbindable(ComponentName(app, EncoreService::class.java))
        android.setUnbindServiceCallsOnServiceDisconnected(false)
        ActivityScenario.launch(MainActivity::class.java).use {
            val gear = compose.onNodeWithTag("open-settings")
            gear.performClick()
            val choose = compose.onNodeWithText("Choose Home app")
            val systemSettings = compose.onNodeWithText("Open Android settings")
            choose.assertIsDisplayed()
            systemSettings.assertIsDisplayed()
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
            gear.performClick()
            systemSettings.performTouchInput { click() }
            compose.runOnIdle { assertEquals(Settings.ACTION_SETTINGS, android.nextStartedActivity.action) }
        }
    }
}
