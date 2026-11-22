package com.encore.personal

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.app.Service
import android.content.Intent
import android.content.pm.ServiceInfo
import android.media.AudioAttributes
import android.media.AudioFocusRequest
import android.media.AudioManager
import android.net.ConnectivityManager
import android.net.LinkProperties
import android.net.Network
import android.net.NetworkCapabilities
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.os.Binder
import android.os.Build
import android.os.Handler
import android.os.Looper
import android.os.PowerManager
import android.os.SystemClock
import com.encore.engine.mobile.Mobile
import com.encore.engine.mobile.PresentationObserver
import com.encore.engine.mobile.Runtime
import com.encore.satellite.AudioIo
import org.json.JSONObject

internal data class MicState(
    val endpoint: String = "", val problem: String = "", val status: String = "Mic paused",
    val canTalk: Boolean = false, val active: Boolean = false, val transcript: String = "",
)

class MicService : Service() {
    inner class LocalBinder : Binder() { val service get() = this@MicService }
    private val binder = LocalBinder()
    private val handler = Handler(Looper.getMainLooper())
    private val connectivity by lazy { getSystemService(ConnectivityManager::class.java) }
    private val audioManager by lazy { getSystemService(AudioManager::class.java) }
    private val focus = AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN_TRANSIENT_MAY_DUCK)
        .setAudioAttributes(AudioAttributes.Builder().setUsage(AudioAttributes.USAGE_ASSISTANT)
            .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH).build())
        .setOnAudioFocusChangeListener { if (it < 0) stopTurn() }.build()
    private var focusHeld = false
    private var wantsFocus = false
    private var runtime: Runtime? = null
    private var audio: AudioIo? = null
    private var wakeLock: PowerManager.WakeLock? = null
    private var registration: NsdManager.RegistrationListener? = null
    private var networkCallback: ConnectivityManager.NetworkCallback? = null
    private var lastHealthy = 0L
    internal var state = MicState()
        private set

    override fun onBind(intent: Intent) = binder

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action != ARM) {
            pause()
        } else if (runtime == null) {
            try {
                arm(intent.getStringExtra(SERVER_IP).orEmpty())
            } catch (error: Exception) {
                pause(error.message ?: "Could not start the assistant microphone")
            } catch (_: LinkageError) {
                pause("Native voice engine unavailable")
            }
        }
        return START_NOT_STICKY
    }

    private fun arm(peer: String) {
        val network = checkNotNull(connectivity.activeNetwork) { "Connect to trusted home Wi-Fi first" }
        val wifi = checkNotNull(wifiAddress(network)) { "Connect to trusted home Wi-Fi with a private IPv4 address first" }
        check(getSystemService(NotificationManager::class.java).areNotificationsEnabled()) {
            "Enable notifications so you can pause the microphone from outside the app"
        }
        val notification = notification()
        if (Build.VERSION.SDK_INT >= 30) startForeground(1, notification,
            ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE or ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PLAYBACK)
        else startForeground(1, notification)

        val io = AudioIo(this).also { audio = it }
        val engine = Mobile.open(noBackupFilesDir.resolve("voice").absolutePath, io).also { runtime = it }
        val callback = object : ConnectivityManager.NetworkCallback() {
            private fun verify() = handler.post {
                if (networkCallback === this && (connectivity.activeNetwork != network || wifiAddress(network) != wifi)) {
                    pause("Wi-Fi changed. Arm again on your trusted network.")
                }
            }
            override fun onLost(network: Network) { verify() }
            override fun onLinkPropertiesChanged(network: Network, properties: LinkProperties) { verify() }
            override fun onCapabilitiesChanged(network: Network, capabilities: NetworkCapabilities) { verify() }
        }
        connectivity.registerDefaultNetworkCallback(callback)
        networkCallback = callback
        check(connectivity.activeNetwork == network && wifiAddress(network) == wifi) { "Wi-Fi changed; try again" }
        engine.start(wifi, peer)
        advertise(engine)
        // Match the explicit arm lifetime, including when the screen is off.
        wakeLock = getSystemService(PowerManager::class.java)
            .newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "$packageName:armed").apply { acquire() }
        io.startCapture({ pcm ->
            // A peer can disconnect during a write; the protocol fences the old link.
            runCatching { engine.capture(pcm) }
        }, { handler.post { if (audio === io) pause("Microphone stopped. Check Android privacy settings and arm again.") } })
        lastHealthy = SystemClock.elapsedRealtime()
        state = MicState(endpoint = "${engine.name()} · $wifi:${Mobile.Port}", status = "Waiting for Incubator")
        armedMic = this
        engine.observePresentation(PresentationObserver { observed ->
            handler.post {
                if (runtime === engine) {
                    // Keep wake/focus decisions live, but deliver transcript snapshots in order.
                    val current = snapshot()
                    refreshVoice(current)
                    PersonalAssistant.ready?.session?.update(this, JSONObject(observed), VoiceTurn.from(current))
                }
            }
        })
        handler.post(tick)
    }

    private fun wifiAddress(network: Network): String? {
        val capabilities = connectivity.getNetworkCapabilities(network) ?: return null
        if (!capabilities.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) ||
            !capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)) return null
        return connectivity.getLinkProperties(network)?.linkAddresses?.firstNotNullOfOrNull {
            it.address.hostAddress?.takeIf(Mobile::isPrivateIPv4)
        }
    }

    private fun notification(): Notification {
        val manager = getSystemService(NotificationManager::class.java)
        manager.createNotificationChannel(NotificationChannel("mic", "Assistant microphone", NotificationManager.IMPORTANCE_LOW))
        check(manager.getNotificationChannel("mic").importance != NotificationManager.IMPORTANCE_NONE) {
            "Enable the Assistant microphone notification first"
        }
        val open = PendingIntent.getActivity(this, 0, Intent(this, MicActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val pause = PendingIntent.getService(this, 1, Intent(this, MicService::class.java).setAction(PAUSE), PendingIntent.FLAG_IMMUTABLE)
        return Notification.Builder(this, "mic").setSmallIcon(R.drawable.ic_mic)
            .setContentTitle(getString(R.string.assistant_armed))
            .setContentText("Microphone on for local wake detection. Pause to turn it off.")
            .setVisibility(Notification.VISIBILITY_PRIVATE).setContentIntent(open).setDeleteIntent(pause).setOngoing(true)
            .addAction(Notification.Action.Builder(null, "Pause mic", pause).build()).build()
    }

    private fun advertise(engine: Runtime) {
        val listener = object : NsdManager.RegistrationListener {
            override fun onServiceRegistered(info: NsdServiceInfo) {}
            override fun onServiceUnregistered(info: NsdServiceInfo) {}
            override fun onRegistrationFailed(info: NsdServiceInfo, code: Int) {
                handler.post { if (registration === this) state = state.copy(problem = "Discovery unavailable. Add the phone by address.") }
            }
            override fun onUnregistrationFailed(info: NsdServiceInfo, code: Int) {}
        }
        getSystemService(NsdManager::class.java).registerService(NsdServiceInfo().apply {
            serviceName = engine.name()
            serviceType = "_voice-incubator._tcp."
            port = Mobile.Port.toInt()
            setAttribute("mac", engine.mac())
            setAttribute("protocol", "2")
        }, NsdManager.PROTOCOL_DNS_SD, listener)
        registration = listener
    }

    private val tick = object : Runnable {
        override fun run() {
            val engine = runtime ?: return
            val io = audio ?: return
            val now = SystemClock.elapsedRealtime()
            val ready = io.captureReady
            if (ready) lastHealthy = now
            if (!getSystemService(NotificationManager::class.java).areNotificationsEnabled() ||
                getSystemService(NotificationManager::class.java).getNotificationChannel("mic")?.importance == NotificationManager.IMPORTANCE_NONE) {
                pause("Listening notification disabled. Mic paused.")
                return
            }
            if (now - lastHealthy > 2000) {
                pause("${io.captureProblem}. Mic paused.")
                return
            }
            val snapshot = JSONObject(engine.snapshot(ready))
            val active = refreshVoice(snapshot)
            state = state.copy(active = active, canTalk = snapshot.optBoolean("talk_ready"),
                status = when {
                    !ready -> "Waiting for microphone"
                    !snapshot.optBoolean("connected") -> "Waiting for Incubator"
                    active -> when (snapshot.optString("phase")) {
                        "tentative", "listening" -> "Listening to your request"
                        "speaking" -> "Replying"
                        else -> "Working"
                    }
                    snapshot.optBoolean("wake_ready") -> "Listening for your wake word"
                    snapshot.optInt("wake_models") == 0 -> "No wake model · Talk is available"
                    else -> "Preparing wake detection"
                }, transcript = if (active) listOf(snapshot.optString("heard"), snapshot.optString("reply"))
                    .filter { it.isNotEmpty() }.joinToString("\n\n") else "")
            handler.postDelayed(this, 150)
        }
    }

    private fun refreshVoice(snapshot: JSONObject): Boolean {
        val turn = VoiceTurn.from(snapshot)
        val owner = snapshot.optJSONObject("output")?.optString("owner")
        // A candidate streams its query during arbitration, but does not own
        // the phone's audio focus or full panel until Incubator accepts it.
        val focusNeeded = turn?.canPresent == true || (snapshot.optBoolean("active") &&
            (snapshot.optBoolean("alert") || snapshot.optBoolean("announcing") || owner == "announcement" || owner == "alert"))
        if (focusNeeded != wantsFocus) {
            wantsFocus = focusNeeded
            if (focusNeeded) {
                focusHeld = audioManager.requestAudioFocus(focus) == AudioManager.AUDIOFOCUS_REQUEST_GRANTED
                if (!focusHeld) {
                    if (turn != null) stopTurn(turn.key) else stopTurn()
                    PersonalAssistant.ready?.session?.invalidate(this)
                    PersonalAssistant.ready?.update(this, null)
                    return false
                }
            } else if (focusHeld) {
                audioManager.abandonAudioFocusRequest(focus)
                focusHeld = false
            }
        }
        PersonalAssistant.ready?.update(this, turn)
        return focusNeeded
    }

    private fun snapshot(): JSONObject = runtime?.let { JSONObject(it.snapshot(audio?.captureReady == true)) } ?: JSONObject()

    internal fun voiceTurn(): VoiceTurn? = VoiceTurn.from(snapshot())

    internal fun talk() {
        if (!snapshot().optBoolean("talk_ready")) return
        runCatching { runtime?.talk() }.onFailure { state = state.copy(problem = "Talk unavailable. Try again when connected.") }
    }

    internal fun stopTurn() { runtime?.stop() }
    internal fun stopTurn(key: TurnKey) { runtime?.stopTurn(key.generation, key.turn) }

    internal fun pause(problem: String = "") {
        shutdown(problem)
        stopSelf()
    }

    private fun shutdown(problem: String) {
        handler.removeCallbacks(tick)
        if (armedMic === this) {
            armedMic = null
            PersonalAssistant.ready?.session?.invalidate(this)
            PersonalAssistant.ready?.update(this, null)
        }
        runtime?.observePresentation(null)
        networkCallback?.let { connectivity.unregisterNetworkCallback(it) }
        networkCallback = null
        registration?.let { runCatching { getSystemService(NsdManager::class.java).unregisterService(it) } }
        registration = null
        audio?.stopCapture()
        runtime?.close()
        runtime = null
        audio?.release()
        audio = null
        wakeLock?.let { if (it.isHeld) it.release() }
        wakeLock = null
        if (focusHeld) audioManager.abandonAudioFocusRequest(focus)
        focusHeld = false
        wantsFocus = false
        state = MicState(problem = problem)
        stopForeground(STOP_FOREGROUND_REMOVE)
    }

    override fun onDestroy() { shutdown(""); super.onDestroy() }

    companion object {
        // Voice interaction services run in the same process and never arm it.
        internal var armedMic: MicService? = null
            private set
        const val ARM = "com.encore.personal.ARM"
        const val PAUSE = "com.encore.personal.PAUSE"
        const val SERVER_IP = "server_ip"
    }
}
