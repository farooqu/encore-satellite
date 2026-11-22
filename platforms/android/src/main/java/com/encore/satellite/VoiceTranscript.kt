package com.encore.satellite

import android.os.Build
import androidx.compose.foundation.background
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.shadow
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.*
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp

internal data class VoiceTranscript(
    val generation: Long, val current: Turn, val earlier: List<Turn> = emptyList(), val finished: Boolean = false,
) {
    data class Turn(val number: Long, val heard: String, val reply: String) {
        val hasText get() = heard.isNotBlank() || reply.isNotBlank()
    }
}

internal fun VoiceTranscript?.update(state: EncoreState, local: Boolean, active: Boolean): VoiceTranscript? {
    if (!state.running || !state.voiceKnown || !state.connected || state.inhibited || state.muted) return null
    val prior = this?.takeIf { it.generation == state.generation }
    val sameTurn = prior?.current?.number == state.turn
    val declined = state.reason == "answered_elsewhere" || state.reason == "wake_rejected"
    val unelected = local && state.invocation == "wake" && !state.accepted && !sameTurn
    // A speculative wake must not erase a completed transcript unless it wins.
    if (declined || unelected) return prior?.takeIf { it.finished && !sameTurn && (!active || local) }
    val turn = VoiceTranscript.Turn(state.turn,
        state.heard.ifBlank { if (sameTurn) prior?.current?.heard.orEmpty() else "" },
        state.reply.ifBlank { if (sameTurn) prior?.current?.reply.orEmpty() else "" })
    if (!active) {
        // The engine retains the final text after drain. Never treat an idle snapshot
        // as a new conversation, or keep a cancelled or interrupted turn on screen.
        return prior?.takeIf { sameTurn && state.phase == "idle" &&
            (state.reason == "done" || state.reason == "not_recognized") &&
            (turn.hasText || it.earlier.isNotEmpty()) }?.copy(current = turn, finished = true)
    }
    if (!local) return null
    if (sameTurn && prior != null) return prior.copy(current = turn, finished = false)
    val earlier = if (state.invocation == "follow_up" && prior != null && !prior.finished)
        if (prior.current.hasText) prior.earlier + prior.current else prior.earlier else emptyList()
    return VoiceTranscript(state.generation, turn, earlier)
}

@Composable private fun voiceColors(): ColorScheme {
    val dark = isSystemInDarkTheme()
    val context = LocalContext.current
    return remember(context, LocalConfiguration.current, dark) {
        if (Build.VERSION.SDK_INT >= 31) {
            if (dark) dynamicDarkColorScheme(context) else dynamicLightColorScheme(context)
        } else if (dark) darkColorScheme() else lightColorScheme()
    }
}

@Composable internal fun VoiceWakeCue(modifier: Modifier) {
    val colors = voiceColors()
    val shape = RoundedCornerShape(12.dp)
    Row(modifier.size(38.dp, 20.dp).shadow(2.dp, shape).background(colors.surface, shape).testTag("voice-wake-cue"),
        horizontalArrangement = Arrangement.spacedBy(2.dp, Alignment.CenterHorizontally),
        verticalAlignment = Alignment.CenterVertically) {
        for (height in listOf(3, 7, 3)) {
            Box(Modifier.size(3.dp, height.dp).background(colors.primary, RoundedCornerShape(2.dp)))
        }
    }
}

@Composable internal fun VoiceTranscriptPanel(call: VoiceTranscript, state: EncoreState, modifier: Modifier,
    onClose: () -> Unit) {
    val lines = buildList {
        for (turn in call.earlier + call.current) {
            val current = turn === call.current && !call.finished
            val heardIcon = if (current && state.phase == "working") R.drawable.ic_voice_working else R.drawable.ic_voice_wave
            if (turn.heard.isNotBlank()) add(TranscriptLine(turn.heard, heardIcon, "You"))
            if (turn.reply.isNotBlank()) add(TranscriptLine(turn.reply, R.drawable.ic_voice_replying, "Encore"))
            if (current) when {
                state.phase == "error" -> add(TranscriptLine("Couldn't complete the request.", heardIcon))
                (state.phase == "speaking" || state.outputState in setOf("queued", "playing", "draining")) && turn.reply.isBlank() ->
                    add(TranscriptLine("Replying…", R.drawable.ic_voice_replying))
                state.phase == "working" && turn.heard.isBlank() -> add(TranscriptLine("Working…", heardIcon))
                state.phase in setOf("tentative", "listening") && turn.heard.isBlank() ->
                    add(TranscriptLine("Listening…", heardIcon))
            }
        }
    }
    MaterialTheme(colorScheme = voiceColors()) {
        val colors = MaterialTheme.colorScheme
        Surface(modifier.testTag("voice-transcript"), color = colors.surface,
            shape = RoundedCornerShape(topStart = 26.dp, topEnd = 26.dp), shadowElevation = 8.dp) {
            Column(Modifier.navigationBarsPadding().padding(start = 18.dp, top = 3.dp, end = 18.dp, bottom = 8.dp)) {
                Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                    Icon(painterResource(R.drawable.ic_second_pulse), contentDescription = "Encore Satellite",
                        modifier = Modifier.size(36.dp), tint = colors.primary)
                    Spacer(Modifier.weight(1f))
                    IconButton(onClick = onClose, modifier = Modifier.size(48.dp)
                        .testTag(if (call.finished) "close-voice-transcript" else "stop").semantics {
                            contentDescription = if (call.finished) "Close voice panel" else "Stop voice turn"
                            if (!call.finished) stateDescription = state.status
                        }) { Text("×", fontSize = 23.sp, color = colors.onSurfaceVariant) }
                }
                val scroll = key(call.generation, call.earlier.firstOrNull()?.number ?: call.current.number) { rememberScrollState() }
                val threshold = with(LocalDensity.current) { 8.dp.roundToPx() }
                val atBottom = scroll.maxValue == Int.MAX_VALUE || scroll.maxValue - scroll.value <= threshold
                LaunchedEffect(lines) {
                    if (atBottom) {
                        withFrameNanos { }
                        scroll.scrollTo(scroll.maxValue)
                    }
                }
                Column(Modifier.weight(1f, fill = false).verticalScroll(scroll).semantics { liveRegion = LiveRegionMode.Polite }) {
                    lines.forEachIndexed { index, line ->
                        val latest = index == lines.lastIndex
                        Row(Modifier.fillMaxWidth().padding(top = 2.dp, bottom = 3.dp, end = 6.dp)) {
                            Box(Modifier.size(2.dp, 19.dp).background(
                                if (latest) colors.primary else colors.surface, RoundedCornerShape(2.dp)))
                            Spacer(Modifier.width(10.dp))
                            Icon(painterResource(line.icon), contentDescription = null,
                                modifier = Modifier.padding(top = 2.dp).size(17.dp), tint = colors.primary)
                            Spacer(Modifier.width(8.dp))
                            Text(line.text, Modifier.weight(1f).semantics {
                                line.speaker?.let { contentDescription = "$it: ${line.text}" }
                            }, fontSize = if (line.speaker == null) 13.sp else 17.sp,
                                fontWeight = if (latest && line.speaker != null) FontWeight.Medium else FontWeight.Normal,
                                color = if (latest && line.speaker != null) colors.onSurface else colors.onSurfaceVariant)
                        }
                    }
                }
            }
        }
    }
}

private data class TranscriptLine(val text: String, val icon: Int, val speaker: String? = null)
