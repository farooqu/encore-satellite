package com.encore.satellite

import android.app.NotificationManager
import android.content.Context
import android.content.pm.Signature
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
        return """{"release":{"core_version":"1.2.0","revision":8,"sha256":"${"a".repeat(64)}"}}"""
    }
    @Implementation fun close() { attempt.closed.countDown() }
}
