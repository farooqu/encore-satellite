package com.encore.satellite

import android.net.Uri
import org.json.JSONObject

data class MusicState(
    val enabled: Boolean = false,
    val status: String = "Off",
    val title: String = "",
    val artist: String = "",
    val playing: Boolean = false,
    val commands: List<String> = emptyList(),
)

data class EncoreState(
    val running: Boolean = false,
    val microphoneReady: Boolean = false,
    val name: String = "Encore Incubator",
    val phase: String = "disconnected",
    val connected: Boolean = false,
    val muted: Boolean = false,
    val models: Int = 0,
    val heard: String = "",
    val reply: String = "",
    val alert: Boolean = false,
    val voiceActive: Boolean = false,
    val announcing: Boolean = false,
    val outputOwner: String = "",
    val outputState: String = "",
    val talkAllowed: Boolean = false,
    val audioInterrupted: Boolean = false,
    val dashboard: String = "",
    val reload: Int = 0,
    val problem: String = "",
    val level: Float = 0f,
    val waveform: List<Float> = emptyList(),
    val playbackLevel: Float? = null,
    val music: MusicState = MusicState(),
    val voiceKnown: Boolean = false,
    val generation: Long = 0,
    val turn: Long = 0,
    val monitorId: Long = 0,
    val inhibited: Boolean = false,
    val input: String = "",
    val inputOpen: Boolean = false,
    val invocation: String = "",
    val accepted: Boolean = false,
    val captureReady: Boolean = false,
    val captureProblem: String = "Microphone unavailable",
    val talkReady: Boolean = false,
    val wakeReady: Boolean = false,
    val night: String = "unknown",
    val speakerName: String = "",
    val speakerRGB: List<Int> = emptyList(),
    val reason: String = "",
    val esphome: String = "",
    val espHomeFriendlyName: String = "",
) {
    val needsHomeAssistantSetup: Boolean get() = esphome == "unpaired" || esphome == "waiting" || esphome == "expired"
    val origin: String get() = if (dashboard.isEmpty()) "" else Uri.parse(dashboard).let { "${it.scheme}://${it.encodedAuthority}" }
    // Unknown snapshots suppress drawing, but keep Stop/shield for last-known
    // ownership until an actual stop or fresh snapshot releases it.
    val interactionVisible: Boolean get() = running && connected && !muted && !inhibited &&
        (voiceActive || announcing || alert || outputState in setOf("queued", "playing", "draining"))
    val microphoneVisible: Boolean get() = running && microphoneReady && voiceKnown && connected && captureReady && !muted && !inhibited &&
        (monitorId != 0L || inputOpen || input in setOf("tentative", "listening"))
    val visual: String get() = when {
        !running || !voiceKnown || muted || !connected || inhibited -> "none"
        alert || (outputOwner == "alert" && outputState in setOf("playing", "draining")) -> "alert"
        outputState == "failed" -> "none"
        outputState in setOf("playing", "draining") -> "output"
        outputState == "queued" || announcing -> "queued"
        !microphoneReady || !captureReady -> "none"
        voiceActive && phase == "working" -> "processing"
        microphoneVisible -> "input"
        else -> "none"
    }
    val status: String get() = when {
        muted -> "Microphone muted"
        !running -> "Satellite stopped"
        !voiceKnown -> "Voice status unavailable"
        !connected -> "Voice disconnected"
        inhibited -> if (phase == "stopping") "Stopping" else "Stopped · waiting for voice"
        monitorId != 0L && microphoneVisible -> "Listening"
        alert -> "Alert"
        outputState == "failed" -> "Playback unavailable"
        outputState == "draining" -> when (outputOwner) { "announcement" -> "Finishing announcement"; "alert" -> "Finishing alert"; else -> "Finishing response" }
        outputState == "playing" -> when (outputOwner) { "announcement" -> "Playing announcement"; "alert" -> "Playing alert"; else -> "Playing response" }
        outputState == "queued" || announcing -> when {
            outputOwner == "announcement" || announcing -> "Waiting for announcement audio"
            outputOwner == "alert" -> "Waiting for alert audio"
            else -> "Waiting for response audio"
        }
        !microphoneReady -> "Microphone off · start microphone"
        !captureReady -> captureProblem
        input == "tentative" -> "Checking wake"
        input == "listening" -> if (invocation == "follow_up") "Listening for your answer" else "Listening"
        voiceActive && phase == "working" -> "Processing"
        reason in setOf("wake_rejected", "answered_elsewhere") -> "Wake declined"
        phase == "error" -> if (reason == "not_recognized") "Not understood" else "Voice error"
        wakeReady -> "Wake ready"
        talkAllowed && talkReady -> "Connected · wake not ready"
        else -> "Voice not ready"
    }

    companion object {
        fun parse(value: String, previous: EncoreState): EncoreState {
            val data = JSONObject(value)
            val voice = data.getJSONObject("voice")
            val output = voice.optJSONObject("output")
            val speaker = voice.optJSONObject("speaker")
            val name = speaker?.optString("name").orEmpty().takeUnless { it == "unknown" }.orEmpty()
            val rgb = speaker?.optJSONArray("rgb")
            val colors = if (name.isNotBlank() && speaker?.optBoolean("color_known") == true && rgb?.length() == 3)
                (0..2).mapNotNull { i -> (rgb.opt(i) as? Number)?.toDouble()?.takeIf { it.isFinite() && it in 0.0..255.0 && it % 1 == 0.0 }?.toInt() }
                    .takeIf { it.size == 3 && it.any { channel -> channel > 0 } }.orEmpty() else emptyList()
            val attributedTurn = voice.optBoolean("active") && !voice.optBoolean("muted") &&
                voice.optBoolean("connected") && !voice.optBoolean("inhibited")
            return previous.copy(voiceKnown = true, phase = voice.getString("phase"), connected = voice.getBoolean("connected"),
                muted = voice.getBoolean("muted"), models = data.getInt("models"),
                heard = voice.optString("heard"), reply = voice.optString("reply"), alert = voice.getBoolean("alert"),
                generation = voice.optLong("generation"), turn = voice.optLong("turn"), monitorId = voice.optLong("monitor_id"),
                voiceActive = voice.optBoolean("active"), announcing = voice.optBoolean("announcing"), inhibited = voice.optBoolean("inhibited"),
                talkAllowed = voice.optBoolean("talk_allowed"),
                input = voice.optString("input"), inputOpen = voice.optBoolean("input_open"), invocation = voice.optString("invocation"),
                reason = voice.optString("reason"), accepted = voice.optBoolean("accepted"),
                outputState = output?.optString("state").orEmpty(), outputOwner = output?.optString("owner").orEmpty(),
                captureReady = voice.optBoolean("capture_ready"), talkReady = voice.optBoolean("talk_ready"), wakeReady = voice.optBoolean("wake_ready"),
                night = voice.optString("night").takeIf { it in setOf("on", "off") } ?: "unknown",
                speakerName = if (attributedTurn) name else "", speakerRGB = if (attributedTurn) colors else emptyList(),
                dashboard = data.getString("dashboard"),
                esphome = data.optString("esphome"),
                reload = data.getInt("reload"), problem = data.getString("problem").ifEmpty { previous.problem })
        }
    }
}
