package com.encore.satellite

import android.app.*
import android.content.*
import android.content.pm.PackageInfo
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.provider.Settings
import androidx.core.content.FileProvider
import com.encore.engine.mobile.Mobile
import com.encore.engine.mobile.Runtime
import com.encore.engine.mobile.UpdateHost
import com.encore.engine.mobile.Updates
import kotlinx.coroutines.*
import kotlinx.coroutines.sync.Mutex
import org.json.JSONObject
import java.security.MessageDigest

private const val READY_TO_INSTALL = "Downloaded and verified. In Encore, open Settings → Review downloaded update to approve installation."

/** No channel/feed/key/installer commands come from dashboard JavaScript. */
class ApkUpdates(private val context: Context, private val runtime: Runtime, private val scope: CoroutineScope) : UpdateHost {
    private val mutex = Mutex()
    private val lifecycle = Any()
    private inner class Worker {
        val job = SupervisorJob(this@ApkUpdates.scope.coroutineContext[Job])
        val scope = CoroutineScope(this@ApkUpdates.scope.coroutineContext + job)
        var verifier: Updates? = null
    }
    private var worker: Worker? = null
    private val installed = context.packageManager.getPackageInfo(context.packageName, signingFlags())
    private val current = BuildConfig.CORE_VERSION
    private val preferences = context.getSharedPreferences("updates", Context.MODE_PRIVATE)

    init {
        runtime.configureUpdates(this, current)
    }

    /** Service Start, not binding/onCreate, owns the periodic update loop. */
    fun start() = synchronized(lifecycle) {
        if (worker?.job?.isActive == true) return@synchronized
        val next = Worker()
        worker = next
        next.scope.launch {
            while (isActive) {
                requestUpdate(runtime.updateChannel(), runtime.updateGeneration(), false)
                delay(6 * 60 * 60 * 1000L)
            }
        }
        Unit
    }

    /** Fence side effects before returning, even if the service remains bound. */
    fun stop() = synchronized(lifecycle) {
        val previous = worker ?: return@synchronized
        worker = null
        previous.verifier?.close()
        previous.job.cancel()
    }

    override fun requestUpdate(channel: String, generation: Long, install: Boolean) {
        val active = synchronized(lifecycle) { worker?.takeIf { it.job.isActive } } ?: return
        active.scope.launch(Dispatchers.IO) {
            // Serialize requests, then recheck channel; never install a download
            // for a channel changed while the network operation was in flight.
            mutex.lock()
            try {
                val verifier = synchronized(lifecycle) {
                    if (worker !== active || !active.job.isActive) return@launch
                    if (runtime.updateChannel() != channel || runtime.updateGeneration() != generation) return@launch
                    val pending = preferences.getString("pending", null)
                    if (pending != null && runCatching { validateRequest(context, JSONObject(pending)) }.isFailure) {
                        clearPending()
                    }
                    runtime.updateStatus(channel, generation, "", if (install) "Downloading and verifying APK" else "Checking updates", false, install)
                    active.verifier ?: Mobile.newUpdates(BuildConfig.UPDATE_BASE_URL, BuildConfig.UPDATE_PUBLIC_KEY, context.packageName,
                        certificates(installed).single(), Build.SUPPORTED_ABIS.first(),
                        context.filesDir.resolve("updates/apks").absolutePath, BuildConfig.CORE_VERSION, revision(installed), Build.VERSION.SDK_INT.toLong())
                        .also { active.verifier = it }
                }
                val result = JSONObject(suspendCancellableCoroutine<String> { continuation ->
                    // Cancellation of a coroutine alone cannot interrupt synchronous JNI.
                    // This handler also covers destruction/cancellation of the parent scope.
                    continuation.invokeOnCancellation { verifier.close() }
                    continuation.resumeWith(runCatching { verifier.fetch(channel, install) })
                })
                ensureActive()
                val release = result.optJSONObject("release")
                if (release != null && install) {
                    release.put("selection_generation", generation)
                    release.put("installed_core_version", BuildConfig.CORE_VERSION)
                    validateAPK(context, release)
                }
                // Stop and publication share one lock: a late Fetch/validation
                // cannot publish into a stopped or subsequently restarted worker.
                synchronized(lifecycle) {
                    ensureActive()
                    if (worker !== active || !active.job.isActive) return@launch
                    if (runtime.updateChannel() != channel || runtime.updateGeneration() != generation) return@launch
                    val pending = preferences.getString("pending", null)?.let { JSONObject(it) }
                    // A later trusted check may supersede a previously downloaded APK.
                    val ready = release != null && pending != null && pending.getString("sha256") == release.getString("sha256") &&
                        pending.getLong("revision") == release.getLong("revision") &&
                        runCatching { validateRequest(context, pending) }.isSuccess && updateFile(context, pending).isFile
                    if (pending != null && !ready) clearPending()
                    if (release == null) {
                        runtime.updateStatus(channel, generation, current, "Up to date", true, false)
                    } else {
                        val latest = release.getString("core_version")
                        if (install) {
                            preferences.edit()
                                .putString("pending", release.toString()).commit().also { check(it) { "Could not retain update request" } }
                            // Notification denial must not hide a verified request from Settings or HA.
                            runCatching { notifyReady(context) }
                        }
                        runtime.updateStatus(channel, generation, latest, if (install || ready) READY_TO_INSTALL else "Update available", true, false)
                    }
                }
            } catch (error: CancellationException) {
                throw error
            } catch (error: Exception) {
                synchronized(lifecycle) {
                    if (worker === active && active.job.isActive) {
                        runtime.updateStatus(channel, generation, "", "Update unavailable: ${error.message?.take(180)}", false, false)
                    }
                }
            } finally { mutex.unlock() }
        }
    }

