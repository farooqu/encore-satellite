package com.encore.satellite

import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.os.Handler
import android.os.Looper
import com.encore.engine.mobile.Mobile
import com.sendspindroid.model.PlaybackStateType
import com.sendspindroid.playback.SendSpinEngine
import com.sendspindroid.sendspin.PlaybackState
import com.sendspindroid.sendspin.SendSpin
import com.sendspindroid.sendspin.SyncAudioPlayer
import com.sendspindroid.sendspin.protocol.AdmissionState
import com.sendspindroid.sendspin.protocol.TrackMetadata
import com.sendspindroid.sendspin.protocol.message.ArtworkReceiver
import com.sendspindroid.coordinator.FailureReason
import com.sendspindroid.coordinator.TransportState
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.combine

/** Sendspin is an independent connection/output; it is not an ESPHome entity. */
internal class MusicPlayer(name: String, private val host: MusicHost, private val rediscover: () -> Unit,
    private val changed: (MusicState) -> Unit) : SendSpin.Callback {
    private val main = Handler(Looper.getMainLooper())
    private var state = MusicState(enabled = true, status = "Connecting")
    private var volume = host.playerVolume.coerceIn(0, 100)
    private var voiceActive = false
    private var muted = host.playerMuted
    @Volatile private var closed = false
    @Volatile private var retryIdle = true
    private var started = false
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private var artworkJob: Job? = null
    private val engine = SendSpinEngine(object : SendSpinEngine.Listener {
        override fun onStreamStarted() {}
        override fun onPlayerChanged(player: SyncAudioPlayer?) {}
        override fun onPlayerStateChanged(state: PlaybackState) {}
    }, lowMemoryMode = { host.lowMemoryMode }, playerMuted = { muted })
    private val client = SendSpin(name, this, host).also { engine.client = it }
    private val commands = mapOf(
        Mobile.SendspinPlay to client::play,
        Mobile.SendspinPause to client::pause,
        Mobile.SendspinStop to client::stop,
        Mobile.SendspinNext to client::next,
        Mobile.SendspinPrevious to client::previous,
    )

    init {
        applyGain()
        changed(state)
        scope.launch {
            client.connectionState.collect { transport ->
                when (transport) {
                    TransportState.Ready -> publish(state.copy(status = "Connected"))
                    TransportState.Connecting -> publish(state.copy(status = "Connecting"))
                    TransportState.Idle -> if (started) {
                        if (retryIdle) disconnected("Reconnecting", reconnect = true)
                        else disconnected("Disconnected; check server pairing")
                    }
                    is TransportState.Failed -> when (transport.reason) {
                        FailureReason.TransientNetwork -> disconnected("Reconnecting", reconnect = true)
                        FailureReason.AuthRejected -> disconnected("Pairing required")
                        FailureReason.ServerLacksEncryption -> disconnected("Server needs a Sendspin upgrade")
                        else -> disconnected("Connection failed; check pairing and server version")
                    }
                }
            }
        }
        scope.launch {
            combine(client.connectionState, client.controllerState) { transport, controller ->
                controller?.supportedCommands.orEmpty().filter { command ->
                    Mobile.sendspinCommandAllowed(command, transport == TransportState.Ready, command in commands && client.canSendCommand(command))
                }
            }.collect { publish(state.copy(commands = it)) }
        }
    }

    private fun onMain(requireConnection: Boolean = true, action: () -> Unit) {
        main.post { if (!closed && (!requireConnection || client.isConnected)) action() }
    }
    private fun publish(next: MusicState) { state = next; changed(next) }
    fun connect(value: MusicEndpoint) {
        started = true
        client.setInitialVolume(volume, muted)
        client.connect(value.address, value.path)
    }
    val playing get() = !closed && client.isConnected && state.playing
    fun command(command: String): Boolean {
        val supported = command in commands && client.canSendCommand(command)
        if (!Mobile.sendspinCommandAllowed(command, !closed && client.isConnected, supported)) return false
        commands.getValue(command).invoke()
        return true
    }
    fun duck(active: Boolean) {
        if (voiceActive == active) return
        voiceActive = active
        applyGain()
    }
    private fun applyGain() = engine.setTrackGain((volume / 100f) * if (voiceActive) .2f else 1f)
    // This is a persistent local output gate, not the voice duck or an MA pause.
    fun stopped(value: Boolean) {
        engine.setMuted(SyncAudioPlayer.MuteReason.VOICE, value)
        publish(state.copy(stopped = value))
    }
    fun interrupted(value: Boolean) = engine.setMuted(SyncAudioPlayer.MuteReason.INTERRUPTION, value)
    fun close() {
        closed = true
        engine.setMuted(SyncAudioPlayer.MuteReason.INTERRUPTION, true)
        scope.cancel()
        main.removeCallbacksAndMessages(null)
        client.destroy()
        engine.release()
    }
    override fun onStateChanged(state: String) = updatePlayback(state)
    override fun onGroupUpdate(groupId: String, groupName: String, playbackState: String) {
        if (playbackState.isNotEmpty()) updatePlayback(playbackState)
    }
    private fun updatePlayback(playbackState: String) = onMain {
        engine.applyServerPlayback(when (playbackState) {
            "playing" -> PlaybackStateType.PLAYING; "paused" -> PlaybackStateType.PAUSED; else -> PlaybackStateType.STOPPED
        })
        publish(state.copy(playing = playbackState == "playing", groupPlayback = playbackState))
    }
    override fun onMetadataUpdate(metadata: TrackMetadata, positionMs: Long) = onMain {
        publish(state.copy(title = metadata.title.orEmpty(), artist = metadata.artist.orEmpty()))
    }
    override fun onArtwork(imageData: ByteArray) = onMain {
        artworkJob?.cancel()
        artworkJob = scope.launch {
            val bitmap = withContext(artworkDecoder) { decodeSendspinArtwork(imageData) }
            if (client.isConnected) publish(state.copy(artwork = bitmap))
        }
    }
    override fun onArtworkCleared() = onMain {
        artworkJob?.cancel()
        publish(state.copy(artwork = null))
    }
    override fun onStreamStart(codec: String, sampleRate: Int, channels: Int, bitDepth: Int, codecHeader: ByteArray?) {
        if (!closed) engine.onStreamStart(codec, sampleRate, channels, bitDepth, codecHeader)
    }
    override fun onStreamClear() { if (!closed) engine.onStreamClear() }
    override fun onStreamEnd() { if (!closed) engine.onStreamEnd() }
    override fun onAudioChunk(serverTimeMicros: Long, audioData: ByteArray) {
        if (!closed) engine.onAudioChunk(serverTimeMicros, audioData)
    }
    override fun onVolumeChanged(volume: Int) = onMain {
        this.volume = volume.coerceIn(0, 100)
        host.playerVolume = this.volume
        applyGain()
    }
    override fun onMutedChanged(muted: Boolean) = onMain {
        this.muted = muted
        host.playerMuted = muted
        engine.setMuted(SyncAudioPlayer.MuteReason.PLAYER, muted)
    }
    override fun onSyncOffsetApplied(offsetMs: Double, source: String) {}
    override fun onNetworkChanged() { if (!closed) engine.onNetworkChanged() }
    override fun onSyncMuteChanged(muted: Boolean) { if (!closed) engine.onSyncMuteChanged(muted) }
    override fun onAdmissionStateChanged(state: AdmissionState) = onMain {
        publish(this.state.copy(status = when (state) {
            AdmissionState.READY -> "Connected"
            AdmissionState.AWAITING_APPROVAL -> "Approve this player on the server"
            AdmissionState.PAIRING -> "Pair this player on the server"
        }))
    }
    override fun onUnpaired(serverId: String?) = onMain(requireConnection = false) {
        started = false
        disconnected("Pairing removed")
    }
    override fun onDisconnected(reconnect: Boolean) {
        retryIdle = reconnect
        if (!closed) engine.onStreamEnd()
    }
    private fun disconnected(status: String, reconnect: Boolean = false) {
        engine.endStream()
        engine.releasePlayer()
        artworkJob?.cancel()
        publish(state.copy(status = status, title = "", artist = "", playing = false, groupPlayback = "", commands = emptyList(), artwork = null))
        if (reconnect) rediscover()
    }
}

private val artworkDecoder = Dispatchers.Default.limitedParallelism(1)

internal fun decodeSendspinArtwork(image: ByteArray): Bitmap? {
    if (image.isEmpty() || image.size > ArtworkReceiver.MAX_IMAGE_BYTES) return null
    val options = BitmapFactory.Options().apply { inJustDecodeBounds = true }
    BitmapFactory.decodeByteArray(image, 0, image.size, options)
    if (options.outWidth !in 1..4096 || options.outHeight !in 1..4096) return null
    options.inSampleSize = 1
    // Preserve the server's standard 500 px cover for the full-screen player.
    while (maxOf(options.outWidth, options.outHeight) > 512 * options.inSampleSize) options.inSampleSize *= 2
    options.inJustDecodeBounds = false
    return BitmapFactory.decodeByteArray(image, 0, image.size, options)
}
