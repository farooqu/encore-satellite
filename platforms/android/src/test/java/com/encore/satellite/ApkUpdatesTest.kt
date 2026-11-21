package com.encore.satellite

import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.ApplicationInfo
import android.content.pm.PackageInfo
import android.content.pm.PackageInstaller
import android.content.pm.PackageManager
import android.content.pm.Signature
import android.content.pm.SigningInfo
import android.net.Uri
import android.os.Bundle
import com.encore.engine.mobile.Mobile
import com.encore.engine.mobile.Runtime
import com.encore.engine.mobile.UpdateHost
import com.encore.engine.mobile.Updates
import kotlinx.coroutines.*
import org.junit.Assert.*
import org.junit.Test
import org.junit.runner.RunWith
import org.json.JSONObject
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.Implementation
import org.robolectric.annotation.Implements
import org.robolectric.shadow.api.Shadow
import org.robolectric.Shadows.shadowOf
import org.robolectric.shadows.ShadowLooper
import java.security.MessageDigest
import java.util.concurrent.CopyOnWriteArrayList
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger

/** Real coroutine/lifecycle code, with only the native JNI boundary shadowed. */
@RunWith(RobolectricTestRunner::class)
@Config(sdk = [27], instrumentedPackages = ["com.encore.engine.mobile"],
    shadows = [UpdateMobileShadow::class, UpdateRuntimeShadow::class, UpdateFetchShadow::class])
class ApkUpdatesTest {
    @Test fun stoppedWorkerRejectsLateResultsAndCanRestart() = exerciseStop(false)
    @Test fun parentCancellationClosesSynchronousFetchAndRejectsLateResults() = exerciseStop(true)

