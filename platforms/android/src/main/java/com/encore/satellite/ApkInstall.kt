package com.encore.satellite

import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.PackageInstaller
import android.net.Uri
import android.os.Build
import androidx.annotation.RequiresApi
import org.json.JSONObject
import java.security.DigestInputStream
import java.security.MessageDigest

internal const val UPDATE_RECONNECT = "update-reconnect"
internal val JSONObject.installBusy get() = optString("phase") in setOf("installing", "prompt", "verifying")

/** An install attempt outlives both the satellite service and its APK. */
internal class ApkInstall(private val context: Context) {
    companion object { val lock = Any() }

    val preferences = context.getSharedPreferences("updates", Context.MODE_PRIVATE)
    val attempt get() = preferences.getString("attempt", null)?.let { JSONObject(it) }
    val supported get() = Build.VERSION.SDK_INT >= 31
    val optedIn get() = preferences.getBoolean("remote_install", false)
    val canInstall get() = supported && optedIn && context.packageManager.canRequestPackageInstalls()
    private val installer get() = context.packageManager.packageInstaller

    fun record(release: JSONObject, phase: String, status: String, session: Int = -1, resume: Boolean = false) {
        val next = JSONObject(release.toString()).put("phase", phase).put("status", status)
            .put("session", session).put("resume", resume)
        check(preferences.edit().putString("attempt", next.toString()).commit()) { "Could not retain install status" }
    }

    @RequiresApi(31)
    fun stage(release: JSONObject): Int {
        val size = release.getLong("size")
        val params = PackageInstaller.SessionParams(PackageInstaller.SessionParams.MODE_FULL_INSTALL).apply {
            setAppPackageName(context.packageName)
            setSize(size)
            setRequireUserAction(PackageInstaller.SessionParams.USER_ACTION_NOT_REQUIRED)
        }
        val id = installer.createSession(params)
        try {
            installer.openSession(id).use { session ->
                session.openWrite("base.apk", 0, size).use { output ->
                    val digest = MessageDigest.getInstance("SHA-256")
                    DigestInputStream(updateFile(context, release).inputStream(), digest).use { input ->
                        check(input.copyTo(output) == size) { "APK size changed" }
                    }
                    check(digest.digest().joinToString("") { "%02x".format(it) } == release.getString("sha256")) { "APK digest changed" }
                    session.fsync(output)
                }
            }
        } catch (failure: Exception) {
            runCatching { abandon(id) }
            throw failure
        }
        return id
    }

    @RequiresApi(31)
    fun commit(id: Int, release: JSONObject) {
        record(release, "installing", "Installing verified APK; waiting for Android", id, resume = true)
        validateRequest(context, release)
        installer.openSession(id).use { it.commit(callback(id, PendingIntent.FLAG_UPDATE_CURRENT)!!.intentSender) }
    }

    private fun callback(id: Int, flags: Int): PendingIntent? = PendingIntent.getBroadcast(context, id,
        Intent(context, ApkInstallReceiver::class.java).setData(Uri.parse("encore-install:$id")),
        PendingIntent.FLAG_MUTABLE or flags)

    fun abandon(id: Int) {
        try {
            installer.abandonSession(id)
        } finally {
            callback(id, PendingIntent.FLAG_NO_CREATE)?.cancel()
        }
    }

    fun receive(result: Intent) {
        val previous = attempt ?: return
        if (previous.optString("phase") != "installing") return
        val id = previous.getInt("session")
        if (result.data != Uri.parse("encore-install:$id") || result.getIntExtra(PackageInstaller.EXTRA_SESSION_ID, -1) != id) return
        if (result.getStringExtra(PackageInstaller.EXTRA_PACKAGE_NAME)?.let { it != context.packageName } == true) return
        when (result.getIntExtra(PackageInstaller.EXTRA_STATUS, PackageInstaller.STATUS_FAILURE)) {
            PackageInstaller.STATUS_PENDING_USER_ACTION -> {
                // Never launch Android's Activity from a broadcast. Close this session
                // before offering the existing local installer, which revalidates the APK.
                val abandoned = runCatching { abandon(id) }
                if (abandoned.isFailure) {
                    record(previous, "failed", "Could not close Android's install session. Request the update again.")
                } else if (runCatching { validateRequest(context, previous) }.isSuccess) {
                    record(previous, "confirmation", "Android requires local confirmation. $READY_TO_INSTALL", resume = previous.getBoolean("resume"))
                    runCatching { notifyReady(context) }
                } else {
                    record(previous, "failed", "Android required confirmation, but the request expired or the channel changed. Request the update again.")
                }
            }
            PackageInstaller.STATUS_SUCCESS -> record(previous, "verifying", "Android reports success; waiting to verify the replacement APK",
                resume = previous.getBoolean("resume"))
            else -> record(previous, "failed", "Android did not install the update: " +
                (result.getStringExtra(PackageInstaller.EXTRA_STATUS_MESSAGE)?.take(140) ?: "installer rejected the request"))
        }
        callback(id, PendingIntent.FLAG_NO_CREATE)?.cancel()
    }

    fun installed(attempt: JSONObject): Boolean = attempt.getLong("revision") == revision(
        context.packageManager.getPackageInfo(context.packageName, 0)) && attempt.getString("core_version") == BuildConfig.CORE_VERSION

    fun cancelReconnect(): Unit = synchronized(lock) {
        val previous = attempt ?: return@synchronized
        if (previous.optBoolean("resume")) {
            preferences.edit().putString("attempt", previous.put("resume", false).toString()).commit()
        }
    }

    fun reconcile(previous: JSONObject) {
        // Until the running APK proves replacement, a missing session is not success.
        val phase = previous.getString("phase")
        if (phase == "installing" && Build.VERSION.SDK_INT >= 31) {
            val id = previous.getInt("session")
            if (installer.getSessionInfo(id)?.isSealed != true || callback(id, PendingIntent.FLAG_NO_CREATE) == null) {
                runCatching { abandon(id) }
                record(previous, "failed", "Android's install session ended without replacing Encore. Request the update again.")
            }
        } else if (phase == "verifying" || phase == "prompt") {
            record(previous, "failed", "Encore could not verify replacement. $READY_TO_INSTALL", resume = previous.getBoolean("resume"))
        }
    }
}

class ApkInstallReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) = synchronized(ApkInstall.lock) {
        val install = ApkInstall(context)
        if (intent.action == Intent.ACTION_MY_PACKAGE_REPLACED) {
            val attempt = install.attempt ?: return@synchronized
            if (attempt.optBoolean("resume") && install.installed(attempt)) {
                // The package-replaced exemption permits foreground service start,
                // not background access to the while-in-use microphone permission.
                runCatching { context.startForegroundService(Intent(context, EncoreService::class.java).setAction(UPDATE_RECONNECT)) }
            }
        } else {
            install.receive(intent)
        }
        Unit
    }
}
