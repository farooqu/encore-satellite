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
import android.text.format.DateUtils
import com.encore.engine.mobile.Mobile
import com.encore.engine.mobile.PresentationObserver
import com.encore.engine.mobile.Runtime
import com.encore.satellite.AudioIo
import org.json.JSONObject
import java.net.Inet4Address

internal data class MicState(
    val endpoint: String = "", val problem: String = "", val status: String = "Ready to talk",
    val canTalk: Boolean = false, val active: Boolean = false, val transcript: String = "", val armed: Boolean = false,
    val wakeWord: Boolean = false, val pendingTalk: Boolean = false,
    val countdowns: String = "", val ringing: String = "",
)

internal fun MicState.withTimers(snapshot: JSONObject): MicState {
    val timers = snapshot.optJSONArray("timers")
    val countdowns = (0 until (timers?.length() ?: 0)).mapNotNull { i ->
        val timer = timers?.optJSONObject(i) ?: return@mapNotNull null
        val seconds = timer.optLong("seconds")
        if (seconds <= 0) return@mapNotNull null
        "${timer.optString("name").ifBlank { "Timer" }} · ${DateUtils.formatElapsedTime(seconds)}"
    }.joinToString("\n")
    val ring = snapshot.optJSONObject("ring")
    val names = ring?.optJSONArray("names")
    val ringing = if (!snapshot.optBoolean("alert")) "" else if (names != null && names.length() > 0) {
        val labels = (0 until names.length()).joinToString(", ") { names.optString(it).ifBlank { "Timer" } }
        "Timers ringing: $labels"
    } else {
        val count = ring?.optInt("count") ?: 0
        val label = if (count > 1) "$count timers ringing" else "Timer ringing"
        val name = ring?.optString("name").orEmpty()
        if (name.isBlank()) label else "$label: $name"
    }
    return copy(countdowns = countdowns, ringing = ringing)
}

@Suppress("DEPRECATION") // Include physical Wi-Fi when a VPN is the default network.
internal fun ConnectivityManager.wifiAddress(address: String? = null): Pair<Network, Inet4Address>? =
    allNetworks.firstNotNullOfOrNull { network ->
        val capabilities = getNetworkCapabilities(network)
        if (capabilities?.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) != true ||
            !capabilities.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_VPN)) return@firstNotNullOfOrNull null
        getLinkProperties(network)?.linkAddresses?.map { it.address }?.filterIsInstance<Inet4Address>()
            ?.firstOrNull { !it.isLinkLocalAddress && (address == null || it.hostAddress == address) }
            ?.let { network to it }
    }