    @Test @Config(sdk = [34]) fun commitsOnlyEligibleExplicitInstallsAndTracksAndroidResults() {
        val context = RuntimeEnvironment.getApplication()
        val pm = shadowOf(context.packageManager)
        val sessions = context.packageManager.packageInstaller
        val signature = Signature("abcd")
        fun signing(signature: Signature) = SigningInfo().also { shadowOf(it).setSignatures(arrayOf(signature)) }
        val installed = context.packageManager.getPackageInfo(context.packageName, PackageManager.GET_META_DATA).apply {
            setLongVersionCode(19)
            signingInfo = signing(signature)
        }
        pm.installPackage(installed)
        context.filesDir.resolve("engine").mkdirs()
        context.filesDir.resolve("engine/encore.json").writeText("""{"channel":"dev","channel_generation":4}""")
        fun sha256(bytes: ByteArray) = MessageDigest.getInstance("SHA-256").digest(bytes).joinToString("") { "%02x".format(it) }
        val bytes = "verified fixture APK".toByteArray()
        val digest = sha256(bytes)
        val file = context.filesDir.resolve("updates/apks/$digest.apk").apply { parentFile!!.mkdirs(); writeBytes(bytes) }
        val archive = PackageInfo().apply {
            packageName = context.packageName
            setLongVersionCode(23)
            versionName = "1.2.4"
            signingInfo = signing(signature)
            applicationInfo = ApplicationInfo().apply { metaData = Bundle().apply { putString("com.encore.core_version", "1.2.4") } }
        }
        pm.setPackageArchiveInfo(file.absolutePath, archive)
        val release = JSONObject().put("application_id", context.packageName).put("revision", 23).put("version", "1.2.4")
            .put("core_version", "1.2.4").put("certificate_sha256", sha256(signature.toByteArray()))
            .put("target", "android-generic").put("backend", "incubator").put("channel", "dev")
            .put("size", bytes.size).put("sha256", digest).put("expires", System.currentTimeMillis() / 1000 + 3600)
        val install = ApkInstall(context)
        val parent = SupervisorJob()
        val runtime = Shadow.newInstanceOf(Runtime::class.java)
        UpdateMobileShadow.attempt = UpdateAttempt().also {
            it.json = JSONObject().put("release", release).toString()
            it.release.countDown()
        }
        UpdateRuntimeShadow.statuses.clear()
        val updater = ApkUpdates(context, runtime, CoroutineScope(parent + Dispatchers.Unconfined))
        fun awaitStatus(prefix: String) {
            repeat(300) {
                ShadowLooper.idleMainLooper()
                if (UpdateRuntimeShadow.statuses.lastOrNull()?.startsWith(prefix) == true) return
                Thread.sleep(10)
            }
            fail("Expected $prefix; got ${UpdateRuntimeShadow.statuses}")
        }
        fun request() {
            UpdateRuntimeShadow.statuses.clear()
            updater.requestUpdate("dev", 4, true)
        }
        fun deliver(id: Int, reportedId: Int, status: Int) {
            val callback = PendingIntent.getBroadcast(context, id, Intent(context, ApkInstallReceiver::class.java)
                .setData(Uri.parse("encore-install:$id")), PendingIntent.FLAG_NO_CREATE or PendingIntent.FLAG_MUTABLE)
            assertNotNull("Session must persist an explicit callback", callback)
            callback!!.send(context, 0, Intent().putExtra(PackageInstaller.EXTRA_SESSION_ID, reportedId)
                .putExtra(PackageInstaller.EXTRA_STATUS, status).putExtra(PackageInstaller.EXTRA_STATUS_MESSAGE, "test installer failure"))
            ShadowLooper.idleMainLooper()
        }
        try {
            pm.setCanRequestPackageInstalls(true)
            updater.start()
            awaitStatus("Update available")
            assertTrue(sessions.mySessions.isEmpty())
            request() // Android source approval alone is not opt-in.
            awaitStatus("Downloaded and verified")
            assertTrue(sessions.mySessions.isEmpty())
            install.preferences.edit().putBoolean("remote_install", true).commit()
            pm.setCanRequestPackageInstalls(false)
            request()
            awaitStatus("Downloaded and verified")
            assertTrue(sessions.mySessions.isEmpty())
            pm.setCanRequestPackageInstalls(true)
            UpdateRuntimeShadow.statuses.clear()
            updater.requestUpdate("dev", 4, false)
            awaitStatus("Downloaded and verified")
            assertTrue("Even an eligible check must not start a session", sessions.mySessions.isEmpty())

            archive.signingInfo = signing(Signature("1234"))
            pm.setPackageArchiveInfo(file.absolutePath, archive)
            request()
            awaitStatus("Update unavailable: APK signer mismatch")
            assertTrue(sessions.mySessions.isEmpty())
            archive.signingInfo = signing(signature)
            pm.setPackageArchiveInfo(file.absolutePath, archive)
            request()
            awaitStatus("Installing verified APK")
            val first = sessions.mySessions.single().sessionId
            assertEquals(context.packageName, sessions.getSessionInfo(first)!!.appPackageName)
            deliver(first, first + 1, PackageInstaller.STATUS_FAILURE)
            awaitStatus("Installing verified APK")
            deliver(first, first, PackageInstaller.STATUS_PENDING_USER_ACTION)
            awaitStatus("Android requires local confirmation")
            assertTrue("The native session must close before offering local review", sessions.mySessions.isEmpty())
            assertNull("Android callback must not launch an Activity", shadowOf(context).nextStartedActivity)
            assertNotNull(install.preferences.getString("pending", null))

            request()
            awaitStatus("Installing verified APK")
            updater.stop()
            sessions.abandonSession(sessions.mySessions.single().sessionId)
            updater.start()
            awaitStatus("Android's install session ended without replacing Encore")

            request()
            awaitStatus("Installing verified APK")
            val retry = sessions.mySessions.single().sessionId
            deliver(retry, retry, PackageInstaller.STATUS_FAILURE_BLOCKED)
            awaitStatus("Android did not install the update: test installer failure")
            assertFalse(install.attempt!!.installBusy)
            assertFalse(install.installed(install.attempt!!))
            sessions.abandonSession(retry) // The shadow doesn't remove a finished session.

            request()
            awaitStatus("Installing verified APK")
            val last = sessions.mySessions.single().sessionId
            deliver(last, last, PackageInstaller.STATUS_SUCCESS)
            awaitStatus("Android reports success; waiting to verify")
            assertFalse("Installer success is not replacement evidence", install.installed(install.attempt!!))
            val replaced = Intent(Intent.ACTION_MY_PACKAGE_REPLACED)
            ApkInstallReceiver().onReceive(context, replaced)
            assertNull(shadowOf(context).nextStartedService)
            // Simulate the replacement process running the new APK/core. No OS install is performed by Robolectric.
            val attempt = install.attempt!!.put("core_version", BuildConfig.CORE_VERSION)
            install.preferences.edit().putString("attempt", attempt.toString()).commit()
            pm.installPackage(installed.apply { setLongVersionCode(23) })
            ApkInstallReceiver().onReceive(context, replaced)
            assertEquals(UPDATE_RECONNECT, shadowOf(context).nextStartedService?.action)
            updater.stop()
            updater.start()
            awaitStatus("Installed Encore ${BuildConfig.CORE_VERSION}")
            assertFalse("The replaced APK must not remain pending", install.preferences.contains("pending"))
        } finally {
            updater.stop()
            parent.cancel()
        }
    }

