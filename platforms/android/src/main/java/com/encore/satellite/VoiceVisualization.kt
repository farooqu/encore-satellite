package com.encore.satellite

import android.database.ContentObserver
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.Box
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.DrawScope
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.lerp
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.contentDescription
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.semantics.stateDescription
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import kotlinx.coroutines.isActive
import kotlin.math.PI
import kotlin.math.cos
import kotlin.math.exp
import kotlin.math.sin
import kotlin.math.sqrt

private val InputMint = Color(0xff97e6d4)
internal val OutputLilac = Color(0xffd5b6e8)
private val AlertAmber = Color(0xffffc88b)

internal val EncoreState.inputBraidColor: Color get() = if (speakerRGB.size == 3)
    Color(speakerRGB[0], speakerRGB[1], speakerRGB[2]) else InputMint

@Composable internal fun systemReducedMotion(): Boolean {
    val resolver = LocalContext.current.contentResolver
    var reduced by remember { mutableStateOf(Settings.Global.getFloat(resolver, Settings.Global.ANIMATOR_DURATION_SCALE, 1f) <= 0f) }
    DisposableEffect(resolver) {
        val observer = object : ContentObserver(Handler(Looper.getMainLooper())) {
            override fun onChange(selfChange: Boolean) { reduced = Settings.Global.getFloat(resolver, Settings.Global.ANIMATOR_DURATION_SCALE, 1f) <= 0f }
        }
        resolver.registerContentObserver(Settings.Global.getUriFor(Settings.Global.ANIMATOR_DURATION_SCALE), false, observer)
        onDispose { resolver.unregisterContentObserver(observer) }
    }
    return reduced
}

/** Independent audio envelopes never drive capture, playback or speaker policy. */
@Composable internal fun VoiceVisualization(state: EncoreState, reducedMotion: Boolean, modifier: Modifier = Modifier,
    lightSurface: Boolean = false) {
    val lifecycle by LocalLifecycleOwner.current.lifecycle.currentStateFlow.collectAsState()
    val observing = lifecycle.isAtLeast(Lifecycle.State.STARTED)
    // Safety and unknown facts remove both streams without an outgoing tail.
    val mode = state.visual
    val ink = Color(0xff0c141d)
    val inputColor = if (lightSurface) lerp(state.inputBraidColor, ink, .64f) else state.inputBraidColor
    val outputColor = if (lightSurface) lerp(OutputLilac, ink, .58f) else OutputLilac
    val description = when (mode) {
        "input" -> if (state.waveform.isEmpty()) "Microphone waveform awaiting samples" else "Live microphone waveform"
        "processing" -> "Processing indicator"
        "output" -> "Playback status indicator"
        "queued" -> "Waiting for playback indicator"
        "alert" -> "Alert status indicator"
        else -> "Microphone waveform inactive"
    }
    val traceModifier = modifier.alpha(if (state.night == "off") 1f else .7f).semantics {
        contentDescription = description
        stateDescription = state.status + if (state.speakerName.isNotBlank())
            " · input speaker reported by server: ${state.speakerName}" else ""
        liveRegion = LiveRegionMode.Polite
    }
    Box(traceModifier) {
        if (mode == "none") return@Box
        val still = reducedMotion || state.night != "off"
        key(state.generation, state.turn, observing) {
            val inputLevel = remember(state.waveform) {
                if (state.waveform.isEmpty()) 0f else
                    sqrt(state.waveform.sumOf { (it * it).toDouble() } / state.waveform.size).toFloat().coerceIn(0f, 1f)
            }
            // Output goes behind input, with its own envelope and opposite flow.
            LevelBraid(state.playbackLevel ?: 0f, state.outputState in setOf("playing", "draining") && state.playbackLevel != null,
                outputColor, -1f, still, observing, Modifier.matchParentSize().semantics { contentDescription = "Response audio" })
            LevelBraid(inputLevel, state.microphoneVisible && state.waveform.isNotEmpty(),
                inputColor, 1f, still, observing, Modifier.matchParentSize().semantics { contentDescription = "Microphone audio" })

            var cycle by remember { mutableFloatStateOf(0f) }
            LaunchedEffect(mode, still, observing) {
                cycle = 0f
                if (observing && !still && mode in setOf("processing", "queued", "alert")) {
                    val start = withFrameNanos { it }
                    while (isActive) withFrameNanos { cycle = ((it - start) % 3_200_000_000L).toFloat() / 3_200_000_000L }
                }
            }
            Canvas(Modifier.matchParentSize()) {
                val center = size.height / 2
                if (!state.microphoneVisible && state.outputState !in setOf("playing", "draining"))
                    drawLine(if (mode == "queued") outputColor else inputColor,
                        Offset(0f, center), Offset(size.width, center), 1.5.dp.toPx(), cap = StrokeCap.Round)
                // Processing is an explicit fact, not inferred from quiet microphone PCM.
                if (mode == "processing" || mode == "queued") {
                    for (i in -1..1) {
                        val opacity = if (still) .65f else .4f + .4f * (1 + cos(cycle * 2 * PI - i * .8).toFloat()) / 2
                        drawCircle((if (lightSurface) ink else Color(0xffb7c4d2)).copy(alpha = opacity), 1.8.dp.toPx(),
                            Offset(size.width / 2 + i * 9.dp.toPx(), size.height * .14f))
                    }
                }
                if (mode == "alert") {
                    val pulse = if (still) .5f else (1 - cos(cycle * 2 * PI).toFloat()) / 2
                    val separation = (5 + 7 * pulse).dp.toPx()
                    for (side in listOf(-1, 1)) drawLine(if (lightSurface) Color(0xff986019) else AlertAmber,
                        Offset(size.width * .06f, center + side * separation),
                        Offset(size.width * .94f, center + side * separation), 2.dp.toPx(), cap = StrokeCap.Round)
                }
            }
        }
    }
}

