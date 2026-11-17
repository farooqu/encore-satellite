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
    val name: String = "Encore Incubator",
    val phase: String = "disconnected",
    val connected: Boolean = false,
    val muted: Boolean = false,
    val models: Int = 0,
    val heard: String = "",
    val reply: String = "",
    val alert: Boolean = false,
    val dashboard: String = "",
    val reload: Int = 0,
    val problem: String = "",
    val level: Float = 0f,
    val waveform: List<Float> = emptyList(),
    val music: MusicState = MusicState(),
) {
    val origin: String get() = if (dashboard.isEmpty()) "" else Uri.parse(dashboard).let { "${it.scheme}://${it.encodedAuthority}" }
    val voiceActive: Boolean get() = alert || phase in setOf("tentative", "listening", "working", "speaking", "stopping")
    val microphoneVisible: Boolean get() = running && !muted && phase in setOf("tentative", "listening")

    companion object {
        fun parse(value: String, previous: EncoreState): EncoreState {
            val data = JSONObject(value)
            val voice = data.getJSONObject("voice")
            return previous.copy(phase = voice.getString("phase"), connected = voice.getBoolean("connected"),
                muted = voice.getBoolean("muted"), models = data.getInt("models"),
                heard = voice.optString("heard"), reply = voice.optString("reply"), alert = voice.getBoolean("alert"),
                dashboard = data.getString("dashboard"),
                reload = data.getInt("reload"), problem = data.getString("problem").ifEmpty { previous.problem })
        }
    }
}