    @Suppress("DEPRECATION")
    private fun exerciseStop(cancelParent: Boolean) {
        val context = RuntimeEnvironment.getApplication()
        val info = context.packageManager.getPackageInfo(context.packageName, 0)
        info.signatures = arrayOf(Signature("abcd"))
        shadowOf(context.packageManager).installPackage(info)
        val preferences = context.getSharedPreferences("updates", Context.MODE_PRIVATE)
        context.filesDir.resolve("engine").mkdirs()
        context.filesDir.resolve("engine/encore.json").writeText("""{"channel":"dev","channel_generation":4}""")
        val digest = "a".repeat(64)
        context.filesDir.resolve("updates/apks").mkdirs()
        context.filesDir.resolve("updates/apks/$digest.apk").writeText("download fixture")
        val pending = JSONObject().put("channel", "dev").put("selection_generation", 4)
            .put("installed_core_version", BuildConfig.CORE_VERSION).put("expires", System.currentTimeMillis() / 1000 + 3600)
            .put("revision", 8).put("sha256", digest).toString()
        preferences.edit().clear().putString("pending", pending).commit()
        UpdateRuntimeShadow.statuses.clear()
        val blocked = UpdateAttempt()
        UpdateMobileShadow.attempt = blocked
        val parent = SupervisorJob()
        val updater = ApkUpdates(context, Shadow.newInstanceOf(Runtime::class.java), CoroutineScope(parent + Dispatchers.Unconfined))
        try {
            // Merely binding/constructing must not start background checks.
            updater.requestUpdate("dev", 4, true)
            assertEquals(1L, blocked.entered.count)
            updater.start()
            updater.start()
            assertTrue("Start must begin the periodic check", blocked.entered.await(3, TimeUnit.SECONDS))
            updater.requestUpdate("dev", 4, true) // Queued behind synchronous JNI.
            if (cancelParent) parent.cancel() else updater.stop()
            assertTrue("Cancellation must reach JNI while Fetch is still blocked", blocked.closed.await(3, TimeUnit.SECONDS))
            val statusAtStop = UpdateRuntimeShadow.statuses.toList()
            updater.requestUpdate("dev", 4, true)
            // The fake deliberately ignores Close and returns a valid-looking
            // late result. Coroutine cancellation/publication fencing must win.
            blocked.release.countDown()
            runBlocking { withTimeout(3000) { parent.children.toList().joinAll() } }
            assertEquals("Queued/stopped requests must not reach Fetch", 1, blocked.calls.get())
            assertEquals(statusAtStop, UpdateRuntimeShadow.statuses.toList())
            assertEquals(pending, preferences.getString("pending", null))
            assertTrue(shadowOf(context.getSystemService(NotificationManager::class.java)).allNotifications.isEmpty())

            if (!cancelParent) {
                val fresh = UpdateAttempt().also { it.release.countDown() }
                UpdateMobileShadow.attempt = fresh
                updater.start()
                assertTrue("Restart must accept fresh results", fresh.published.await(3, TimeUnit.SECONDS))
                assertTrue(UpdateRuntimeShadow.statuses.any { it.startsWith("Downloaded and verified") })
                assertEquals("Checking the same release must retain approval instructions", pending, preferences.getString("pending", null))
                updater.stop()
            }
        } finally {
            blocked.release.countDown()
            updater.stop()
            parent.cancel()
        }
    }
}

class UpdateAttempt {
    val calls = AtomicInteger()
    val entered = CountDownLatch(1)
    val release = CountDownLatch(1)
    val closed = CountDownLatch(1)
    val published = CountDownLatch(1)
    var json = """{"release":{"core_version":"1.2.0","revision":8,"sha256":"${"a".repeat(64)}"}}"""
}

@Implements(value = Mobile::class, isInAndroidSdk = false)
class UpdateMobileShadow {
    companion object {
        lateinit var attempt: UpdateAttempt
        @JvmStatic @Implementation fun __staticInitializer__() {}
        @JvmStatic @Implementation fun newUpdates(base: String, key: String, appID: String, certificate: String,
            abi: String, directory: String, installedCore: String, installed: Long, sdk: Long): Updates =
            Shadow.newInstance(Updates::class.java, arrayOf(Int::class.javaPrimitiveType!!), arrayOf(0)).also {
                Shadow.extract<UpdateFetchShadow>(it).attempt = attempt
            }
    }
}

@Implements(value = Runtime::class, isInAndroidSdk = false)
class UpdateRuntimeShadow {
    companion object { val statuses = CopyOnWriteArrayList<String>() }
    @Implementation fun __constructor__() {}
    @Implementation fun configureUpdates(host: UpdateHost, current: String) {}
    @Implementation fun updateChannel() = "dev"
    @Implementation fun updateGeneration() = 4L
    @Implementation fun updateStatus(channel: String, generation: Long, latest: String, status: String, known: Boolean, busy: Boolean) {
        statuses.add(status)
        if (status.startsWith("Downloaded and verified")) UpdateMobileShadow.attempt.published.countDown()
    }
}

@Implements(value = Updates::class, isInAndroidSdk = false)
class UpdateFetchShadow {
    lateinit var attempt: UpdateAttempt
    @Implementation fun __constructor__(refnum: Int) {}
    @Implementation fun fetch(channel: String, download: Boolean): String {
        attempt.calls.incrementAndGet()
        attempt.entered.countDown()
        check(attempt.release.await(5, TimeUnit.SECONDS)) { "Test never released Fetch" }
        return attempt.json
    }
    @Implementation fun close() { attempt.closed.countDown() }
}
