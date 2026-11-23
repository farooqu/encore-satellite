package com.encore.satellite

import android.Manifest
import android.app.*
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.IntentFilter
import android.content.pm.PackageManager
import android.content.pm.ServiceInfo
import android.media.AudioAttributes
import android.media.AudioFocusRequest
import android.media.AudioManager
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.os.Binder
import android.os.Build
import android.os.IBinder
import androidx.core.content.ContextCompat
import com.encore.engine.mobile.Mobile
import com.encore.engine.mobile.Runtime
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock

/** Dashboard Activity lifetime/reloads never own the microphone or network sockets. */
class EncoreService : Service() {
    inner class LocalBinder : Binder() { val service: EncoreService get() = this@EncoreService }
    private val binder = LocalBinder()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private val mutable = MutableStateFlow(EncoreState())
    val state = mutable.asStateFlow()
    private lateinit var audio: AudioIo
    private var runtime: Runtime? = null
    private var updates: ApkUpdates? = null
    lateinit var bluetooth: BluetoothHost
        private set
    private var bluetoothRefresh = 0L
    private val bluetoothSetting = Mutex()
    private val advertisements = mutableListOf<NsdManager.RegistrationListener>()
    private var espHomeAdvertisement: NsdManager.RegistrationListener? = null
    private var musicHost: MusicHost? = null
    private var music: MusicPlayer? = null
    private var musicRetry: Job? = null
    private val musicPrefs by lazy { getSharedPreferences("sendspin", MODE_PRIVATE) }
    private val musicDiscovery by lazy { MusicDiscovery(this, ::musicDiscovered) }
    private val musicRoute = object : BroadcastReceiver() {
        override fun onReceive(context: Context, intent: Intent) {
            if (intent.action == AudioManager.ACTION_AUDIO_BECOMING_NOISY) stopMusic()
        }
    }
    private var light: LightSensorHost? = null
    private var microphoneForeground = false
    internal var awaitingUpdateMicrophone = false
        private set
    private var monitorScreenVisible = false
    private val focus by lazy {
        SatelliteAudioFocus(getSystemService(AudioManager::class.java)) { focused ->
            mutable.value = mutable.value.copy(audioInterrupted = !focused)
            music?.interrupted(!focused)
            if (!focused) audio.clearVisualization()
            runtime?.setPlaybackInterrupted(!focused)
        }
    }

