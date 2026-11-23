package com.encore.personal

import android.app.KeyguardManager
import android.content.Intent
import android.os.Bundle
import android.os.PowerManager
import android.speech.RecognitionService
import android.speech.SpeechRecognizer
import android.service.voice.VoiceInteractionService
import android.service.voice.VoiceInteractionSession
import android.service.voice.VoiceInteractionSessionService
import org.json.JSONObject

internal data class TurnKey(val generation: Long, val turn: Long)

internal data class VoiceTurn(
    val key: TurnKey, val invocation: String, val phase: String, val accepted: Boolean,
    val heard: String, val reply: String,
) {
    val pending get() = invocation == "wake" && !accepted && phase == "tentative"
    val canPresent get() = accepted || invocation == "manual" || invocation == "follow_up"

    companion object {
        fun from(snapshot: JSONObject): VoiceTurn? {
            val invocation = snapshot.optString("invocation")
            if (invocation.isEmpty() || !snapshot.optBoolean("active") || snapshot.optBoolean("inhibited")) return null
            return VoiceTurn(TurnKey(snapshot.getLong("generation"), snapshot.getLong("turn")), invocation,
                snapshot.optString("phase"), snapshot.optBoolean("accepted"), snapshot.optString("heard"), snapshot.optString("reply"))
        }
    }
}

// A show is requested at most once per wake. An already-shown cue upgrades in
// its own window; an outstanding system show reads the latest state at delivery.
internal class WakeWindow(private val show: (TurnKey) -> Unit) {
    private var key: TurnKey? = null

    fun update(turn: VoiceTurn?) {
        val next = turn?.takeIf { it.invocation == "wake" && (it.pending || it.accepted) }?.key
        if (next == key) return
        key = next
        if (next != null) show(next)
    }
}

class PersonalAssistant : VoiceInteractionService() {
    private data class Show(val id: Long, val mic: MicService, val key: TurnKey)
    private var source: MicService? = null
    private var request: Show? = null
    internal var session: PersonalVoiceSession? = null
    private val wake = WakeWindow { key ->
        val mic = source
        val live = mic?.voiceTurn()
        if (mic != null && mic === MicService.armedMic && live?.key == key &&
            (live.pending || live.accepted) && screenAvailable() && session?.owns(mic, key) != true) {
            val show = Show(++serial, mic, key)
            request = show
            try {
                showSession(Bundle().apply { putLong(WAKE_REQUEST, show.id) }, VoiceInteractionSession.SHOW_SOURCE_APPLICATION)
            } catch (_: SecurityException) {
                // Android may switch assistants before delivering onShutdown.
                request = null
            }
        }
    }

    override fun onReady() {
        super.onReady()
        setDisabledShowContext(VoiceInteractionSession.SHOW_WITH_ASSIST or VoiceInteractionSession.SHOW_WITH_SCREENSHOT)
        ready = this
        MicService.armedMic?.let { update(it, it.voiceTurn()) }
    }

    internal fun update(mic: MicService, turn: VoiceTurn?) {
        val armed = MicService.armedMic
        if (source !== armed) {
            wake.update(null)
            source = armed
        }
        if (request?.mic !== mic || request?.key != turn?.key) request = null
        wake.update(turn)
    }

    internal fun currentWake(id: Long): Pair<MicService, VoiceTurn>? {
        val show = request?.takeIf { it.id == id && it.mic === MicService.armedMic && screenAvailable() } ?: return null
        request = null
        val live = show.mic.voiceTurn()?.takeIf { it.key == show.key && (it.pending || it.accepted) } ?: return null
        return show.mic to live
    }

    private fun screenAvailable() = getSystemService(PowerManager::class.java).isInteractive &&
        !getSystemService(KeyguardManager::class.java).isKeyguardLocked

    override fun onShutdown() {
        if (ready === this) ready = null
        wake.update(null)
        source = null
        request = null
        session?.dismiss()
        session = null
        super.onShutdown()
    }

    companion object {
        internal const val WAKE_REQUEST = "com.encore.personal.WAKE_REQUEST"
        private var serial = 0L
        internal var ready: PersonalAssistant? = null
            private set
    }
}

class PersonalSessionService : VoiceInteractionSessionService() {
    override fun onNewSession(args: Bundle?): VoiceInteractionSession = PersonalVoiceSession(this)
}

// Android requires a recognizer component in the assistant metadata. This app
// only sends audio for a voice request on its own Incubator connection.
class NoDeviceRecognition : RecognitionService() {
    override fun onStartListening(intent: Intent, listener: Callback) { listener.error(SpeechRecognizer.ERROR_CLIENT) }
    override fun onStopListening(listener: Callback) = Unit
    override fun onCancel(listener: Callback) = Unit
}
