package com.encore.satellite

import android.app.Application
import android.content.Context
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.net.Uri
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import android.webkit.WebView
import androidx.compose.foundation.Image
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.unit.dp
import androidx.lifecycle.AndroidViewModel
import androidx.lifecycle.viewModelScope
import androidx.webkit.WebViewCompat
import androidx.webkit.WebViewFeature
import com.encore.engine.mobile.DashboardEnrollment
import com.encore.engine.mobile.DashboardSession
import com.encore.engine.mobile.Mobile
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.NonCancellable
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.net.Inet4Address
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

internal class DashboardAuth(app: Application) : AndroidViewModel(app) {
    private val prefs = app.getSharedPreferences("dashboard-auth", Context.MODE_PRIVATE)
    private val network = app.getSystemService(ConnectivityManager::class.java)
    private val key: SecretKey by lazy {
        val alias = "encore-dashboard-auth"
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (store.getKey(alias, null) as SecretKey?) ?: KeyGenerator.getInstance(
            KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore").run {
            init(KeyGenParameterSpec.Builder(alias, KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT)
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM).setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE).build())
            generateKey()
        }
    }

    var saved by mutableStateOf<DashboardSession?>(null)
        private set
    var lostOrigin by mutableStateOf<String?>(null)
        private set
    var attempt by mutableStateOf<Attempt?>(null)
        private set

    init {
        prefs.getString("origin", null)?.let { origin ->
            saved = runCatching {
                val bytes = Base64.decode(requireNotNull(prefs.getString("session", null)), Base64.NO_WRAP)
                val cipher = Cipher.getInstance("AES/GCM/NoPadding")
                cipher.init(Cipher.DECRYPT_MODE, key, GCMParameterSpec(128, bytes.copyOfRange(0, 12)))
                Mobile.restoreDashboardSession(String(cipher.doFinal(bytes.copyOfRange(12, bytes.size)), Charsets.UTF_8))
                    .takeIf { it.origin() == origin }
            }.getOrNull()
            if (saved == null) lostOrigin = origin
        }
    }

    class Attempt(val origin: String) {
        var qr by mutableStateOf<Bitmap?>(null)
        var candidate by mutableStateOf<DashboardSession?>(null)
        var error by mutableStateOf("")
        var enrollment: DashboardEnrollment? = null
        var job: Job? = null
    }

    fun start(origin: String) {
        cancel()
        val next = Attempt(origin)
        attempt = next
        next.job = viewModelScope.launch(Dispatchers.IO) {
            var enrollment: DashboardEnrollment? = null
            try {
                val active = network.activeNetwork
                val caps = network.getNetworkCapabilities(active)
                val address = if (caps?.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) == true ||
                    caps?.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET) == true) {
                    network.getLinkProperties(active)?.linkAddresses?.map { it.address }
                        ?.filterIsInstance<Inet4Address>()?.firstOrNull { it.isSiteLocalAddress }?.hostAddress
                } else null
                enrollment = Mobile.beginDashboardEnrollment(origin, address.orEmpty())
                val bytes = enrollment.qrCode()
                val qr = requireNotNull(BitmapFactory.decodeByteArray(bytes, 0, bytes.size))
                val visible = withContext(Dispatchers.Main.immediate) {
                    if (attempt !== next) false else {
                        next.enrollment = enrollment
                        next.qr = qr
                        true
                    }
                }
                if (visible) {
                    val candidate = enrollment.await()
                    val kept = withContext(NonCancellable) {
                        val current = withContext(Dispatchers.Main.immediate) {
                            if (attempt !== next) false else {
                                next.qr = null
                                next.candidate = candidate
                                true
                            }
                        }
                        if (!current) runCatching { candidate.revoke() }
                        current
                    }
                    if (kept) {
                        delay(5 * 60 * 1000L)
                        withContext(Dispatchers.Main.immediate) {
                            if (attempt === next) {
                                next.candidate = null
                                next.error = "Confirmation timed out. Start a new phone sign-in."
                                revoke(candidate)
                            }
                        }
                    }
                }
            } catch (error: Exception) {
                withContext(NonCancellable + Dispatchers.Main.immediate) {
                    if (attempt === next) {
                        next.qr = null
                        next.error = error.message ?: "Couldn't start phone sign-in."
                    }
                }
            } finally {
                enrollment?.close()
            }
        }
    }

    fun cancel() {
        val current = attempt ?: return
        attempt = null
        current.enrollment?.close()
        current.job?.cancel()
        current.candidate?.let(::revoke)
    }

    fun accept(current: Attempt) {
        if (attempt !== current) return
        val candidate = current.candidate ?: return
        if (!runCatching {
                val cipher = Cipher.getInstance("AES/GCM/NoPadding")
                cipher.init(Cipher.ENCRYPT_MODE, key)
                val encrypted = cipher.iv + cipher.doFinal(candidate.serialize().toByteArray())
                prefs.edit().putString("origin", candidate.origin())
                    .putString("session", Base64.encodeToString(encrypted, Base64.NO_WRAP)).commit()
            }.getOrDefault(false)) {
            current.error = "Couldn't securely save the session. Try again, or sign in on the tablet."
            return
        }
        val previous = saved
        saved = candidate
        lostOrigin = null
        current.candidate = null
        cancel()
        previous?.let(::revoke)
    }

    fun sessionLost(session: DashboardSession) {
        // An old WebView response must not clear a replacement session.
        if (saved !== session) return
        lostOrigin = session.origin()
        saved = null
        prefs.edit().remove("session").apply()
    }

    fun manualSignIn() {
        lostOrigin = null
        prefs.edit().clear().apply()
    }

    private fun revoke(session: DashboardSession) {
        viewModelScope.launch(NonCancellable + Dispatchers.IO) { runCatching { session.revoke() } }
    }

    override fun onCleared() { cancel() }
}