    override fun onCreate() {
        super.onCreate()
        audio = AudioIo(this)
        bluetooth = BluetoothHost(this)
        mutable.value = mutable.value.copy(manualMusicServer = musicPrefs.getString("manual_server", null))
        ContextCompat.registerReceiver(this, musicRoute, IntentFilter(AudioManager.ACTION_AUDIO_BECOMING_NOISY),
            ContextCompat.RECEIVER_NOT_EXPORTED)
        runCatching {
            Mobile.openWithBluetoothDefault(filesDir.resolve("engine").absolutePath, audio, bluetooth.supported).also {
                it.configureHardware(Build.MANUFACTURER.orEmpty(), Build.MODEL.orEmpty())
                it.configureDashboard()
                it.configureSendspin({ enabled ->
                    scope.launch {
                        if (mutable.value.running && enabled != musicEnabled()) {
                            if (enabled) enableMusic(mutable.value.manualMusicServer) else disconnectMusic()
                        }
                    }
                }, musicEnabled())
            }
        }
            .onSuccess {
                runtime = it
                bluetooth.attach(it)
                mutable.value = mutable.value.copy(name = it.name(), espHomeFriendlyName = it.espHomeFriendlyName())
                updates = ApkUpdates(this, it, scope)
            }
            .onFailure { problem("Engine could not start: ${it.message}") }
        scope.launch {
            while (isActive) {
                val now = android.os.SystemClock.elapsedRealtime()
                if (now - bluetoothRefresh >= 500) { bluetooth.refresh(); bluetoothRefresh = now }
                runtime?.let { engine ->
                    val microphoneProblem = audio.captureProblem
                    runCatching { EncoreState.parse(engine.snapshot(microphoneProblem.isEmpty()), mutable.value) }
                        .onSuccess { snapshot ->
                            if (snapshot.monitorId != 0L && (!monitorScreenVisible || !snapshot.microphoneVisible)) {
                                engine.setMonitorVisible(snapshot.monitorId, false)
                                return@onSuccess
                            }
                            val frame = audio.microphoneFrame(snapshot.generation, snapshot.turn, snapshot.microphoneVisible)
                            val next = snapshot.copy(level = frame.level, waveform = frame.waveform,
                                playbackLevel = if (snapshot.visual != "none" && snapshot.outputState in setOf("playing", "draining")) audio.playbackLevel() else null,
                                captureProblem = microphoneProblem.ifEmpty { "Microphone unavailable" })
                            val newVoiceActivity = next.voiceActive && !mutable.value.voiceActive
                            val espHomeChanged = next.esphome != mutable.value.esphome
                            mutable.value = next
                            if (espHomeChanged && next.running) {
                                updateAdvertisements(engine, next.esphome)
                                if (next.esphome == "paired") {
                                    if (microphoneForeground && !next.muted) {
                                        runCatching { startMicrophone() }.onFailure {
                                            setMuted(true)
                                            problem("Microphone unavailable. Check permission and privacy controls before unmuting.")
                                        }
                                    }
                                    if (musicEnabled() && !mutable.value.music.enabled) startMusic()
                                }
                            }
                            val current = mutable.value
                            focus.update(current.voiceActive || (current.music.playing && !current.music.stopped),
                                resume = newVoiceActivity && current.voiceActive)
                            music?.duck(current.voiceActive)
                        }.onFailure {
                            engine.setMonitorVisible(mutable.value.monitorId, false)
                            audio.clearVisualization()
                            mutable.value = mutable.value.copy(voiceKnown = false, level = 0f, waveform = emptyList(), playbackLevel = null,
                                heard = "", reply = "", speakerName = "", speakerRGB = emptyList())
                            problem("Engine status unavailable")
                        }
                }
                delay(40)
            }
        }
    }

    override fun onBind(intent: Intent): IBinder = binder

    fun monitorScreenVisible(visible: Boolean) {
        monitorScreenVisible = visible
        if (!visible) setMonitorOverlay(mutable.value.monitorId, false)
    }