    private fun clearPending() {
        check(preferences.edit().remove("pending").commit()) { "Could not clear the previous update request" }
        context.getSystemService(NotificationManager::class.java).cancel(3)
    }
}

@Suppress("DEPRECATION")
private fun signingFlags() = if (Build.VERSION.SDK_INT >= 28) PackageManager.GET_SIGNING_CERTIFICATES else PackageManager.GET_SIGNATURES
@Suppress("DEPRECATION")
private fun revision(info: PackageInfo): Long = if (Build.VERSION.SDK_INT >= 28) info.longVersionCode else info.versionCode.toLong()
@Suppress("DEPRECATION")
private fun certificates(info: PackageInfo): Set<String> {
    val signatures = if (Build.VERSION.SDK_INT >= 28) info.signingInfo?.apkContentsSigners else info.signatures
    return checkNotNull(signatures).map { signature -> MessageDigest.getInstance("SHA-256").digest(signature.toByteArray()).joinToString("") { "%02x".format(it) } }.toSet()
}

internal fun validateAPK(context: Context, release: JSONObject) {
    validateRequest(context, release)
    val file = updateFile(context, release)
    check(file.length() == release.getLong("size")) { "APK length changed" }
    val digest = MessageDigest.getInstance("SHA-256")
    file.inputStream().use { input -> val buffer = ByteArray(65536); while (true) { val n = input.read(buffer); if (n < 0) break; digest.update(buffer, 0, n) } }
    check(digest.digest().joinToString("") { "%02x".format(it) } == release.getString("sha256")) { "APK digest changed" }
    val pm = context.packageManager
    val archive = checkNotNull(pm.getPackageArchiveInfo(file.absolutePath, signingFlags() or PackageManager.GET_META_DATA)) { "Invalid APK" }
    val installed = pm.getPackageInfo(context.packageName, signingFlags())
    check(archive.packageName == context.packageName && archive.packageName == release.getString("application_id")) { "Wrong application" }
    check(revision(archive) == release.getLong("revision") && revision(archive) > revision(installed)) { "Not a newer package revision" }
    check(archive.versionName == release.getString("version")) { "Version mismatch" }
    check(archive.applicationInfo?.metaData?.getString("com.encore.core_version") == release.getString("core_version")) { "Core version mismatch" }
    check(certificates(archive) == certificates(installed) && certificates(archive).single() == release.getString("certificate_sha256")) { "APK signer mismatch" }
    check(release.getString("backend") == "incubator" && release.getString("target") == "android-generic") { "Wrong backend or target" }
}