internal fun Runtime.incubatorService(network: Network) = NsdServiceInfo().apply {
    serviceName = name()
    serviceType = "_voice-incubator._tcp."
    port = Mobile.Port.toInt()
    setAttribute("mac", mac())
    setAttribute("protocol", "2")
    if (Build.VERSION.SDK_INT >= 33) setNetwork(network)
}

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
    private var advertisedNetwork: Network? = null
    private var networkCallback: ConnectivityManager.NetworkCallback? = null
    private var peer = ""
    private var localAddress = ""
    private var lastHealthy = 0L
    private var talkDeadline = 0L
    private var capturing = false
    internal var state = MicState()
        private set

    override fun onBind(intent: Intent) = binder

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val action = intent?.action
        if (action != WAKE_WORD && action != TALK && action != SHORTCUT) {
            pause()
        } else {
            if (action == SHORTCUT && PersonalAssistant.ready?.session?.awaitTalk(this, intent!!.getLongExtra(SHORTCUT_ID, 0)) != true) {
                if (runtime == null) stopSelf()
                return START_NOT_STICKY
            }
            try {
                if (runtime == null) openVoice(intent?.getStringExtra(SERVER_IP).orEmpty(), wakeWord = action == WAKE_WORD)
                if (action == WAKE_WORD) setWakeWord(true) else talk()
            } catch (error: Exception) {
                pause(error.message ?: "Could not start the assistant microphone")
            } catch (_: LinkageError) {
                pause("Native voice engine unavailable")
            }
        }
        return START_NOT_STICKY
    }

    private fun openVoice(peer: String, wakeWord: Boolean) {
        check(peer.isNotEmpty()) { "Set up an Incubator server first" }
        check(getSystemService(NotificationManager::class.java).areNotificationsEnabled()) {
            "Enable notifications so you can pause the microphone from outside the app"
        }
        val notification = notification()
        if (Build.VERSION.SDK_INT >= 30) startForeground(1, notification,
            ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE or ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PLAYBACK)
        else startForeground(1, notification)

        val io = AudioIo(this).also { audio = it }
        val engine = Mobile.open(noBackupFilesDir.resolve("voice").absolutePath, io).also { runtime = it }
        engine.setWakeWordEnabled(wakeWord)
        this.peer = peer
        val callback = object : ConnectivityManager.NetworkCallback() {
            private fun retry() {
                handler.post {
                    if (networkCallback === this) {
                        handler.removeCallbacks(refreshRoute)
                        handler.post(refreshRoute)
                    }
                }
            }
            override fun onAvailable(network: Network) { retry() }
            override fun onLost(network: Network) { retry() }
            override fun onLinkPropertiesChanged(network: Network, properties: LinkProperties) { retry() }
        }
        connectivity.registerDefaultNetworkCallback(callback)
        networkCallback = callback
        // Keep an explicit wake session alive while the screen is off.
        wakeLock = getSystemService(PowerManager::class.java)
            .newWakeLock(PowerManager.PARTIAL_WAKE_LOCK, "$packageName:voice").apply { acquire() }
        state = MicState(armed = true, wakeWord = wakeWord, status = "Waiting for Incubator")
        armedMic = this
        engine.observePresentation(PresentationObserver { observed ->
            handler.post {
                if (runtime === engine) {
                    // Keep wake/focus decisions live, but deliver transcript snapshots in order.
                    val current = snapshot()
                    refreshVoice(current)
                    val live = VoiceTurn.from(current)
                    if (talkDeadline != 0L && live?.canPresent == true) {
                        talkDeadline = 0
                        state = state.copy(pendingTalk = false)
                        PersonalAssistant.ready?.session?.beginTalk(this, live)
                    }
                    PersonalAssistant.ready?.session?.update(this, JSONObject(observed), live)
                }
            }
        })
        handler.post(refreshRoute)
        handler.post(tick)
    }

    private val refreshRoute = object : Runnable {
        override fun run() {
            val engine = runtime ?: return
            val result = runCatching { engine.refreshRoute(peer) }
            val address = result.getOrDefault("")
            val wifi = address.takeIf { it.isNotEmpty() }?.let { connectivity.wifiAddress(it)?.first }
            if (address != localAddress || wifi != advertisedNetwork) {
                unadvertise()
                state = state.copy(problem = "")
                localAddress = address
                advertisedNetwork = wifi
                if (wifi != null) advertise(engine, wifi)
            }
            state = state.copy(endpoint = if (address.isEmpty()) "Server: $peer" else
                "${engine.name()} · $address:${Mobile.Port}\nServer: $peer")
            if (result.isFailure) state = state.copy(problem = "Could not open the voice connection. Retrying…")
            handler.postDelayed(this, 5000)
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
            .setContentTitle("Encore voice is active")
            .setContentText("Tap Stop voice to end the request or turn off wake-word listening.")
            .setVisibility(Notification.VISIBILITY_PRIVATE).setContentIntent(open).setDeleteIntent(pause).setOngoing(true)
            .addAction(Notification.Action.Builder(null, "Stop voice", pause).build()).build()
    }

    private fun advertise(engine: Runtime, network: Network) {
        val listener = object : NsdManager.RegistrationListener {
            override fun onServiceRegistered(info: NsdServiceInfo) {}
            override fun onServiceUnregistered(info: NsdServiceInfo) {}
            override fun onRegistrationFailed(info: NsdServiceInfo, code: Int) {
                handler.post { if (registration === this) state = state.copy(problem = "Discovery unavailable. Add the phone by address.") }
            }
            override fun onUnregistrationFailed(info: NsdServiceInfo, code: Int) {}
        }
        runCatching {
            getSystemService(NsdManager::class.java).registerService(engine.incubatorService(network), NsdManager.PROTOCOL_DNS_SD, listener)
            registration = listener
        }.onFailure { state = state.copy(problem = "Discovery unavailable. Add the phone by address.") }
    }

    private fun unadvertise() {
        registration?.let { runCatching { getSystemService(NsdManager::class.java).unregisterService(it) } }
        registration = null
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
                pause("The mic was turned off because its notification is disabled.")
                return
            }
            if (capturing && now - lastHealthy > 2000) {
                pause("${io.captureProblem}. The mic is off.")
                return
            }
            val snapshot = JSONObject(engine.snapshot(ready))
            if (!capturing && (state.wakeWord || (talkDeadline != 0L && snapshot.optBoolean("connected")))) {
                capturing = true
                lastHealthy = now
                try {
                    io.startCapture({ pcm -> runCatching { engine.capture(pcm) } },
                        { handler.post { if (audio === io) pause("Microphone unavailable. Check Android privacy settings.") } })
                } catch (_: Exception) {
                    pause("Microphone unavailable. Check Android privacy settings.")
                    return
                }
            }
            if (talkDeadline != 0L && now >= talkDeadline) {
                cancelPendingTalk("Couldn't reach Incubator. Try again when connected.")
                if (runtime !== engine) return
            }
            if (talkDeadline != 0L && snapshot.optBoolean("talk_ready")) {
                startTalk(engine)
                if (runtime === engine) handler.post(this)
                return
            }
            if (!state.wakeWord && talkDeadline == 0L && !snapshot.optBoolean("active")) {
                // The observer has already queued the turn's final transcript under the engine lock.
                handler.post {
                    if (runtime === engine && !state.wakeWord && talkDeadline == 0L && !snapshot().optBoolean("active"))
                        pause(keepTranscript = true)
                }
                handler.postDelayed(this, 150)
                return
            }
            val active = refreshVoice(snapshot)
            state = state.copy(active = active, canTalk = snapshot.optBoolean("talk_ready"),
                status = when {
                    !snapshot.optBoolean("connected") -> "Waiting for Incubator"
                    !ready -> "Waiting for microphone"
                    active -> when (snapshot.optString("phase")) {
                        "tentative", "listening" -> "Listening to your request"
                        "speaking" -> "Replying"
                        else -> "Working"
                    }
                    snapshot.optBoolean("wake_ready") -> "Listening for your wake word"
                    !state.wakeWord -> "Ready to talk"
                    snapshot.optInt("wake_models") == 0 -> "No wake model · Talk is available"
                    else -> "Preparing wake detection"
                }, transcript = if (active) listOf(snapshot.optString("heard"), snapshot.optString("reply"))
                    .filter { it.isNotEmpty() }.joinToString("\n\n") else "").withTimers(snapshot)
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

    private fun talk() {
        val engine = runtime ?: return
        if (voiceTurn()?.canPresent == true || talkDeadline != 0L) return
        if (snapshot().optBoolean("talk_ready")) startTalk(engine)
        else {
            talkDeadline = SystemClock.elapsedRealtime() + 65_000
            state = state.copy(pendingTalk = true, problem = "")
        }
    }

    private fun startTalk(engine: Runtime) {
        talkDeadline = 0
        state = state.copy(pendingTalk = false, problem = "")
        runCatching { engine.talk() }
            .onSuccess {
                voiceTurn()?.let { PersonalAssistant.ready?.session?.beginTalk(this, it) }
            }
            .onFailure { cancelPendingTalk("Talk unavailable. Try again when connected.") }
    }

    internal fun cancelPendingTalk(problem: String = "") {
        if (talkDeadline == 0L && problem.isEmpty()) return
        talkDeadline = 0
        state = state.copy(pendingTalk = false, problem = problem)
        if (!state.wakeWord && !snapshot().optBoolean("active")) pause(problem)
        else if (problem.isNotEmpty()) PersonalAssistant.ready?.session?.talkUnavailable(this, problem)
    }

    internal fun setWakeWord(enabled: Boolean) {
        runtime?.setWakeWordEnabled(enabled)
        state = state.copy(wakeWord = enabled)
    }

    internal fun stopTurn() { if (state.pendingTalk) cancelPendingTalk() else runtime?.stop() }
    internal fun stopTurn(key: TurnKey) { runtime?.stopTurn(key.generation, key.turn) }

    internal fun pause(problem: String = "", keepTranscript: Boolean = false) {
        shutdown(problem, keepTranscript)
        stopSelf()
    }

    private fun shutdown(problem: String, keepTranscript: Boolean = false) {
        handler.removeCallbacks(tick)
        handler.removeCallbacks(refreshRoute)
        talkDeadline = 0
        capturing = false
        if (problem.isNotEmpty()) PersonalAssistant.ready?.session?.talkUnavailable(this, problem)
        PersonalAssistant.ready?.session?.invalidate(this, keepTranscript)
        if (armedMic === this) {
            armedMic = null
            PersonalAssistant.ready?.update(this, null)
        }
        runtime?.observePresentation(null)
        networkCallback?.let { connectivity.unregisterNetworkCallback(it) }
        networkCallback = null
        unadvertise()
        advertisedNetwork = null
        localAddress = ""
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
        // The service is alive for a manual request or explicit wake-word listening.
        internal var armedMic: MicService? = null
            private set
        const val WAKE_WORD = "com.encore.personal.WAKE_WORD"
        const val TALK = "com.encore.personal.TALK"
        const val SHORTCUT = "com.encore.personal.SHORTCUT"
        const val SHORTCUT_ID = "shortcut_id"
        const val PAUSE = "com.encore.personal.PAUSE"
        const val PREFERENCES = "personal_mic"
        const val SERVER_IP = "server_ip"
    }
}