    fun setMonitorOverlay(id: Long, visible: Boolean) {
        val current = mutable.value
        runtime?.setMonitorVisible(id, visible && monitorScreenVisible && current.monitorId == id &&
            current.microphoneVisible && audio.captureReady)
    }

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == "stop") { ApkInstall(this).cancelReconnect(); shutdown(); stopSelf(); return START_NOT_STICKY }
        val updateReconnect = intent?.action == UPDATE_RECONNECT
        if (updateReconnect && mutable.value.running) return START_NOT_STICKY
        if (updateReconnect) awaitingUpdateMicrophone = true
        val bluetoothStart = intent?.action == "bluetooth"
        if (bluetoothStart && mutable.value.running) {
            runCatching { setBluetoothEnabled(true) }.onFailure { problem("Could not save Bluetooth setting") }
            return START_NOT_STICKY
        }
        val channel = NotificationChannel("encore", "Encore satellite", NotificationManager.IMPORTANCE_LOW)
        getSystemService(NotificationManager::class.java).createNotificationChannel(channel)
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val stop = PendingIntent.getService(this, 1, Intent(this, EncoreService::class.java).setAction("stop"), PendingIntent.FLAG_IMMUTABLE)
        val notification = Notification.Builder(this, "encore").setSmallIcon(R.drawable.ic_encore)
            .setContentTitle("Encore Incubator").setContentText(if (updateReconnect) "Encore updated. Open Encore to restore microphone access."
                else "Satellite running; voice and music controls are available in the app")
            .setContentIntent(open).setOngoing(true).addAction(Notification.Action.Builder(null, "Stop satellite", stop).build()).build()
        try {
            val enableMicrophone = !updateReconnect && intent?.action != "music" && !bluetoothStart && checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED
            microphoneForeground = microphoneForeground || enableMicrophone
            val bluetoothReady = bluetooth.state.value.supported && bluetooth.state.value.permitted
            check(!bluetoothStart || bluetoothReady) { "Bluetooth permission or implementation unavailable" }
            if (Build.VERSION.SDK_INT >= 30) startForeground(1, notification,
                (when {
                    updateReconnect && Build.VERSION.SDK_INT >= 34 -> ServiceInfo.FOREGROUND_SERVICE_TYPE_SPECIAL_USE
                    bluetoothStart -> ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE
                    updateReconnect -> ServiceInfo.FOREGROUND_SERVICE_TYPE_NONE // pre-34 has no type requirement for this work
                    else -> ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PLAYBACK
                }) or (if (musicEnabled()) ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PLAYBACK else 0) or
                    (if (microphoneForeground) ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE else 0))
            else startForeground(1, notification)
            val engine = checkNotNull(runtime) { "Engine unavailable" }
            if (updateReconnect) engine.suspendCapture(true)
            if (enableMicrophone && awaitingUpdateMicrophone) {
                engine.suspendCapture(false)
                awaitingUpdateMicrophone = false
            }
            if (!mutable.value.running) {
                // Optional sensor failure never prevents voice/music startup.
                light = runCatching { LightSensorHost.create(this) }.getOrNull()
                engine.setLightHost(light)
                if (!microphoneForeground && !bluetoothStart && !updateReconnect) engine.setMuted(true)
                engine.start()
                updates?.start()
                mutable.value = mutable.value.copy(running = true)
                bluetooth.running(true)
                val esphome = EncoreState.parse(engine.snapshot(false), mutable.value).esphome
                mutable.value = mutable.value.copy(esphome = esphome)
                updateAdvertisements(engine, esphome)
            }
            val unmute = enableMicrophone && intent?.getBooleanExtra("unmute", false) == true
            if (unmute) engine.setMuted(false)
            if (microphoneForeground && !EncoreState.parse(engine.snapshot(audio.captureReady), mutable.value).muted) startMicrophone()
            if (unmute) musicPrefs.edit().remove("privacy").apply()
            if (intent?.action == "music") enableMusic(intent.getStringExtra("manual_server"))
            else if (!mutable.value.music.enabled && musicEnabled()) startMusic()
            if (bluetoothStart) setBluetoothEnabled(true)
        } catch (error: Exception) {
            if (bluetoothStart) {
                problem("Bluetooth could not start: ${error.message}")
                if (!mutable.value.running) { stopForeground(STOP_FOREGROUND_REMOVE); stopSelf(startId) }
                return START_NOT_STICKY
            }
            if (!awaitingUpdateMicrophone) runtime?.let { runCatching { it.setMuted(true) } }
            stopMicrophone()
            problem("Could not start satellite: ${error.message}")
            shutdown()
            if (updateReconnect) stopSelf(startId)
        }
        return START_NOT_STICKY
    }

    private fun startMicrophone() {
        if (mutable.value.esphome != "paired") return
        check(microphoneForeground) { "Start the microphone from the app first" }
        audio.startCapture({ runtime?.capture(it) }, {
            scope.launch { setMuted(true); problem("Microphone unavailable. Check permission and privacy controls before unmuting.") }
        })
        mutable.value = mutable.value.copy(microphoneReady = true)
    }

    private fun stopMicrophone() {
        mutable.value = mutable.value.copy(microphoneReady = false, captureReady = false, level = 0f, waveform = emptyList())
        audio.stopCapture()
    }

    fun setMuted(value: Boolean) {
        if (value) {
            inhibitPresentation("muted", true)
            musicPrefs.edit().putBoolean("privacy", true).putBoolean("stopped", true).apply()
            music?.stopped(true)
        }
        runCatching {
            runtime?.setMuted(value)
            if (value) stopMicrophone() else if (mutable.value.running) startMicrophone()
            if (!value) musicPrefs.edit().remove("privacy").apply()
        }.onFailure {
            runtime?.let { engine -> runCatching { engine.setMuted(true) } }
            stopMicrophone()
            problem("Microphone remains off: ${it.message}")
        }
    }

    fun talk() {
        if (!mutable.value.running || mutable.value.esphome != "paired" || !mutable.value.talkAllowed) return
        runCatching {
            check(mutable.value.microphoneReady && mutable.value.captureReady && !mutable.value.muted) { "Start the microphone from the app first" }
            focus.update(true, resume = true)
            if (focus.focused) runtime?.talk()
        }.onFailure { problem("Voice is not ready: ${it.message}") }
    }
    fun stopTurn() { inhibitPresentation("stopping"); runtime?.stop() }
    fun stopAlert(sequence: Long) { runtime?.stopAlert(sequence) }
    private fun inhibitPresentation(phase: String, muted: Boolean = mutable.value.muted) {
        audio.clearVisualization()
        val current = mutable.value
        val keepAlert = phase == "muted" && current.alertVisible
        val keepOutput = keepAlert && current.outputOwner == "alert"
        mutable.value = current.copy(phase = phase, muted = muted, inhibited = true, voiceActive = keepAlert, announcing = false,
            alert = keepAlert && current.alert, outputState = if (keepOutput) current.outputState else "", outputOwner = if (keepOutput) "alert" else "",
            input = "", inputOpen = false, heard = "", reply = "", speakerName = "", speakerRGB = emptyList(),
            level = 0f, waveform = emptyList(), playbackLevel = null, talkAllowed = false, talkReady = false, wakeReady = false)
    }
    fun setBluetoothEnabled(enabled: Boolean) {
        scope.launch {
            // Preserve tap order without doing filesystem I/O on the UI thread.
            bluetoothSetting.withLock {
                try {
                    val engine = checkNotNull(runtime) { "Engine unavailable" }
                    withContext(Dispatchers.IO) { engine.setBluetoothEnabled(enabled) }
                    bluetooth.refresh()
                } catch (cancelled: CancellationException) {
                    throw cancelled
                } catch (_: Exception) {
                    problem("Could not save Bluetooth setting")
                }
            }
        }
    }
    fun noiseKey(): String = runtime?.noiseKey().orEmpty()
    fun pairHomeAssistant() {
        if (!mutable.value.running || mutable.value.esphome !in setOf("unpaired", "expired")) return
        runCatching { checkNotNull(runtime).pairHomeAssistant() }
            .onFailure { problem("Could not start Home Assistant pairing. Try again.") }
    }
    private fun musicProblem(message: String) { mutable.value = mutable.value.copy(musicProblem = message) }
    private fun musicEnabled() = musicPrefs.getBoolean("enabled", true)
    // A first-run default does not bypass microphone setup to start the service.
    // Once the paired player has started, it can reopen without the microphone.
    internal fun startForSavedMusic() = musicPrefs.getBoolean("enabled", false)
    fun musicToken(): String = runCatching { host().pairingToken() }
        .getOrElse { musicProblem("Sendspin secure storage unavailable"); "" }
    private fun host(): MusicHost = musicHost ?: MusicHost(this).also { musicHost = it }
    private fun enableMusic(server: String?) {
        if (mutable.value.esphome != "paired") {
            musicProblem("Pair with Home Assistant before connecting Sendspin")
            return
        }
        val endpoint = server?.let(MusicEndpoint::manual)
        if (server != null && endpoint == null) {
            musicProblem("Enter a Sendspin host and port, without a scheme or credentials")
            return
        }
        runCatching {
            check(musicPrefs.edit().remove("address").putString("manual_server", endpoint?.setting).putBoolean("enabled", true).commit())
            mutable.value = mutable.value.copy(manualMusicServer = endpoint?.setting)
            runtime?.setSendspinEnabled(true)
            startMusic()
        }.onFailure { musicProblem("Could not save Sendspin setting") }
    }
    private fun startMusic() {
        // Keep discovery and connections off until initial ESPHome pairing succeeds.
        if (!mutable.value.running || mutable.value.esphome != "paired") return
        if (!musicPrefs.contains("enabled")) musicPrefs.edit().putBoolean("enabled", true).apply()
        closeMusic()
        musicProblem("")
        val server = mutable.value.manualMusicServer
        val endpoint = server?.let(MusicEndpoint::manual)
        when {
            endpoint != null -> connectMusic(endpoint)
            server != null -> {
                mutable.value = mutable.value.copy(music = MusicState(enabled = true, awaitingServer = true, status = "Invalid saved server"))
                musicProblem("Edit the saved Sendspin server or use auto-discovery")
            }
            else -> {
                mutable.value = mutable.value.copy(music = MusicState(enabled = true, awaitingServer = true, status = "Looking for a server…"))
                musicDiscovery.start()
            }
        }
    }
    private fun retryMusic() {
        musicRetry?.cancel()
        musicRetry = scope.launch {
            delay(5_000)
            musicRetry = null
            if (musicEnabled()) startMusic()
        }
    }
    private fun musicDiscovered(endpoint: MusicEndpoint?, message: String) {
        if (endpoint == null) {
            mutable.value = mutable.value.copy(music = MusicState(enabled = true, awaitingServer = true, status = "Waiting for a server"))
            musicProblem(message)
            retryMusic()
            return
        }
        connectMusic(endpoint)
    }
    private fun connectMusic(endpoint: MusicEndpoint) {
        runCatching {
            val pairingHost = host()
            MusicPlayer(mutable.value.name, pairingHost, ::retryMusic) { next ->
                mutable.value = mutable.value.copy(music = next)
                focus.update(mutable.value.voiceActive || (next.playing && !next.stopped))
                music?.interrupted(!focus.focused)
            }.also {
                music = it
                it.interrupted(!focus.focused)
                it.stopped(musicPrefs.getBoolean("stopped", false) || musicPrefs.getBoolean("privacy", false))
                it.duck(mutable.value.voiceActive)
                runtime?.setSendspinPairing(pairingHost.pairingClientId, pairingHost.pairingToken())
                it.connect(endpoint)
            }
        }.onFailure {
            closeMusic()
            mutable.value = mutable.value.copy(music = MusicState(enabled = true, status = "Could not start Sendspin"))
            musicProblem("Sendspin unavailable; check secure storage")
        }
    }
    private fun closeMusic() {
        runtime?.setSendspinPairing("", "")
        musicRetry?.cancel()
        musicRetry = null
        musicDiscovery.stop()
        music?.close()
        music = null
        mutable.value = mutable.value.copy(music = MusicState())
        focus.update(mutable.value.voiceActive)
    }
    fun disconnectMusic() {
        closeMusic()
        if (musicPrefs.edit().putBoolean("enabled", false).commit()) {
            runtime?.setSendspinEnabled(false)
            musicProblem("")
        } else musicProblem("Could not save Sendspin setting")
    }
    fun revokeMusic() {
        closeMusic()
        runCatching {
            check(musicPrefs.edit().putBoolean("enabled", false).commit())
            runtime?.setSendspinEnabled(false)
            host().revokeAll()
            musicHost = null
            musicProblem("")
        }.onFailure { musicProblem("Could not revoke Sendspin pairing") }
    }
    private fun stopMusic() {
        music?.stopped(true)
        if (music != null) musicPrefs.edit().putBoolean("stopped", true).apply()
    }
    fun musicCommand(command: String) {
        val player = music ?: return
        when (command) {
            Mobile.SendspinStop -> { stopMusic(); player.command(command) }
            Mobile.SendspinPlay, "resume" -> {
                if (musicPrefs.getBoolean("privacy", false)) { musicProblem("Turn off microphone privacy before playing music"); return }
                if (command == "resume" || (mutable.value.music.stopped && player.playing)) {
                    if (!player.playing) return
                } else if (!player.command(command)) return
                focus.update(true, resume = true)
                if (!focus.focused) return
                musicPrefs.edit().remove("stopped").apply()
                musicProblem("")
                player.stopped(false)
            }
            else -> player.command(command)
        }
    }
    fun dismissProblem() { runtime?.dismissProblem(); mutable.value = mutable.value.copy(problem = "") }
    private fun problem(message: String) { mutable.value = mutable.value.copy(problem = message) }

    private fun updateAdvertisements(engine: Runtime, esphome: String) {
        val manager = getSystemService(NsdManager::class.java)
        espHomeAdvertisement?.let { listener ->
            runCatching { manager.unregisterService(listener) }
            advertisements.remove(listener)
        }
        espHomeAdvertisement = null
        val noise = "Noise_NNpsk0_25519_ChaChaPoly_SHA256"
        val security = when (esphome) {
            "waiting" -> mapOf("api_encryption_supported" to noise, "api_provisioning" to "zero-psk")
            "paired" -> mapOf("api_encryption" to noise)
            else -> return
        }
        espHomeAdvertisement = runCatching {
            advertise(engine.name(), "_esphomelib._tcp.", 6053,
                mapOf("mac" to engine.mac().replace(":", ""), "version" to Mobile.ESPHomeVersion,
                    "friendly_name" to engine.espHomeFriendlyName()) + security)
        }.onFailure { problem("ESPHome discovery unavailable; use the tablet IP") }.getOrNull()
        if (esphome == "paired") runCatching {
            advertise(engine.name(), "_voice-incubator._tcp.", 6055, mapOf("mac" to engine.mac(), "protocol" to "2"))
        }.onFailure { problem("Voice discovery unavailable; use the tablet IP") }
    }

    private fun advertise(name: String, type: String, port: Int, attributes: Map<String, String>): NsdManager.RegistrationListener {
        val manager = getSystemService(NsdManager::class.java)
        val listener = object : NsdManager.RegistrationListener {
            override fun onRegistrationFailed(info: NsdServiceInfo, code: Int) { problem("Discovery unavailable ($code); use the tablet IP") }
            override fun onUnregistrationFailed(info: NsdServiceInfo, code: Int) {}
            override fun onServiceRegistered(info: NsdServiceInfo) {}
            override fun onServiceUnregistered(info: NsdServiceInfo) {}
        }
        manager.registerService(NsdServiceInfo().apply {
            serviceName = name; serviceType = type; setPort(port)
            attributes.forEach { (key, value) -> setAttribute(key, value) }
        }, NsdManager.PROTOCOL_DNS_SD, listener)
        advertisements += listener
        return listener
    }

    private fun shutdown() {
        updates?.stop()
        // Gate capture before BLE cleanup or any network subscriber can wait.
        inhibitPresentation("muted", true)
        if (!awaitingUpdateMicrophone) runtime?.let { runCatching { it.setMuted(true) } }
        stopMicrophone()
        closeMusic()
        bluetooth.running(false)
        runtime?.close()
        light?.close()
        light = null
        val manager = getSystemService(NsdManager::class.java)
        advertisements.forEach { runCatching { manager.unregisterService(it) } }
        advertisements.clear()
        espHomeAdvertisement = null
        focus.update(false)
        mutable.value = mutable.value.copy(running = false, level = 0f)
        microphoneForeground = false
        stopForeground(STOP_FOREGROUND_REMOVE)
    }

    override fun onDestroy() { unregisterReceiver(musicRoute); shutdown(); scope.cancel(); audio.release(); super.onDestroy() }
}

