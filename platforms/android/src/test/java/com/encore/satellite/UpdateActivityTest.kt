package com.encore.satellite

import android.app.AlertDialog
import android.app.Activity
import android.content.Context
import android.content.Intent
import android.content.pm.ApplicationInfo
import android.content.pm.PackageInfo
import android.content.pm.PackageManager
import android.content.pm.Signature
import android.graphics.Bitmap
import android.os.Bundle
import android.provider.Settings
import androidx.core.view.drawToBitmap
import org.json.JSONObject
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
import org.robolectric.shadows.ShadowToast
import java.io.File
import java.security.MessageDigest

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], qualifiers = "w1280dp-h800dp-mdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class UpdateActivityTest {
    @Test fun remoteEnrollmentRequiresLocalConsentAndAllowedInstallSource() {
        val context = RuntimeEnvironment.getApplication()
        val pm = shadowOf(context.packageManager)
        val install = ApkInstall(context)
        install.preferences.edit().clear().commit()
        pm.setCanRequestPackageInstalls(false)
        val direct = Intent(context, UpdateActivity::class.java).putExtra(UpdateActivity.REMOTE_SETTINGS, true)
        val denied = Robolectric.buildActivity(UpdateActivity::class.java, direct).setup()
        val deniedShadow = shadowOf(denied.get())
        ShadowAlertDialog.getLatestAlertDialog().getButton(AlertDialog.BUTTON_POSITIVE).performClick()
        ShadowLooper.idleMainLooper()
        val deniedSettings = deniedShadow.nextStartedActivityForResult.intent
        assertEquals(deniedSettings, deniedShadow.nextStartedActivity)
        deniedShadow.receiveResult(deniedSettings, Activity.RESULT_OK, null) // A positive result does not grant access.
        assertFalse(install.optedIn)
        denied.pause().stop().destroy()

        val controller = Robolectric.buildActivity(UpdateActivity::class.java, direct).setup()
        val activity = shadowOf(controller.get())
        ShadowLooper.idleMainLooper()
        capture("android-update-remote-opt-in")
        assertFalse(install.optedIn)
        assertNull(activity.nextStartedActivity)
        ShadowAlertDialog.getLatestAlertDialog().getButton(AlertDialog.BUTTON_POSITIVE).performClick()
        ShadowLooper.idleMainLooper()
        val settings = activity.nextStartedActivityForResult.intent
        assertEquals(settings, activity.nextStartedActivity) // The shadow also records this launch in the general queue.
        assertEquals(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, settings.action)
        assertEquals("package:${context.packageName}", settings.data.toString())
        assertFalse("Opt-in is not saved before Android source approval", install.optedIn)
        pm.setCanRequestPackageInstalls(true)
        activity.receiveResult(settings, Activity.RESULT_CANCELED, null) // Settings' result code is not authoritative.
        ShadowLooper.idleMainLooper()
        assertTrue(install.canInstall)
        capture("android-update-remote-enabled")
        assertEquals("Enrollment must not replay a pending install", 0, context.packageManager.packageInstaller.mySessions.size)
        assertNull(activity.nextStartedActivity)
        controller.pause().stop().destroy()

        val reopen = Robolectric.buildActivity(UpdateActivity::class.java).setup()
        ShadowAlertDialog.getLatestAlertDialog().getButton(AlertDialog.BUTTON_NEUTRAL).performClick()
        ShadowLooper.idleMainLooper()
        capture("android-update-remote-disable")
        ShadowAlertDialog.getLatestAlertDialog().getButton(AlertDialog.BUTTON_POSITIVE).performClick()
        ShadowLooper.idleMainLooper()
        assertFalse(install.optedIn)
        reopen.pause().stop().destroy()
    }

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
        assertNull(shadowOf(changed.get()).nextStartedActivity)
        capture("android-update-rejected")
        rejected.getButton(AlertDialog.BUTTON_POSITIVE).performClick()
        ShadowLooper.idleMainLooper()
        changed.pause().stop().destroy()
    }

    @Suppress("DEPRECATION")
    @Test @Config(sdk = [27]) fun confirmedUpdateGrantsOnlyItsApkAndCanBeRetriedAfterHandoff() {
        val context = RuntimeEnvironment.getApplication()
        val pm = shadowOf(context.packageManager)
        val signature = Signature("abcd")
        // Preserve the actual manifest provider when replacing the installed package's signing info.
        val installed = context.packageManager.getPackageInfo(context.packageName, PackageManager.GET_PROVIDERS or PackageManager.GET_META_DATA).apply {
            versionCode = 19
            signatures = arrayOf(signature)
        }
        pm.installPackage(installed)
        pm.setCanRequestPackageInstalls(true)
        context.filesDir.resolve("engine").mkdirs()
        // Earlier builds persisted an empty string for the default Dev selection.
        context.filesDir.resolve("engine/encore.json").writeText("""{"channel":"","channel_generation":7}""")
        val bytes = "verified fixture APK".toByteArray()
        fun sha256(value: ByteArray) = MessageDigest.getInstance("SHA-256").digest(value).joinToString("") { "%02x".format(it) }
        val digest = sha256(bytes)
        val file = context.filesDir.resolve("updates/apks/$digest.apk").apply { parentFile!!.mkdirs(); writeBytes(bytes) }
        pm.setPackageArchiveInfo(file.absolutePath, PackageInfo().apply {
            packageName = context.packageName
            versionCode = 23
            versionName = "1.2.4"
            signatures = arrayOf(signature)
            applicationInfo = ApplicationInfo().apply { metaData = Bundle().apply { putString("com.encore.core_version", "1.2.4") } }
        })
        val request = JSONObject().put("application_id", context.packageName).put("revision", 23).put("version", "1.2.4")
            .put("core_version", "1.2.4").put("installed_core_version", BuildConfig.CORE_VERSION).put("certificate_sha256", sha256(signature.toByteArray()))
            .put("target", "android-generic").put("backend", "incubator").put("channel", "dev").put("selection_generation", 7)
            .put("size", bytes.size).put("sha256", digest).put("expires", System.currentTimeMillis() / 1000 + 3600).toString()
        val preferences = context.getSharedPreferences("updates", Context.MODE_PRIVATE)
        preferences.edit().putString("pending", request).commit()
        repeat(2) {
            val controller = Robolectric.buildActivity(UpdateActivity::class.java).setup()
            val activity = shadowOf(controller.get())
            assertNull(activity.nextStartedActivity)
            ShadowAlertDialog.getLatestAlertDialog().getButton(AlertDialog.BUTTON_POSITIVE).performClick()
            var intent: Intent? = null
            for (attempt in 1..300) {
                ShadowLooper.idleMainLooper()
                intent = activity.nextStartedActivity
                if (intent != null) break
                Thread.sleep(10)
            }
            val installer = checkNotNull(intent) {
                "No Android installer handoff: ${ShadowToast.getTextOfLatestToast() ?: shadowOf(ShadowAlertDialog.getLatestAlertDialog()).message}"
            }
            assertEquals(Intent.ACTION_INSTALL_PACKAGE, installer.action)
            assertEquals("content://${context.packageName}.updates/apk/$digest.apk", installer.data.toString())
            assertEquals(Intent.FLAG_GRANT_READ_URI_PERMISSION,
                installer.flags and (Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_GRANT_WRITE_URI_PERMISSION))
            assertTrue(installer.getBooleanExtra(Intent.EXTRA_RETURN_RESULT, false))
            assertEquals(request, preferences.getString("pending", null))
            activity.receiveResult(installer, Activity.RESULT_CANCELED, null)
            ShadowLooper.idleMainLooper()
            assertTrue(ApkInstall(context).attempt!!.getString("status").startsWith("Android installation cancelled"))
            controller.pause().stop().destroy()
        }
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
