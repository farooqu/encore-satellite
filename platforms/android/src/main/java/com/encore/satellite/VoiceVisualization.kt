package com.encore.satellite

import android.database.ContentObserver
import android.os.Handler
import android.os.Looper
import android.provider.Settings
import androidx.compose.animation.core.*
import androidx.compose.foundation.layout.Box
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.drawWithCache
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Rect
import androidx.compose.ui.geometry.RoundRect
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.PathMeasure
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.LocalLifecycleOwner
import kotlin.math.sqrt

private val MicMint = Color(0xff97e6d4)
private val ReplyLilac = Color(0xffd5b6e8)
private val WorkingBlue = Color(0xffa1c8ff)
private val AlertAmber = Color(0xffffc88b)

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

@Composable internal fun VoiceEdgeGlow(state: EncoreState, reducedMotion: Boolean, modifier: Modifier,
    onMonitorOverlay: (Long, Boolean) -> Unit) {
    val mic = state.microphoneVisible
    val monitor = state.monitorId.takeIf { mic } ?: 0L
    val freshMic = mic && state.waveform.isNotEmpty()
    val band = when {
        mic -> MicMint
        state.visual == "alert" -> AlertAmber
        state.visual in setOf("queued", "output") -> ReplyLilac
        state.visual == "processing" -> WorkingBlue
        else -> null
    }
    // Use the full-block RMS and the playback head's level, never queued audio or a synthesized waveform.
    val input = if (freshMic) sqrt(state.level.coerceIn(0f, 1f)) else 0f
    val playing = band != null && state.playbackLevel != null && state.outputState in setOf("playing", "draining")
    val output = if (playing)
        sqrt(state.playbackLevel?.coerceIn(0f, 1f) ?: 0f) else 0f
    val lifecycle by LocalLifecycleOwner.current.lifecycle.currentStateFlow.collectAsState()
    val still = reducedMotion || !lifecycle.isAtLeast(Lifecycle.State.STARTED)

    val indicator = if (monitor != 0L) key(monitor) {
        val view = LocalView.current
        val notify by rememberUpdatedState(onMonitorOverlay)
        val ready by rememberUpdatedState(freshMic)
        val reported = remember(view) { booleanArrayOf(false) }
        val drawn = remember(view) { Runnable {
            if (ready && view.isShown && view.hasWindowFocus()) notify(monitor, true)
            else reported[0] = false
        } }
        DisposableEffect(view, drawn) {
            onDispose {
                view.removeCallbacks(drawn)
                notify(monitor, false)
            }
        }
        // Admit the exact request only after the visible mic band has been drawn.
        Modifier.drawWithContent {
            drawContent()
            if (!reported[0] && freshMic && size.width > 0f && size.height > 0f && view.isShown && view.hasWindowFocus()) {
                reported[0] = true
                view.postOnAnimation(drawn)
            }
        }
    } else Modifier

    Box(modifier.then(indicator)) {
        if (band != null) key(state.generation, state.turn, monitor) {
            val micStrength by animateFloatAsState(input, if (still || !freshMic) snap() else tween(if (input > 0f) 45 else 110), label = "Mic glow")
            val replyStrength by animateFloatAsState(output, if (still || !playing) snap() else tween(if (output > 0f) 55 else 125), label = "Reply glow")
            val phase = if (!still && (input > 0f || output > 0f || (freshMic && micStrength > 0f) || (playing && replyStrength > 0f))) {
                rememberInfiniteTransition(label = "Edge flow").animateFloat(0f, 1f,
                    infiniteRepeatable(tween(2400, easing = LinearEasing)), label = "Perimeter position")
            } else null
            val dim = if (state.night == "on") .72f else 1f
            Box(Modifier.matchParentSize().testTag("voice-edge-glow").drawWithCache {
                val inset = 2.dp.toPx()
                val outline = Path().apply {
                    addRoundRect(RoundRect(Rect(inset, inset, size.width - inset, size.height - inset), CornerRadius(11.dp.toPx())))
                }
                val measure = PathMeasure().apply { setPath(outline, false) }
                val segment = Path()
                val bandStroke = Stroke(3.dp.toPx())
                val softStroke = Stroke(12.dp.toPx(), cap = StrokeCap.Round)
                val highlightStroke = Stroke(7.dp.toPx(), cap = StrokeCap.Round)
                // The wider layers fade into the dashboard rather than creating a second hard edge.
                val bloom = (36 downTo 12 step 4).map { width ->
                    Stroke(width.dp.toPx(), cap = StrokeCap.Round) to (.025f + .055f * (36 - width) / 24f)
                }
                onDrawBehind {
                    drawPath(outline, band.copy(alpha = .11f * dim), style = softStroke)
                    drawPath(outline, band.copy(alpha = .71f * dim), style = bandStroke)
                    fun light(color: Color, strength: Float, position: Float, direction: Int) {
                        if (strength <= 0f || measure.length <= 0f) return
                        if (still) {
                            drawPath(outline, color.copy(alpha = .38f * strength * dim), style = softStroke)
                            return
                        }
                        // Overlapping lengths make a soft tail that wraps cleanly around every corner.
                        for (trail in 3 downTo 1) {
                            val width = .045f * trail
                            val start = ((position - if (direction > 0) width else 0f) % 1f + 1f) % 1f
                            val end = start + width
                            segment.reset()
                            measure.getSegment(start * measure.length, end.coerceAtMost(1f) * measure.length, segment, true)
                            if (end > 1f) measure.getSegment(0f, (end - 1f) * measure.length, segment, true)
                            val opacity = (strength * 1.65f).coerceAtMost(1f) * dim
                            val bloomOpacity = opacity / sqrt(trail.toFloat())
                            for ((stroke, falloff) in bloom) {
                                drawPath(segment, color.copy(alpha = falloff * bloomOpacity), style = stroke)
                            }
                            drawPath(segment, color.copy(alpha = (.48f / trail) * opacity), style = highlightStroke)
                        }
                    }
                    val flow = phase?.value ?: 0f
                    light(MicMint, if (freshMic) micStrength else 0f, flow, 1)
                    light(ReplyLilac, if (playing) replyStrength else 0f, .5f - flow, -1)
                }
            })
        }
    }
}