/** A level-reactive braid, not an oscilloscope or a speech/speaker detector. */
@Composable private fun LevelBraid(raw: Float, available: Boolean, base: Color, direction: Float,
    reducedMotion: Boolean, observing: Boolean, modifier: Modifier) {
    // Compress the mic's visual range before smoothing, so quiet speech rises
    // above a flat line without stretching the release or changing output gain.
    val target = if (direction > 0f) sqrt(raw * (2f - raw)) else raw
    val latestLevel by rememberUpdatedState(target)
    var level by remember { mutableFloatStateOf(0f) }
    var phase by remember { mutableFloatStateOf(0f) }
    LaunchedEffect(reducedMotion, observing, available) {
        level = 0f
        phase = 0f
        if (observing && !reducedMotion && available) {
            var previous = withFrameNanos { it }
            while (isActive) withFrameNanos { now ->
                val seconds = ((now - previous) / 1_000_000_000f).coerceIn(0f, .05f)
                previous = now
                val tau = if (latestLevel > level) .022f else .095f
                level += (latestLevel - level) * (1f - exp(-seconds / tau))
                if (level > .0001f) phase = (phase + direction * seconds * 1.6f) % (2 * PI).toFloat()
            }
        }
    }
    Canvas(modifier) {
        if (!available) return@Canvas
        val energy = if (reducedMotion || !observing) target else level
        val strength = if (direction > 0f || reducedMotion) energy else energy * 1.6f / (1f + .6f * energy)
        val time = (if (reducedMotion || !observing) 0f else phase) + if (direction < 0) (PI / 2).toFloat() else 0f
        drawBraid(strength, time, reducedMotion,
            listOf(base, lerp(base, Color.White, .32f), lerp(base, Color.Black, .22f)))
    }
}

private fun DrawScope.drawBraid(strength: Float, time: Float, reducedMotion: Boolean, colors: List<Color>) {
    for (line in 0..2) {
        val path = Path()
        var previous = Offset(0f, size.height / 2)
        path.moveTo(previous.x, previous.y)
        for (i in 1..64) {
            val x = i / 64f
            val envelope = sin(x * PI).toFloat()
            val wave = .78f * sin(x * PI * 4 - time + line * 2 * PI / 3).toFloat() +
                .22f * sin(x * PI * 7 + time * 2 + line).toFloat()
            val taper = if (reducedMotion) envelope * envelope else envelope
            val y = size.height / 2 + strength * taper * wave * size.height *
                (if (reducedMotion) .18f else .46f) * (1 - line * .12f)
            val point = Offset(x * size.width, y)
            path.quadraticBezierTo(previous.x, previous.y, (previous.x + point.x) / 2, (previous.y + point.y) / 2)
            previous = point
        }
        path.quadraticBezierTo(previous.x, previous.y, size.width, size.height / 2)
        drawPath(path, colors[line].copy(alpha = .85f), style = Stroke((2.2f - line * .35f).dp.toPx(), cap = StrokeCap.Round))
    }
}
