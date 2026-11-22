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
import java.util.UUID

internal const val READY_TO_INSTALL = "Downloaded and verified. In Encore, open Settings → Review downloaded update to approve installation."

/** No channel/feed/key/installer commands come from dashboard JavaScript. */
class ApkUpdates(private val context: Context, private val runtime: Runtime, private val scope: CoroutineScope) : UpdateHost {
    private val mutex = Mutex()
    private val lifecycle = ApkInstall.lock
    private inner class Worker {
        val job = SupervisorJob(this@ApkUpdates.scope.coroutineContext[Job])
        val scope = CoroutineScope(this@ApkUpdates.scope.coroutineContext + job)
        var verifier: Updates? = null
    }
    private var worker: Worker? = null
    private val installed = context.packageManager.getPackageInfo(context.packageName, signingFlags() or PackageManager.GET_META_DATA)
    private val updateTrust = installed.applicationInfo?.metaData
    private val current = BuildConfig.CORE_VERSION
    private val installer = ApkInstall(context)
    private val preferences = installer.preferences
    private val installListener = SharedPreferences.OnSharedPreferenceChangeListener { _, key ->
        if (key == "attempt") {
            val active = synchronized(lifecycle) { worker }
            active?.scope?.launch(Dispatchers.IO) {
                synchronized(lifecycle) {
                    if (worker === active && active.job.isActive && installer.attempt != null) {
                        val channel = runtime.updateChannel()
                        val generation = runtime.updateGeneration()
                        if (!publishAttempt(channel, generation)) requestUpdate(channel, generation, false)
                    }
                }
            }
        }
    }

    init {
        runtime.configureUpdates(this, current)
    }

