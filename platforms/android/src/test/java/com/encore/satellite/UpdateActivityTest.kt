package com.encore.satellite

import android.app.AlertDialog
import android.content.Context
import android.graphics.Bitmap
import android.provider.Settings
import androidx.core.view.drawToBitmap
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.Shadows.shadowOf
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import org.robolectric.shadows.ShadowAlertDialog
import org.robolectric.shadows.ShadowLooper
import java.io.File

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], qualifiers = "w1280dp-h800dp-mdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class UpdateActivityTest {
    @Test fun updateRequiresAnExplicitLocalActionAndInstallPermission() {
        val context = RuntimeEnvironment.getApplication()
        val preferences = context.getSharedPreferences("updates", Context.MODE_PRIVATE)
        preferences.edit().clear().commit()
        val empty = Robolectric.buildActivity(UpdateActivity::class.java).setup()
        assertNotNull(ShadowAlertDialog.getLatestAlertDialog())
        assertNull(shadowOf(empty.get()).nextStartedActivity)
        capture("android-update-empty")
        ShadowAlertDialog.getLatestAlertDialog().getButton(AlertDialog.BUTTON_POSITIVE).performClick()
        ShadowLooper.idleMainLooper()
        empty.pause().stop().destroy()

        preferences.edit().putString("pending", """{"version":"0.2.0-incubator","channel":"stable"}""").commit()
        shadowOf(context.packageManager).setCanRequestPackageInstalls(false)
        val blocked = Robolectric.buildActivity(UpdateActivity::class.java).setup()
        assertNull("Remote download must not open settings or installer", shadowOf(blocked.get()).nextStartedActivity)
        capture("android-update-permission")
        ShadowAlertDialog.getLatestAlertDialog().getButton(AlertDialog.BUTTON_POSITIVE).performClick()
        ShadowLooper.idleMainLooper()
        assertEquals(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, shadowOf(blocked.get()).nextStartedActivity.action)
        blocked.pause().stop().destroy()

        shadowOf(context.packageManager).setCanRequestPackageInstalls(true)
        val allowed = Robolectric.buildActivity(UpdateActivity::class.java).setup()
        assertNull(shadowOf(allowed.get()).nextStartedActivity)
        capture("android-update-confirm")
        ShadowAlertDialog.getLatestAlertDialog().getButton(AlertDialog.BUTTON_NEGATIVE).performClick()
        ShadowLooper.idleMainLooper()
        assertTrue("Later must leave the pending request retryable", preferences.contains("pending"))
        assertNull("Later must not launch installation", shadowOf(allowed.get()).nextStartedActivity)
        allowed.pause().stop().destroy()

        // Another app replacement may raise the installed core while an old
        // request survives in private data. It needs fresh eligibility approval.
        preferences.edit().putString("pending", """{"version":"0.2.0-incubator","channel":"stable","installed_core_version":"0.0.0"}""").commit()
        val changed = Robolectric.buildActivity(UpdateActivity::class.java).setup()
        val confirmation = ShadowAlertDialog.getLatestAlertDialog()
        confirmation.getButton(AlertDialog.BUTTON_POSITIVE).performClick()
        repeat(100) {
            if (ShadowAlertDialog.getLatestAlertDialog() === confirmation) Thread.sleep(10)
            ShadowLooper.idleMainLooper()
        }
        val rejected = ShadowAlertDialog.getLatestAlertDialog()
        assertEquals("Update rejected", shadowOf(rejected).title)
        assertEquals("Installed core changed; check again", shadowOf(rejected).message)
        assertFalse(preferences.contains("pending"))
        assertNull(shadowOf(changed.get()).nextStartedActivity)
        capture("android-update-rejected")
        rejected.getButton(AlertDialog.BUTTON_POSITIVE).performClick()
        ShadowLooper.idleMainLooper()
        changed.pause().stop().destroy()
    }

    private fun capture(name: String) {
        val directory = File("build/reports/screenshots").also { it.mkdirs() }
        // Platform dialogs aren't in Espresso's activity root hierarchy here.
        // Draw the laid-out native dialog itself, not a synthetic replacement.
        File(directory, "$name.png").outputStream().use {
            ShadowAlertDialog.getLatestAlertDialog().window!!.decorView.drawToBitmap()
                .compress(Bitmap.CompressFormat.PNG, 100, it)
        }
    }
}