private fun validateRequest(context: Context, release: JSONObject) {
    // A request verified against an earlier installed core cannot authorize an
    // update after another APK replacement. Recheck the feed against the new floor.
    check(release.getString("installed_core_version") == BuildConfig.CORE_VERSION) { "Installed core changed; check again" }
    val settings = JSONObject(context.filesDir.resolve("engine/encore.json").readText())
    // Older fresh installs persisted an empty channel; Go has always treated it as Dev.
    check(settings.optString("channel").ifEmpty { "dev" } == release.getString("channel")) { "Update channel changed" }
    check(settings.optLong("channel_generation") == release.getLong("selection_generation")) { "Update selection changed; request again" }
    check(System.currentTimeMillis() / 1000 < release.getLong("expires")) { "Update metadata expired; check again" }
}

private fun updateFile(context: Context, release: JSONObject): java.io.File {
    val digest = release.getString("sha256")
    check(digest.matches(Regex("[0-9a-f]{64}"))) { "Invalid APK digest" }
    return context.filesDir.resolve("updates/apks/$digest.apk")
}

private fun notifyReady(context: Context) {
    val manager = context.getSystemService(NotificationManager::class.java)
    manager.createNotificationChannel(NotificationChannel("updates", "Encore updates", NotificationManager.IMPORTANCE_DEFAULT))
    val intent = PendingIntent.getActivity(context, 3, Intent(context, UpdateActivity::class.java), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    manager.notify(3, Notification.Builder(context, "updates").setSmallIcon(R.drawable.ic_encore)
        .setContentTitle("Encore update ready").setContentText("Tap to review and approve the APK update on this device.")
        .setContentIntent(intent).setAutoCancel(true).build())
}

/** Only a local user opens Settings/Android's installer; no background launch. */
class UpdateActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val preferences = getSharedPreferences("updates", MODE_PRIVATE)
        val request = preferences.getString("pending", null)
        val release = runCatching { JSONObject(checkNotNull(request)) }.getOrNull()
        if (release == null) {
            AlertDialog.Builder(this).setTitle("No downloaded update")
                .setMessage("Choose a channel and request the APK update through ESPHome first.")
                .setPositiveButton("Close") { _, _ -> finish() }.setOnCancelListener { finish() }.show()
            return
        }
        AlertDialog.Builder(this).setTitle("Encore APK update")
            .setMessage("Install ${release.optString("version")} from ${release.optString("channel")}? Android will ask you to confirm. Voice and music stop during replacement; app data is retained.")
            .setPositiveButton(if (packageManager.canRequestPackageInstalls()) "Continue to installer" else "Allow APK installation") { _, _ ->
                if (!packageManager.canRequestPackageInstalls()) {
                    runCatching { startActivity(Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:$packageName"))) }
                        .onFailure { android.widget.Toast.makeText(this, "Install settings unavailable; use verified manual provisioning.", android.widget.Toast.LENGTH_LONG).show() }
                    finish()
                } else {
                    // Hashing and package parsing must not block voice/UI threads.
                    Thread {
                        val failure = runCatching { validateAPK(this, release) }.exceptionOrNull()
                        runOnUiThread {
                            if (isFinishing || isDestroyed) return@runOnUiThread
                            // Selection may have changed while this UI task was queued.
                            val reviewFailure = failure ?: runCatching {
                                check(preferences.getString("pending", null) == request) { "Update request changed; open it again" }
                                validateRequest(this, release)
                            }.exceptionOrNull()
                            if (reviewFailure != null) {
                                AlertDialog.Builder(this).setTitle("Update rejected").setMessage(reviewFailure.message)
                                    .setPositiveButton("Close") { _, _ -> finish() }.setOnCancelListener { finish() }.show()
                            } else {
                                // Launch is not success. Keep the verified request retryable if Android is cancelled.
                                runCatching {
                                    val uri = FileProvider.getUriForFile(this, "$packageName.updates", updateFile(this, release))
                                    @Suppress("DEPRECATION")
                                    startActivity(Intent(Intent.ACTION_INSTALL_PACKAGE)
                                        .setDataAndType(uri, "application/vnd.android.package-archive")
                                        .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION))
                                }.onFailure { android.widget.Toast.makeText(this, "Installer unavailable: ${it.message}", android.widget.Toast.LENGTH_LONG).show() }
                                finish()
                            }
                        }
                    }.start()
                }
            }.setNegativeButton("Later") { _, _ -> finish() }.setOnCancelListener { finish() }.show()
    }
}