    /** Service Start, not binding/onCreate, owns the periodic update loop. */
    fun start() = synchronized(lifecycle) {
        if (worker?.job?.isActive == true) return@synchronized
        val next = Worker()
        worker = next
        preferences.registerOnSharedPreferenceChangeListener(installListener)
        installer.attempt?.let { attempt ->
            if (installer.installed(attempt)) {
                if (preferences.contains("pending")) clearPending()
            } else installer.reconcile(attempt)
        }
        val restored = publishAttempt(runtime.updateChannel(), runtime.updateGeneration())
        next.scope.launch {
            if (restored) delay(6 * 60 * 60 * 1000L)
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
        preferences.unregisterOnSharedPreferenceChangeListener(installListener)
        previous.verifier?.close()
        previous.job.cancel()
    }

    override fun requestUpdate(channel: String, generation: Long, install: Boolean) {
        val active = synchronized(lifecycle) { worker?.takeIf { it.job.isActive } } ?: return
        active.scope.launch(Dispatchers.IO) {
            // Serialize requests, then recheck channel; never install a download
            // for a channel changed while the network operation was in flight.
            mutex.lock()
            var session: Int? = null
            var nativeRelease: JSONObject? = null
            try {
                val verifier = synchronized(lifecycle) {
                    if (worker !== active || !active.job.isActive) return@launch
                    if (runtime.updateChannel() != channel || runtime.updateGeneration() != generation) return@launch
                    val attempt = installer.attempt
                    if (attempt != null && !installer.installed(attempt) && attempt.installBusy) {
                        publishAttempt(channel, generation)
                        return@launch
                    }
                    val pending = preferences.getString("pending", null)
                    if (pending != null && runCatching { validateRequest(context, JSONObject(pending)) }.isFailure) {
                        clearPending()
                    }
                    runtime.updateStatus(channel, generation, "", if (install) "Downloading and verifying APK" else "Checking updates", false, install, "", "")
                    active.verifier ?: Mobile.newUpdates(updateTrust?.getString("com.encore.update_base_url").orEmpty(),
                        updateTrust?.getString("com.encore.update_public_key").orEmpty(), context.packageName,
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
                    if (Build.VERSION.SDK_INT >= 31 && installer.canInstall) {
                        nativeRelease = release
                        session = installer.stage(release)
                    }
                }
                // Stop and publication share one lock: a late Fetch/validation
                // cannot publish into a stopped or subsequently restarted worker.
                synchronized(lifecycle) {
                    ensureActive()
                    if (worker !== active || !active.job.isActive) return@launch
                    if (runtime.updateChannel() != channel || runtime.updateGeneration() != generation) return@launch
                    val attempt = installer.attempt
                    if (attempt != null && !installer.installed(attempt) && attempt.installBusy) {
                        publishAttempt(channel, generation)
                        return@launch
                    }
                    if (install && release != null) {
                        validateRequest(context, release)
                        retainPending(release)
                    }
                    val id = session
                    if (Build.VERSION.SDK_INT >= 31 && id != null && release != null && installer.canInstall) {
                        installer.commit(id, release)
                        session = null // Android owns the committed session now.
                        publishAttempt(channel, generation)
                        return@launch
                    }
                    val pending = preferences.getString("pending", null)?.let { JSONObject(it) }
                    // A later trusted check may supersede a previously downloaded APK.
                    val ready = release != null && pending != null && pending.getString("sha256") == release.getString("sha256") &&
                        pending.getLong("revision") == release.getLong("revision") &&
                        runCatching { validateRequest(context, pending) }.isSuccess && updateFile(context, pending).isFile
                    if (pending != null && !ready) clearPending()
                    if (release == null) {
                        if (!publishAttempt(channel, generation, offer = false)) runtime.updateStatus(channel, generation, current, "Up to date", true, false, "", "")
                    } else {
                        val latest = release.getString("core_version")
                        if (install) {
                            // Notification denial must not hide a verified request from Settings or HA.
                            runCatching { notifyReady(context) }
                        }
                        if (!ready || !publishAttempt(channel, generation)) {
                            runtime.updateStatus(channel, generation, latest, if (ready) READY_TO_INSTALL else "Update available", true, false,
                                release.optString("url"), release.optString("release_summary"))
                        }
                    }
                }
            } catch (error: CancellationException) {
                throw error
            } catch (error: Exception) {
                synchronized(lifecycle) {
                    if (worker === active && active.job.isActive) {
                        val existing = installer.attempt
                        if (existing?.installBusy == true && !installer.installed(existing) && existing.optInt("session", -1) != session) {
                            publishAttempt(channel, generation)
                            return@synchronized
                        }
                        val verified = nativeRelease
                        if (verified != null && runtime.updateChannel() == channel && runtime.updateGeneration() == generation &&
                            runCatching { validateRequest(context, verified) }.isSuccess) {
                            // The staged bytes may have changed. Do not authorize the local installer on a staging error.
                            val retryLocal = session != null && preferences.getString("pending", null) == verified.toString()
                            val status = "Could not install the verified APK: ${error.message?.take(140)}"
                            if (!retryLocal && preferences.contains("pending")) runCatching { clearPending() }
                            val retained = runCatching {
                                installer.record(verified, "failed", if (retryLocal) "$status. $READY_TO_INSTALL" else status)
                            }.isSuccess
                            if (retryLocal) runCatching { notifyReady(context) }
                            if (!retained || !publishAttempt(channel, generation)) {
                                runtime.updateStatus(channel, generation, "", status, false, false, "", "")
                            }
                        } else {
                            if (session != null && existing != null && existing.optInt("session", -1) == session) {
                                runCatching { installer.record(existing, "failed", "Install request expired or changed before Android accepted it.") }
                            }
                            runtime.updateStatus(channel, generation, "", "Update unavailable: ${error.message?.take(180)}", false, false, "", "")
                        }
                    }
                }
            } finally {
                session?.let { runCatching { installer.abandon(it) } }
                mutex.unlock()
            }
        }
    }

    private fun retainPending(release: JSONObject) {
        check(preferences.edit().putString("pending", release.toString()).remove("attempt").commit()) { "Could not retain update request" }
    }

    private fun publishAttempt(channel: String, generation: Long, offer: Boolean = true): Boolean {
        val attempt = installer.attempt ?: return false
        if (installer.installed(attempt)) {
            runtime.updateStatus(channel, generation, current, "Installed Encore $current", true, false, "", "")
            return true
        }
        val busy = attempt.installBusy
        if (attempt.getString("channel") != channel || attempt.getLong("selection_generation") != generation) {
            if (busy) runtime.updateStatus(channel, generation, current, "Android is finishing an earlier install request", false, true, "", "")
            return busy
        }
        val known = offer && preferences.contains("pending") && runCatching { validateRequest(context, attempt) }.isSuccess && updateFile(context, attempt).isFile
        runtime.updateStatus(channel, generation, if (known) attempt.getString("core_version") else current,
            if (known || busy || attempt.optString("phase") == "failed") attempt.getString("status") else "Local approval expired or was superseded. Request the update again.", known, busy,
            attempt.optString("url"), attempt.optString("release_summary"))
        return true
    }

    private fun clearPending() {
        check(preferences.edit().remove("pending").commit()) { "Could not clear the previous update request" }
        context.getSystemService(NotificationManager::class.java).cancel(3)
    }
}

@Suppress("DEPRECATION")
private fun signingFlags() = if (Build.VERSION.SDK_INT >= 28) PackageManager.GET_SIGNING_CERTIFICATES else PackageManager.GET_SIGNATURES
@Suppress("DEPRECATION")
internal fun revision(info: PackageInfo): Long = if (Build.VERSION.SDK_INT >= 28) info.longVersionCode else info.versionCode.toLong()
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

internal fun validateRequest(context: Context, release: JSONObject) {
    // A request verified against an earlier installed core cannot authorize an
    // update after another APK replacement. Recheck the feed against the new floor.
    check(release.getString("installed_core_version") == BuildConfig.CORE_VERSION) { "Installed core changed; check again" }
    val settings = JSONObject(context.filesDir.resolve("engine/encore.json").readText())
    // Older fresh installs persisted an empty channel; Go has always treated it as Dev.
    check(settings.optString("channel").ifEmpty { "dev" } == release.getString("channel")) { "Update channel changed" }
    check(settings.optLong("channel_generation") == release.getLong("selection_generation")) { "Update selection changed; request again" }
    check(System.currentTimeMillis() / 1000 < release.getLong("expires")) { "Update metadata expired; check again" }
}

internal fun updateFile(context: Context, release: JSONObject): java.io.File {
    val digest = release.getString("sha256")
    check(digest.matches(Regex("[0-9a-f]{64}"))) { "Invalid APK digest" }
    return context.filesDir.resolve("updates/apks/$digest.apk")
}

internal fun notifyReady(context: Context) {
    val manager = context.getSystemService(NotificationManager::class.java)
    manager.createNotificationChannel(NotificationChannel("updates", "Encore updates", NotificationManager.IMPORTANCE_DEFAULT))
    val intent = PendingIntent.getActivity(context, 3, Intent(context, UpdateActivity::class.java), PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    manager.notify(3, Notification.Builder(context, "updates").setSmallIcon(R.drawable.ic_encore)
        .setContentTitle("Encore update ready").setContentText("Tap to review and approve the APK update on this device.")
        .setContentIntent(intent).setAutoCancel(true).build())
}

/** Local enrollment and installer review; remote requests never open this Activity. */
@Suppress("DEPRECATION")
class UpdateActivity : Activity() {
    companion object { internal const val REMOTE_SETTINGS = "remote_settings" }
    private val installer by lazy { ApkInstall(this) }
    private val preferences get() = installer.preferences
    private var prompt: String? = null

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        prompt = savedInstanceState?.getString("prompt")
        if (prompt != null || savedInstanceState?.getBoolean("enrolling") == true) {
            enrolling = savedInstanceState?.getBoolean("enrolling") == true
            return
        }
        if (intent.getBooleanExtra(REMOTE_SETTINGS, false) && installer.supported) {
            showRemoteSettings()
            return
        }
        val request = preferences.getString("pending", null)
        val release = runCatching { JSONObject(checkNotNull(request)) }.getOrNull()
        val attempt = installer.attempt
        val dialog = AlertDialog.Builder(this).setOnCancelListener { finish() }
        if (installer.supported) dialog.setNeutralButton("Remote installs") { _, _ -> showRemoteSettings() }
        if (attempt?.installBusy == true && !installer.installed(attempt)) {
            dialog.setTitle("Encore APK update").setMessage(attempt.getString("status"))
                .setPositiveButton("Close") { _, _ -> finish() }.show()
            return
        }
        if (release == null || (attempt != null && installer.installed(attempt))) {
            dialog.setTitle("No downloaded update")
                .setMessage("Choose a channel and request the APK update through Home Assistant first.")
                .setPositiveButton("Close") { _, _ -> finish() }.show()
            return
        }
        dialog.setTitle("Encore APK update")
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
                            val reviewFailure = failure ?: runCatching { synchronized(ApkInstall.lock) {
                                check(preferences.getString("pending", null) == request) { "Update request changed; open it again" }
                                validateRequest(this, release)
                                val previous = installer.attempt
                                check(previous?.installBusy != true) { "Android is already handling an install" }
                                val resume = previous?.optBoolean("resume") == true && previous.optString("phase") == "confirmation"
                                launchInstaller(release, resume)
                            } }.exceptionOrNull()
                            if (reviewFailure != null) {
                                AlertDialog.Builder(this).setTitle("Update rejected").setMessage(reviewFailure.message)
                                    .setPositiveButton("Close") { _, _ -> finish() }.setOnCancelListener { finish() }.show()
                            }
                        }
                    }.start()
                }
            }.setNegativeButton("Later") { _, _ -> finish() }.setOnCancelListener { finish() }.show()
    }

    private var enrolling = false

    private fun showRemoteSettings() {
        val enabled = installer.optedIn
        val paused = enabled && !packageManager.canRequestPackageInstalls()
        val dialog = AlertDialog.Builder(this).setTitle("Install from Home Assistant")
            .setMessage(when {
                paused -> "Remote installs are paused. Restore Android’s Allow from this source setting, or turn remote installs off. Checking for updates never installs them."
                enabled -> "Enabled. Encore can install verified APK updates when you choose Install in Home Assistant. Android may still require confirmation. Checking for updates never installs them."
                else -> "Allow explicit Home Assistant Install requests to install verified APK updates without a second tap when Android permits. Enable Allow from this source in Android if asked. Checking never installs updates. After an update, open Encore to restore microphone access."
            })
            .setPositiveButton(if (paused) "Restore Android access" else if (enabled) "Disable" else "Enable") { _, _ ->
                when {
                    enabled && !paused -> enrollmentResult(false)
                    packageManager.canRequestPackageInstalls() -> enrollmentResult(true)
                    else -> {
                        enrolling = true
                        runCatching { startActivityForResult(Intent(Settings.ACTION_MANAGE_UNKNOWN_APP_SOURCES, Uri.parse("package:$packageName")), 1) }
                            .onFailure { finishEnrollment() }
                    }
                }
            }.setNegativeButton("Close") { _, _ -> finish() }.setOnCancelListener { finish() }
        if (paused) dialog.setNeutralButton("Disable") { _, _ -> enrollmentResult(false) }
        dialog.show()
    }

    private fun finishEnrollment() {
        enrollmentResult(installer.optedIn || packageManager.canRequestPackageInstalls())
    }

    private fun enrollmentResult(enabled: Boolean) {
        enrolling = false
        val saved = synchronized(ApkInstall.lock) { preferences.edit().putBoolean("remote_install", enabled).commit() }
        val message = when {
            !saved -> "Could not save remote install preference."
            installer.canInstall -> "Enabled. Choose Install in Home Assistant when you want to update."
            enabled -> "Remote installs are paused. Updates still need local approval."
            else -> "Remote installs are off. Updates still need local approval."
        }
        AlertDialog.Builder(this).setTitle("Install from Home Assistant").setMessage(message)
            .setPositiveButton("Close") { _, _ -> finish() }.setOnCancelListener { finish() }.show()
    }

    private fun launchInstaller(release: JSONObject, resume: Boolean) {
        val token = UUID.randomUUID().toString()
        val attempt = JSONObject(release.toString()).put("prompt", token)
        installer.record(attempt, "prompt", "Waiting for Android installer confirmation on the device", resume = resume)
        try {
            val uri = FileProvider.getUriForFile(this, "$packageName.updates", updateFile(this, release))
            startActivityForResult(Intent(Intent.ACTION_INSTALL_PACKAGE)
                .setDataAndType(uri, "application/vnd.android.package-archive")
                .addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION).putExtra(Intent.EXTRA_RETURN_RESULT, true), 2)
            prompt = token
        } catch (failure: Exception) {
            installer.record(attempt, "failed", "Installer unavailable. $READY_TO_INSTALL")
            throw failure
        }
    }

    override fun onResume() {
        super.onResume()
        if (enrolling) finishEnrollment()
    }

    override fun onActivityResult(requestCode: Int, resultCode: Int, data: Intent?) {
        super.onActivityResult(requestCode, resultCode, data)
        if (requestCode == 1 && enrolling) finishEnrollment()
        if (requestCode == 2) {
            synchronized(ApkInstall.lock) {
                val attempt = installer.attempt
                if (attempt != null && prompt != null && attempt.optString("prompt") == prompt && attempt.optString("phase") == "prompt") {
                    val resume = attempt.getBoolean("resume")
                    when (resultCode) {
                        RESULT_OK -> installer.record(attempt, "verifying", "Android reports success; waiting to verify the replacement APK", resume = resume)
                        RESULT_CANCELED -> installer.record(attempt, "confirmation", "Android installation cancelled. $READY_TO_INSTALL", resume = resume)
                        else -> installer.record(attempt, "failed", "Android could not install the update. $READY_TO_INSTALL", resume = resume)
                    }
                }
            }
            finish()
        }
    }

    override fun onSaveInstanceState(outState: Bundle) {
        super.onSaveInstanceState(outState)
        outState.putString("prompt", prompt)
        outState.putBoolean("enrolling", enrolling)
    }
}
