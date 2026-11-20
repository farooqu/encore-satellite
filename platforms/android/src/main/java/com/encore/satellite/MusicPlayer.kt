package com.encore.satellite

import android.os.Handler
import android.os.Looper
import com.encore.engine.mobile.Mobile
import com.sendspindroid.model.PlaybackStateType
import com.sendspindroid.playback.SendSpinEngine
import com.sendspindroid.sendspin.PlaybackState
import com.sendspindroid.sendspin.SendSpin
import com.sendspindroid.sendspin.SyncAudioPlayer
import com.sendspindroid.sendspin.protocol.TrackMetadata
import com.sendspindroid.coordinator.TransportState
import kotlinx.coroutines.*
import kotlinx.coroutines.flow.combine

/** Sendspin is an independent connection/output; it is not an ESPHome entity. */
class MusicPlayer(name: String, host: MusicHost, private val changed: (MusicState) -> Unit) : SendSpin.Callback {
    private val main = Handler(Looper.getMainLooper())
    private var state = MusicState(enabled = true, status = "Connecting")
    private var volume = 1f
    private var voiceActive = false
    private var muted = false
    private var closed = false
    private var address = ""
    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Main.immediate)
    private val engine = SendSpinEngine(object : SendSpinEngine.Listener {
        override fun onStreamStarted() {}
        override fun onPlayerChanged(player: SyncAudioPlayer?) {}
        override fun onPlayerStateChanged(state: PlaybackState) {}
    }, lowMemoryMode = { true }, playerMuted = { muted })
    private val client = SendSpin(name, this, host).also { engine.client = it }
    private val commands = mapOf(
        Mobile.SendspinPlay to client::play,
        Mobile.SendspinPause to client::pause,
        Mobile.SendspinNext to client::next,
        Mobile.SendspinPrevious to client::previous,
    )

    init {
        changed(state)
        scope.launch { client.connectionState.collect { transport ->
            publish(state.copy(status = when (transport) {
                TransportState.Ready -> "Connected"
                TransportState.Connecting -> "Connecting"
                else -> "Disconnected"
            }))
        } }
        scope.launch {
            combine(client.connectionState, client.controllerState) { transport, controller ->
                controller?.supportedCommands.orEmpty().filter { command ->
                    Mobile.sendspinCommandAllowed(command, transport == TransportState.Ready, command in commands)
                }
            }.collect { publish(state.copy(commands = it)) }
        }
    }

    private fun onMain(action: () -> Unit) { main.post { if (!closed) action() } }
    private fun publish(next: MusicState) { state = next; changed(next) }
    fun connect(value: String) {
        require(Regex("(?:[A-Za-z0-9.-]+|\\[[0-9a-fA-F:]+]):[0-9]{1,5}").matches(value)) { "Use Sendspin host:port" }
        address = value
        client.connect(value)
    }
    fun command(command: String) {
        val supported = command in client.controllerState.value?.supportedCommands.orEmpty() && command in commands
        if (Mobile.sendspinCommandAllowed(command, !closed && client.isConnected, supported)) {
            commands[command]?.invoke()
        }
    }
    fun duck(active: Boolean) {
        voiceActive = active
        engine.setTrackGain(volume * if (active) .15f else 1f)
    }
    fun interrupted(value: Boolean) = engine.setMuted(SyncAudioPlayer.MuteReason.INTERRUPTION, value)
    fun close() { closed = true; scope.cancel(); main.removeCallbacksAndMessages(null); client.destroy(); engine.release() }
    override fun onStateChanged(state: String) = onMain {
        engine.applyServerPlayback(when (state) {
            "playing" -> PlaybackStateType.PLAYING; "paused" -> PlaybackStateType.PAUSED; else -> PlaybackStateType.STOPPED
        })
        publish(this.state.copy(playing = state == "playing"))
    }
    override fun onGroupUpdate(groupId: String, groupName: String, playbackState: String) = onMain {
        val playing = playbackState == "playing"
        engine.applyServerPlayback(when (playbackState) {
            "playing" -> PlaybackStateType.PLAYING; "paused" -> PlaybackStateType.PAUSED; else -> PlaybackStateType.STOPPED
        })
        publish(state.copy(playing = playing))
    }
    override fun onMetadataUpdate(metadata: TrackMetadata, positionMs: Long) = onMain {
        publish(state.copy(title = metadata.title.orEmpty(), artist = metadata.artist.orEmpty()))
    }
    override fun onArtwork(imageData: ByteArray) {}
    override fun onArtworkCleared() {}
    override fun onStreamStart(codec: String, sampleRate: Int, channels: Int, bitDepth: Int, codecHeader: ByteArray?) = engine.onStreamStart(codec, sampleRate, channels, bitDepth, codecHeader)
    override fun onStreamClear() = engine.onStreamClear()
    override fun onStreamEnd() = engine.onStreamEnd()
    override fun onAudioChunk(serverTimeMicros: Long, audioData: ByteArray) = engine.onAudioChunk(serverTimeMicros, audioData)
    override fun onVolumeChanged(volume: Int) = onMain { this.volume = volume.coerceIn(0, 100) / 100f; duck(voiceActive) }
    override fun onMutedChanged(muted: Boolean) = onMain { this.muted = muted; engine.setMuted(SyncAudioPlayer.MuteReason.PLAYER, muted) }
    override fun onSyncOffsetApplied(offsetMs: Double, source: String) {}
    override fun onNetworkChanged() = engine.onNetworkChanged()
    override fun onSyncMuteChanged(muted: Boolean) = engine.onSyncMuteChanged(muted)
    override fun onUnpaired(serverId: String?) = onMain { publish(state.copy(status = "Pairing removed", playing = false)); engine.releasePlayer() }
    override fun onDisconnected(reconnect: Boolean) = onMain {
        engine.releasePlayer()
        publish(state.copy(status = if (reconnect) "Reconnecting" else "Disconnected", playing = false))
        if (reconnect) main.postDelayed({ if (!closed) client.connect(address) }, 3000)
    }
}
