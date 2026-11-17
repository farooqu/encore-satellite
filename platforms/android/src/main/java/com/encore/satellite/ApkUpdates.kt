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
import kotlinx.coroutines.*
import kotlinx.coroutines.sync.Mutex
import org.json.JSONObject
import java.security.MessageDigest

/** No channel/feed/key/installer commands come from dashboard JavaScript. */
class ApkUpdates(private val context: Context, private val runtime: Runtime, private val scope: CoroutineScope) : UpdateHost {
    private val mutex = Mutex()
    private val installed = context.packageManager.getPackageInfo(context.packageName, signingFlags())
    private val current = "${BuildConfig.CORE_VERSION} (${revision(installed)})"
    private val verifier by lazy {
        Mobile.newUpdates(BuildConfig.UPDATE_BASE_URL, BuildConfig.UPDATE_PUBLIC_KEY, context.packageName,
            certificates(installed).single(), Build.SUPPORTED_ABIS.first(),
            context.filesDir.resolve("updates/apks").absolutePath, BuildConfig.CORE_VERSION, revision(installed), Build.VERSION.SDK_INT.toLong())
    }

    init {
        runtime.configureUpdates(this, current)
        scope.launch { while (isActive) { requestUpdate(runtime.updateChannel(), runtime.updateGeneration(), false); delay(6 * 60 * 60 * 1000L) } }
    }

    override fun requestUpdate(channel: String, generation: Long, install: Boolean) {
        scope.launch(Dispatchers.IO) {
            // Serialize requests, then recheck channel; never install a download
            // for a channel changed while the network operation was in flight.
            mutex.lock()
            try {
                if (runtime.updateChannel() != channel || runtime.updateGeneration() != generation) return@launch
                val preferences = context.getSharedPreferences("updates", Context.MODE_PRIVATE)
                val pending = preferences.getString("pending", null)?.let { JSONObject(it) }
                if (pending != null && (pending.optString("channel") != channel || pending.optLong("selection_generation") != generation)) {
                    preferences.edit().remove("pending").commit()
                    context.getSystemService(NotificationManager::class.java).cancel(3)
                }
                runtime.updateStatus(channel, generation, "", if (install) "Downloading and verifying APK" else "Checking updates", false, install)
                val result = JSONObject(verifier.fetch(channel, install))
                if (runtime.updateChannel() != channel || runtime.updateGeneration() != generation) return@launch
                val release = result.optJSONObject("release")
                if (release == null) {
                    runtime.updateStatus(channel, generation, current, "Up to date", true, false)
                } else {
                    val latest = "${release.getString("core_version")} (${release.getLong("revision")})"
                    if (install) {
                        release.put("selection_generation", generation)
                        release.put("installed_core_version", BuildConfig.CORE_VERSION)
                        validateAPK(context, release)
                        context.getSharedPreferences("updates", Context.MODE_PRIVATE).edit()
                            .putString("pending", release.toString()).commit().also { check(it) { "Could not retain update request" } }
                        notifyReady(context)
                    }
                    runtime.updateStatus(channel, generation, latest, if (install) "Verified. Use Settings → Review downloaded update on this device to approve installation." else "Update available", true, false)
                }
            } catch (error: Exception) {
                runtime.updateStatus(channel, generation, "", "Update unavailable: ${error.message}", false, false)
            } finally { mutex.unlock() }
        }
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
    check(settings.optString("channel", "dev") == release.getString("channel")) { "Update channel changed" }
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
        val release = runCatching { JSONObject(checkNotNull(preferences.getString("pending", null))) }.getOrNull()
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
                            val reviewFailure = failure ?: runCatching { validateRequest(this, release) }.exceptionOrNull()
                            if (reviewFailure != null) {
                                preferences.edit().remove("pending").apply()
                                AlertDialog.Builder(this).setTitle("Update rejected").setMessage(reviewFailure.message)
                                    .setPositiveButton("Close") { _, _ -> finish() }.setOnCancelListener { finish() }.show()
                            } else {
                                val uri = FileProvider.getUriForFile(this, "$packageName.updates", updateFile(this, release))
                                @Suppress("DEPRECATION")
                                val intent = Intent(Intent.ACTION_INSTALL_PACKAGE).setDataAndType(uri, "application/vnd.android.package-archive").addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
                                runCatching { startActivity(intent) }.onSuccess { preferences.edit().remove("pending").apply() }
                                    .onFailure { android.widget.Toast.makeText(this, "Installer unavailable: ${it.message}", android.widget.Toast.LENGTH_LONG).show() }
                                finish()
                            }
                        }
                    }.start()
                }
            }.setNegativeButton("Later") { _, _ -> finish() }.setOnCancelListener { finish() }.show()
    }
}
