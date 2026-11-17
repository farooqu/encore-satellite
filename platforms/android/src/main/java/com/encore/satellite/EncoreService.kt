package com.encore.satellite

import android.Manifest
import android.app.*
import android.content.Intent
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
import com.encore.engine.mobile.Mobile
import com.encore.engine.mobile.Runtime
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow

/** Dashboard Activity lifetime/reloads never own the microphone or network sockets. */
class EncoreService : Service() {
    inner class LocalBinder : Binder() { val service: EncoreService get() = this@EncoreService }
    private val binder = LocalBinder()
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private val mutable = MutableStateFlow(EncoreState())
    val state = mutable.asStateFlow()
    private lateinit var audio: AudioIo
    private var runtime: Runtime? = null
    private val advertisements = mutableListOf<NsdManager.RegistrationListener>()
    private var musicHost: MusicHost? = null
    private var music: MusicPlayer? = null
    private var microphoneForeground = false
    private var focused = false
    private var focusWanted = false
    private val audioManager by lazy { getSystemService(AudioManager::class.java) }
    private val focus by lazy { AudioFocusRequest.Builder(AudioManager.AUDIOFOCUS_GAIN)
        .setAudioAttributes(AudioAttributes.Builder().setUsage(AudioAttributes.USAGE_MEDIA).build())
        .setOnAudioFocusChangeListener { value ->
            focused = value == AudioManager.AUDIOFOCUS_GAIN
            music?.interrupted(!focused)
            if (!focused) runtime?.stop()
        }.build() }

    override fun onCreate() {
        super.onCreate()
        audio = AudioIo(this)
        runCatching { Mobile.open(filesDir.resolve("engine").absolutePath, audio) }
            .onSuccess { runtime = it; mutable.value = mutable.value.copy(name = it.name()); ApkUpdates(this, it, scope) }
            .onFailure { problem("Engine could not start: ${it.message}") }
        scope.launch {
            while (isActive) {
                runtime?.let { engine ->
                    runCatching { EncoreState.parse(engine.snapshot(), mutable.value) }
                        .onSuccess { snapshot ->
                            val next = snapshot.copy(level = if (snapshot.microphoneVisible) audio.level else 0f,
                                waveform = if (snapshot.microphoneVisible) audio.waveform else emptyList())
                            mutable.value = next
                            updateFocus(next.voiceActive || next.music.playing)
                            music?.duck(next.voiceActive)
                        }.onFailure { problem("Engine status unavailable") }
                }
                delay(40)
            }
        }
    }