internal fun supportsDashboardBridge() =
    WebViewFeature.isFeatureSupported(WebViewFeature.WEB_MESSAGE_LISTENER) &&
        WebViewFeature.isFeatureSupported(WebViewFeature.DOCUMENT_START_SCRIPT)

internal fun dashboardLoadUrl(dashboard: String, externalAuth: Boolean): String {
    if (!externalAuth) return dashboard
    val uri = Uri.parse(dashboard)
    val pairs = uri.encodedQuery.orEmpty().split('&').filter { it.isNotEmpty() && Uri.decode(it.substringBefore('=')) != "external_auth" }
    return uri.buildUpon().encodedQuery((pairs + "external_auth=1").joinToString("&")).build().toString()
}

internal fun DashboardAuth.installBridge(view: WebView, session: DashboardSession) {
    val server = Uri.parse(session.origin())
    val origins = setOf(session.origin())
    WebViewCompat.addWebMessageListener(view, "externalAppV2", origins) { _, message, source, mainFrame, reply ->
        if (!mainFrame || source.scheme != "https" || !source.host.equals(server.host, ignoreCase = true) ||
            (source.port.takeIf { it != -1 } ?: 443) != (server.port.takeIf { it != -1 } ?: 443) || saved !== session) {
            return@addWebMessageListener
        }
        val request = runCatching { JSONObject(message.data ?: "") }.getOrNull() ?: return@addWebMessageListener
        val payload = request.optJSONObject("payload") ?: return@addWebMessageListener
        val type = request.optString("type")
        if (type == "externalBus" && payload.optString("type") == "config/get") {
            // HA waits for capabilities before it asks for a token. Encore exposes no companion commands.
            val id = payload.optInt("id")
            if (id > 0) reply.postMessage(JSONObject().put("type", "config").put("id", id).toString())
            return@addWebMessageListener
        }
        val callback = when (type) {
            "getExternalAuth" -> "externalAuthSetToken"
            "revokeExternalAuth" -> "externalAuthRevokeToken"
            else -> return@addWebMessageListener
        }
        if (callback != payload.optString("callback")) return@addWebMessageListener
        viewModelScope.launch {
            val result = withContext(Dispatchers.IO) {
                runCatching {
                    if (type == "getExternalAuth") JSONObject(session.access(payload.optBoolean("force")))
                    else { session.revoke(); null }
                }
            }
            if (saved !== session) return@launch
            // Reply to the requesting document, never evaluate token-bearing JavaScript in a later page.
            runCatching { reply.postMessage(JSONObject().put("type", type).put("ok", result.isSuccess)
                .put("token", result.getOrNull()).toString()) }
            if (withContext(Dispatchers.IO) { session.expired() }) sessionLost(session)
        }
    }
    WebViewCompat.addDocumentStartJavaScript(view, """
        if (window === window.top) window.externalAppV2.onmessage = (event) => {
            const message = JSON.parse(event.data);
            if (message.type === 'config') window.externalBus?.({type: 'result', id: message.id, success: true, result: {}});
            if (message.type === 'getExternalAuth') window.externalAuthSetToken?.(message.ok, message.token);
            if (message.type === 'revokeExternalAuth') window.externalAuthRevokeToken?.(message.ok);
        };
    """.trimIndent(), origins)
}

@Composable
internal fun DashboardEnrollmentDialog(device: String, origin: String, qr: Bitmap?, account: String?, error: String,
    onCancel: () -> Unit, onAccept: () -> Unit, onRetry: () -> Unit) {
    AlertDialog(onDismissRequest = onCancel, title = { Text("Sign in with your phone") },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                Text("$device · $origin")
                if (account != null) {
                    Text("Home Assistant signed in as $account. Use this account only if it's your intended satellite account.")
                } else if (error.isEmpty()) {
                    Text("Connect your phone to the same local network. Scan the QR code and open it in a private browser tab if your phone uses a different Home Assistant account. Check the HTTPS address, then sign in as your non-admin satellite account. Return here to confirm.")
                    Text("The QR code expires after five minutes. It doesn't contain a password or token.")
                    if (qr == null) CircularProgressIndicator() else Image(qr.asImageBitmap(),
                        "Home Assistant phone sign-in QR code", Modifier.size(260.dp))
                }
                if (error.isNotEmpty()) Text(error, color = MaterialTheme.colorScheme.error)
            }
        },
        dismissButton = { TextButton(onClick = onCancel) { Text("Cancel") } },
        confirmButton = {
            if (account != null) TextButton(onClick = onAccept) { Text("Use this account") }
            else if (error.isNotEmpty()) TextButton(onClick = onRetry) { Text("Try again") }
        })
}