/** Desired playback is not a focus grant. Only a new intent retries a lost/denied grant. */
internal class SatelliteAudioFocus(private val manager: AudioManager, private val changed: (Boolean) -> Unit) {
    var focused = false
        private set
    private var wanted = false
    private val request: AudioFocusRequest = AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN)
        .setAudioAttributes(AudioAttributes.Builder().setUsage(AudioAttributes.USAGE_MEDIA).build())
        .setOnAudioFocusChangeListener(::onFocusChanged).build()

    private fun onFocusChanged(value: Int) {
        focused = wanted && value == AudioManager.AUDIOFOCUS_GAIN
        // Permanent loss has no promised gain callback. Do not keep a stale registration,
        // or let unchanged server "playing" snapshots steal focus back on the next poll.
        if (value == AudioManager.AUDIOFOCUS_LOSS) manager.abandonAudioFocusRequest(request)
        changed(focused)
    }

    fun update(next: Boolean, resume: Boolean = false) {
        if (next == wanted && !(resume && next && !focused)) return
        wanted = next
        if (next) {
            focused = manager.requestAudioFocus(request) == AudioManager.AUDIOFOCUS_REQUEST_GRANTED
            changed(focused)
        } else {
            manager.abandonAudioFocusRequest(request)
            focused = false
        }
    }
}