    override fun onBind(intent: Intent): IBinder = binder

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        if (intent?.action == "stop") { shutdown(); stopSelf(); return START_NOT_STICKY }
        val channel = NotificationChannel("encore", "Encore satellite", NotificationManager.IMPORTANCE_LOW)
        getSystemService(NotificationManager::class.java).createNotificationChannel(channel)
        val open = PendingIntent.getActivity(this, 0, Intent(this, MainActivity::class.java), PendingIntent.FLAG_IMMUTABLE)
        val stop = PendingIntent.getService(this, 1, Intent(this, EncoreService::class.java).setAction("stop"), PendingIntent.FLAG_IMMUTABLE)
        val notification = Notification.Builder(this, "encore").setSmallIcon(R.drawable.ic_encore)
            .setContentTitle("Encore Incubator").setContentText("Satellite running; voice and music controls are available in the app")
            .setContentIntent(open).setOngoing(true).addAction(Notification.Action.Builder(null, "Stop satellite", stop).build()).build()
        try {
            val enableMicrophone = intent?.action != "music" && checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED
            microphoneForeground = microphoneForeground || enableMicrophone
            if (Build.VERSION.SDK_INT >= 30) startForeground(1, notification,
                ServiceInfo.FOREGROUND_SERVICE_TYPE_MEDIA_PLAYBACK or
                    if (microphoneForeground) ServiceInfo.FOREGROUND_SERVICE_TYPE_MICROPHONE else 0)
            else startForeground(1, notification)
            val engine = checkNotNull(runtime) { "Engine unavailable" }
            if (!mutable.value.running) {
                if (!microphoneForeground) engine.setMuted(true)
                engine.start()
                mutable.value = mutable.value.copy(running = true)
                advertise(engine.name(), "_voice-incubator._tcp.", 6055, mapOf("mac" to engine.mac(), "protocol" to "2"))
                advertise(engine.name(), "_esphomelib._tcp.", 6053,
                    mapOf("mac" to engine.mac().replace(":", ""), "version" to "0.1.0", "api_encryption" to "Noise_NNpsk0_25519_ChaChaPoly_SHA256"))
            }
            if (enableMicrophone && intent?.getBooleanExtra("unmute", false) == true) engine.setMuted(false)
            if (microphoneForeground && !EncoreState.parse(engine.snapshot(), mutable.value).muted) startMicrophone()
            if (intent?.action == "music") connectMusic(intent.getStringExtra("address").orEmpty())
        } catch (error: Exception) {
            runtime?.let { runCatching { it.setMuted(true) } }
            audio.stopCapture()
            problem("Could not start satellite: ${error.message}")
            shutdown()
        }
        return START_NOT_STICKY
    }

    private fun startMicrophone() = audio.startCapture({ runtime?.capture(it) }, {
        scope.launch { setMuted(true); problem("Microphone unavailable. Check permission and privacy controls before unmuting.") }
    })

    fun setMuted(value: Boolean) {
        runCatching {
            runtime?.setMuted(value)
            if (value) audio.stopCapture() else if (mutable.value.running) startMicrophone()
        }.onFailure {
            runtime?.let { engine -> runCatching { engine.setMuted(true) } }
            audio.stopCapture()
            problem("Microphone remains off: ${it.message}")
        }
    }

    fun talk() { runCatching { runtime?.talk() }.onFailure { problem("Voice is not ready: ${it.message}") } }
    fun stopTurn() { runtime?.stop() }
    fun noiseKey(): String = runtime?.noiseKey().orEmpty()
    fun musicToken(): String = runCatching { host().pairingToken() }.getOrElse { problem("Sendspin secure storage unavailable"); "" }
    private fun host(): MusicHost = musicHost ?: MusicHost(this).also { musicHost = it }
    fun connectMusic(address: String) {
        if (!mutable.value.running) { problem("Start the satellite before connecting Sendspin"); return }
        disconnectMusic()
        runCatching {
            MusicPlayer(mutable.value.name, host()) { next -> mutable.value = mutable.value.copy(music = next) }
                .also { music = it; it.interrupted(!focused); it.duck(mutable.value.voiceActive); it.connect(address) }
        }.onFailure { disconnectMusic(); problem("Sendspin: ${it.message}") }
    }
    fun disconnectMusic() { music?.close(); music = null; mutable.value = mutable.value.copy(music = MusicState()) }
    fun musicCommand(command: String) { music?.command(command) }
    fun dismissProblem() { runtime?.dismissProblem(); mutable.value = mutable.value.copy(problem = "") }
    private fun problem(message: String) { mutable.value = mutable.value.copy(problem = message) }

    private fun updateFocus(wanted: Boolean) {
        if (wanted == focusWanted) return
        focusWanted = wanted
        if (wanted) {
            focused = audioManager.requestAudioFocus(focus) == AudioManager.AUDIOFOCUS_REQUEST_GRANTED
            music?.interrupted(!focused)
            if (!focused) runtime?.stop()
        } else {
            audioManager.abandonAudioFocusRequest(focus)
            focused = false
        }
    }

    private fun advertise(name: String, type: String, port: Int, attributes: Map<String, String>) {
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
    }

    private fun shutdown() {
        // Gate transport/pre-roll before waiting for the native recorder thread.
        runtime?.let { runCatching { it.setMuted(true) }; it.close() }
        audio.stopCapture()
        disconnectMusic()
        val manager = getSystemService(NsdManager::class.java)
        advertisements.forEach { runCatching { manager.unregisterService(it) } }
        advertisements.clear()
        updateFocus(false)
        mutable.value = mutable.value.copy(running = false, level = 0f)
        microphoneForeground = false
        stopForeground(STOP_FOREGROUND_REMOVE)
    }

    override fun onDestroy() { shutdown(); scope.cancel(); audio.release(); super.onDestroy() }
}
