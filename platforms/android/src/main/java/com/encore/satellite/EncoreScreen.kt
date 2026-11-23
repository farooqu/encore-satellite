package com.encore.satellite

import android.content.Context
import android.net.Uri
import android.os.Build
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.awaitEachGesture
import androidx.compose.foundation.gestures.awaitFirstDown
import androidx.compose.foundation.isSystemInDarkTheme
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.AbsoluteAlignment
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.rotate
import androidx.compose.ui.focus.focusProperties
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.drawscope.scale
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.changedToUpIgnoreConsumed
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.*
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.encore.engine.mobile.Mobile
import kotlinx.coroutines.delay

private val Ink = Color(0xff0c141d)
private val Mint = Color(0xff97e6d4)
private val Muted = Color(0xffa0afbd)

@OptIn(ExperimentalMaterial3Api::class)
@Composable
internal fun EncoreScreen(state: EncoreState, onStart: () -> Unit, onTalk: () -> Unit, onStop: () -> Unit,
    onMuted: (Boolean) -> Unit, noiseKey: () -> String,
    musicToken: () -> String, onMusicEnable: (String?) -> Unit, onMusicDisconnect: () -> Unit,
    onMusicCommand: (String) -> Unit, onDismiss: () -> Unit, onUpdate: () -> Unit = {},
    onBluetooth: () -> Unit = {},
    reducedMotion: Boolean = systemReducedMotion(),
    onMonitorOverlay: (Long, Boolean) -> Unit = { _, _ -> },
    onPairHomeAssistant: () -> Unit = {},
    microphonePermissionGranted: Boolean = true,
    dashboardAuth: DashboardAuth? = null,
    dashboardContent: (@Composable () -> Unit)? = null,
    onChooseHome: () -> Unit = {}, onNotificationSettings: () -> Unit = {}, onSetup: () -> Unit = {},
    onStopAlert: (Long) -> Unit = { onStop() }, onMusicRevoke: () -> Unit = {}) {
    val voiceVisible = state.interactionVisible || state.microphoneVisible
    val runningTimers = state.timers.takeIf { state.running && state.connected && state.voiceKnown }.orEmpty()
    val ringing = state.voiceKnown && state.alertVisible && state.alert
    // Only local Assist invocations use the transcript. Unclassified and remote
    // sessions keep the existing indicator, including its monitor admission hook.
    val localAssist = state.voiceKnown && state.invocation in setOf("wake", "manual", "follow_up") &&
        state.monitorId == 0L && !state.announcing && !state.alert && state.outputOwner in setOf("", "voice")
    val legacyVisible = voiceVisible && !localAssist
    var transcript by remember { mutableStateOf<VoiceTranscript?>(null) }
    val panel = transcript.update(state, localAssist, voiceVisible)
    if (transcript != panel) SideEffect { transcript = panel }
    LaunchedEffect(panel?.takeIf { it.finished }) {
        if (panel?.finished == true) { delay(6000); transcript = null }
    }
    val pendingWake = localAssist && state.microphoneVisible && state.invocation == "wake" && !state.accepted &&
        state.phase == "tentative" && state.reason.isEmpty()
    var timersExpanded by remember(state.generation, runningTimers.isNotEmpty()) { mutableStateOf(false) }
    val sheetVisible = ringing || (!legacyVisible && (panel != null || timersExpanded))
    var settingsVisible by remember(legacyVisible || sheetVisible) { mutableStateOf(false) }
    var pickerOrigin by remember(settingsVisible) { mutableStateOf<String?>(null) }
    val context = LocalContext.current
    val displayPrefs = remember(context) { context.getSharedPreferences("dashboard-display", Context.MODE_PRIVATE) }
    var selection by remember(displayPrefs) {
        mutableStateOf(displayPrefs.getString("selection", null)?.let(DashboardSelection::decode))
    }
    val displayOrigin = selection?.origin.orEmpty()
    val displayPages = selection?.pages.orEmpty()
    fun saveDisplay(origin: String, pages: List<DashboardPage>) {
        val next = DashboardSelection(origin, pages)
        displayPrefs.edit().putString("selection", next.encode()).apply()
        selection = next
    }
    fun closePicker() {
        if (dashboardAuth?.attempt?.origin == pickerOrigin) dashboardAuth?.cancel()
        pickerOrigin = null
    }
    val music = state.music
    val showMusic = music.enabled && !music.stopped && music.groupPlayback != "stopped" &&
        (music.playing || music.groupPlayback == "paused")
    fun closeSettings() { settingsVisible = false }
    fun closePanel() {
        if (panel == null) timersExpanded = false
        transcript = null
        if (panel?.finished == false) onStop()
    }
    fun stopAlert() = onStopAlert(state.ring?.sequence ?: 0L)
    MaterialTheme(colorScheme = darkColorScheme(primary = Mint, background = Ink, surface = Color(0xff152330))) {
        BoxWithConstraints(Modifier.fillMaxSize().background(Ink)) {
            val landscape = maxWidth > maxHeight
            val swipeEnabled = !settingsVisible && !sheetVisible && !legacyVisible && !pendingWake
            var dockTap by remember(swipeEnabled) { mutableIntStateOf(0) }
            LaunchedEffect(dockTap) {
                if (dockTap != 0) {
                    delay(5_000)
                    dockTap = 0
                }
            }
            val revealDock = if (swipeEnabled) Modifier.pointerInput(Unit) {
                awaitEachGesture {
                    // Observe taps without stealing them from the dashboard or the dock buttons.
                    val down = awaitFirstDown(requireUnconsumed = false, pass = PointerEventPass.Initial)
                    while (true) {
                        val change = awaitPointerEvent(PointerEventPass.Initial).changes.singleOrNull() ?: break
                        if (change.id != down.id || (change.position - down.position).getDistance() > viewConfiguration.touchSlop ||
                            change.uptimeMillis - down.uptimeMillis >= viewConfiguration.longPressTimeoutMillis) break
                        if (!change.pressed) {
                            if (change.changedToUpIgnoreConsumed()) dockTap++
                            break
                        }
                    }
                }
            } else Modifier
            Box(Modifier.fillMaxSize().testTag("satellite-display").semantics {
                if (settingsVisible) hideFromAccessibility()
                else if (swipeEnabled && dockTap == 0) {
                    customActions = listOf(CustomAccessibilityAction("Show satellite controls") { dockTap++; true })
                }
            }.focusProperties { canFocus = !settingsVisible }.then(revealDock)) {
                Box(Modifier.fillMaxSize()) {
                    if (dashboardContent != null) dashboardContent()
                    else Dashboard(state, microphonePermissionGranted, onStart, onPairHomeAssistant,
                        dashboardAuth, settingsVisible, displayOrigin, displayPages, swipeEnabled, reducedMotion,
                        nowPlaying = if (showMusic) ({ minimize ->
                            if (!voiceVisible) NowPlaying(music, onMusicCommand, state.audioInterrupted, landscape, minimize)
                        }) else null,
                        miniPlayer = { expand ->
                            if (!voiceVisible) Box(Modifier.fillMaxSize().systemBarsPadding().padding(horizontal = 16.dp)
                                .padding(bottom = if (landscape) 68.dp else 132.dp),
                                contentAlignment = if (landscape) AbsoluteAlignment.BottomLeft else Alignment.BottomCenter) {
                                MiniPlayer(music, onMusicCommand, state.audioInterrupted, expand)
                            }
                        })
                }
                if (!settingsVisible && !sheetVisible && (legacyVisible || pendingWake || dockTap != 0)) {
                    val buttons: @Composable () -> Unit = {
                        val startMicrophone = state.running && !state.microphoneReady && !state.muted
                        val primaryAction = when {
                            state.alertVisible -> ::stopAlert
                            legacyVisible -> onStop
                            startMicrophone -> onStart
                            else -> onTalk
                        }
                        FilledIconButton(onClick = { if (swipeEnabled) dockTap++; primaryAction() },
                            enabled = legacyVisible || startMicrophone ||
                                (state.running && state.voiceKnown && state.connected && state.microphoneReady && state.captureReady &&
                                    !state.muted && !state.inhibited && state.talkAllowed && state.talkReady),
                            colors = IconButtonDefaults.filledIconButtonColors(containerColor = Ink, contentColor = Mint,
                                disabledContainerColor = Color(0xff38434d), disabledContentColor = Color(0xff9ca8b1)),
                            modifier = Modifier.size(56.dp).testTag(if (legacyVisible) "stop" else "talk").semantics {
                                contentDescription = when {
                                    state.alertVisible -> "Stop alert"
                                    legacyVisible -> "Stop voice turn"
                                    startMicrophone -> "Start microphone"
                                    else -> "Trigger assistant"
                                }
                                val status = state.status
                                stateDescription = when {
                                    !state.microphoneVisible -> status
                                    state.monitorId != 0L -> if (state.waveform.isNotEmpty()) "Remote mic on" else "Remote mic starting"
                                    state.outputState in setOf("playing", "draining") -> "Mic on · $status"
                                    state.phase == "working" -> "Mic on · Processing"
                                    status.startsWith("Listening") -> status
                                    else -> "Mic on"
                                }
                                if (legacyVisible) liveRegion = LiveRegionMode.Polite
                            }) {
                            if (legacyVisible) {
                                val color = LocalContentColor.current
                                Canvas(Modifier.size(18.dp)) { drawRoundRect(color, cornerRadius = CornerRadius(3.dp.toPx())) }
                            } else Icon(painterResource(R.drawable.ic_trigger_assistant), contentDescription = null)
                        }
                        MuteButton(state, { if (swipeEnabled) dockTap++; onMuted(it) })
                        FilledIconButton(onClick = { settingsVisible = true }, enabled = !legacyVisible,
                            colors = IconButtonDefaults.filledIconButtonColors(containerColor = Ink, contentColor = Mint,
                                disabledContainerColor = Color(0xff38434d), disabledContentColor = Color(0xff9ca8b1)),
                            modifier = Modifier.size(56.dp).testTag("open-settings")) {
                            Icon(painterResource(R.drawable.ic_satellite_settings), contentDescription = "Settings")
                        }
                    }
                    val dock = Modifier.navigationBarsPadding().padding(16.dp)
                    if (landscape) Column(Modifier.align(AbsoluteAlignment.CenterRight).then(dock),
                        verticalArrangement = Arrangement.spacedBy(16.dp)) { buttons() }
                    else Row(Modifier.align(Alignment.BottomCenter).then(dock),
                        horizontalArrangement = Arrangement.spacedBy(16.dp)) { buttons() }
                }
            }
            if (legacyVisible || ringing) {
                VoiceEdgeGlow(state, reducedMotion, Modifier.matchParentSize(), onMonitorOverlay)
            }
            if (sheetVisible) {
                BackHandler(onBack = if (ringing) ::stopAlert else ::closePanel)
                SatelliteSheet(panel, state, runningTimers, ringing, pendingWake, Modifier.align(Alignment.BottomCenter).widthIn(max = 640.dp)
                    .fillMaxWidth().heightIn(max = maxHeight * if (ringing || runningTimers.isNotEmpty()) .65f else .5f),
                    onClose = ::closePanel, onStopAlert = ::stopAlert)
            } else if (legacyVisible) {
                BackHandler(onBack = if (state.alertVisible) ::stopAlert else onStop)
            } else if (settingsVisible) {
                // Keep Settings in the Activity window so a remote turn can take over without losing window focus.
                val choosing = pickerOrigin
                BackHandler(onBack = if (choosing != null) ::closePicker else ::closeSettings)
                val dark = isSystemInDarkTheme()
                val colors = when {
                    Build.VERSION.SDK_INT >= 31 -> if (dark) dynamicDarkColorScheme(context) else dynamicLightColorScheme(context)
                    dark -> darkColorScheme()
                    else -> lightColorScheme()
                }
                MaterialTheme(colorScheme = colors) {
                    Surface(Modifier.fillMaxSize().testTag("settings-screen").semantics { paneTitle = "Satellite settings" }) {
                        if (choosing != null) DashboardPicker(choosing, displayPages.takeIf { choosing == displayOrigin }.orEmpty(),
                            dashboardAuth, ::closePicker) { saveDisplay(choosing, it); closeSettings() }
                        else SettingsContent(state, noiseKey, musicToken, onMusicEnable, onMusicDisconnect, onMusicCommand, onUpdate, onBluetooth,
                            dashboardAuth, onChooseHome, onNotificationSettings, { closeSettings(); onSetup() }, onMusicRevoke, ::closeSettings,
                            displayOrigin, displayPages, { pickerOrigin = it }, { saveDisplay(displayOrigin, it) })
                    }
                }
            } else if (pendingWake) {
                VoiceWakeCue(Modifier.align(Alignment.BottomCenter).navigationBarsPadding()
                    .padding(bottom = if (landscape) 8.dp else 82.dp))
            } else if (runningTimers.isNotEmpty()) {
                val count = runningTimers.size
                FilledTonalButton(onClick = { timersExpanded = true },
                    Modifier.align(Alignment.BottomCenter).navigationBarsPadding()
                        .padding(bottom = if (landscape) 12.dp else 76.dp)
                        .heightIn(min = 48.dp).testTag("timer-chip")
                        .semantics { contentDescription = if (count == 1) "Show running timer" else "Show $count running timers" }) {
                    Text("${runningTimers.first().label} · ${if (count == 1) "1 timer" else "$count timers"}")
                }
            }
            if (ringing) {
                MuteButton(state, onMuted, Modifier.align(Alignment.TopEnd).statusBarsPadding().padding(16.dp))
            }
            if (!settingsVisible && state.problem.isNotEmpty()) {
                Surface(Modifier.align(Alignment.TopCenter).statusBarsPadding()
                    .padding(start = 20.dp, end = if (ringing) 84.dp else 20.dp, top = 4.dp, bottom = 4.dp),
                    shape = RoundedCornerShape(12.dp)) {
                    Row(Modifier.padding(horizontal = 12.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                        Text(state.problem, Modifier.weight(1f), color = Color(0xffffd0a4), maxLines = 2,
                            overflow = androidx.compose.ui.text.style.TextOverflow.Ellipsis)
                        IconButton(onClick = onDismiss, Modifier.semantics { contentDescription = "Dismiss notice" }) { Text("×", fontSize = 24.sp) }
                    }
                }
            }
        }
        dashboardAuth?.attempt?.let { attempt ->
            if (attempt.origin == (pickerOrigin ?: displayOrigin)) DashboardEnrollmentDialog(state.name, attempt.origin, attempt.qr,
                attempt.candidate?.name(), attempt.error, onCancel = dashboardAuth::cancel,
                onAccept = { dashboardAuth.accept(attempt) }, onRetry = { dashboardAuth.start(attempt.origin) })
            else if (state.running) LaunchedEffect(attempt) { dashboardAuth.cancel() }
        }
    }
}

@Composable private fun MuteButton(state: EncoreState, onMuted: (Boolean) -> Unit, modifier: Modifier = Modifier) {
    FilledIconToggleButton(checked = state.muted, onCheckedChange = onMuted, enabled = state.microphoneReady || state.muted,
        colors = IconButtonDefaults.filledIconToggleButtonColors(containerColor = Ink, contentColor = Mint,
            checkedContainerColor = Color(0xffa23b45), checkedContentColor = Color.White),
        modifier = modifier.size(56.dp).testTag("privacy").semantics {
            contentDescription = if (state.muted) "Unmute mic" else "Mute mic"
            stateDescription = if (state.muted) "Microphone muted" else "Microphone unmuted"
        }) {
        MicrophoneGlyph(LocalContentColor.current)
    }
}

@Composable private fun MicrophoneGlyph(color: Color) {
    Canvas(Modifier.size(26.dp)) {
        val unit = size.width / 24
        val stroke = 1.8f * unit
        drawRoundRect(color, Offset(9 * unit, 3 * unit), Size(6 * unit, 11 * unit),
            CornerRadius(3 * unit), style = Stroke(stroke))
        drawArc(color, 0f, 180f, false, Offset(6 * unit, 6 * unit), Size(12 * unit, 12 * unit),
            style = Stroke(stroke, cap = StrokeCap.Round))
        drawLine(color, Offset(12 * unit, 18 * unit), Offset(12 * unit, 21 * unit), stroke, StrokeCap.Round)
        drawLine(color, Offset(9 * unit, 21 * unit), Offset(15 * unit, 21 * unit), stroke, StrokeCap.Round)
        drawLine(color, Offset(3 * unit, 3 * unit), Offset(21 * unit, 21 * unit), stroke, StrokeCap.Round)
    }
}

@Composable private fun NowPlaying(music: MusicState, command: (String) -> Unit, interrupted: Boolean,
    landscape: Boolean, minimize: () -> Unit) {
    val artwork: @Composable (Modifier) -> Unit = { modifier ->
        val cover = music.artwork
        Surface(modifier, shape = RoundedCornerShape(24.dp), shadowElevation = 12.dp, color = Color(0xff152e36)) {
            if (cover != null) {
                Image(cover.asImageBitmap(), contentDescription = "Album cover",
                    modifier = Modifier.fillMaxSize(), contentScale = ContentScale.Fit)
            } else Box(Modifier.fillMaxSize().background(Brush.linearGradient(listOf(Color(0xff315752), Ink))),
                contentAlignment = Alignment.Center) {
                MusicGlyph("track", Mint, Modifier.size(76.dp))
            }
        }
    }
    val details: @Composable (Modifier) -> Unit = { modifier ->
        Column(modifier, horizontalAlignment = if (landscape) Alignment.Start else Alignment.CenterHorizontally) {
            val align = if (landscape) TextAlign.Start else TextAlign.Center
            Column(horizontalAlignment = if (landscape) Alignment.Start else Alignment.CenterHorizontally,
                modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite }) {
                Text(musicPlaybackLabel(music, interrupted), color = Mint, letterSpacing = 1.4.sp,
                    style = MaterialTheme.typography.labelLarge)
                Spacer(Modifier.height(8.dp))
                Text(music.title.ifBlank { "Sendspin music" }, textAlign = align, color = Color.White,
                    style = if (landscape) MaterialTheme.typography.displaySmall else MaterialTheme.typography.headlineMedium,
                    maxLines = 2, overflow = TextOverflow.Ellipsis)
                if (music.artist.isNotBlank()) Text(music.artist, textAlign = align, color = Muted,
                    style = MaterialTheme.typography.titleMedium, maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            Spacer(Modifier.height(16.dp))
            Row(verticalAlignment = Alignment.CenterVertically) {
                MusicIconButton(Mobile.SendspinPrevious, "Previous", Mobile.SendspinPrevious in music.commands, command)
                MusicPlayPause(music, interrupted, command, prominent = true)
                MusicIconButton(Mobile.SendspinNext, "Next", Mobile.SendspinNext in music.commands, command)
                MusicIconButton(Mobile.SendspinStop, "Stop music", true, command)
            }
        }
    }
    BoxWithConstraints(Modifier.fillMaxSize().background(Brush.verticalGradient(listOf(Color(0xff19343b), Ink)))
        .systemBarsPadding().padding(top = 16.dp, bottom = if (landscape) 64.dp else 128.dp),
        contentAlignment = Alignment.Center) {
        val coverSize = if (landscape) minOf((maxWidth - 120.dp) * .42f, maxHeight * .78f, 440.dp)
            else minOf(maxWidth - 56.dp, maxHeight * .40f, 380.dp)
        Column(Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).padding(top = 8.dp, bottom = 12.dp)
            .testTag("now-playing"), horizontalAlignment = Alignment.CenterHorizontally,
            verticalArrangement = Arrangement.spacedBy(if (landscape) 24.dp else 12.dp)) {
            Column(horizontalAlignment = Alignment.CenterHorizontally) {
                DisplayTime(landscape, Modifier.testTag("now-playing-time"))
                IconButton(onClick = minimize, modifier = Modifier.size(48.dp)
                    .semantics { contentDescription = "Minimize" }) {
                    MusicGlyph("expand", Mint, Modifier.size(24.dp).rotate(180f))
                }
            }
            if (landscape) {
                Box(Modifier.fillMaxWidth().absolutePadding(left = 32.dp, right = 88.dp), contentAlignment = Alignment.Center) {
                    Row(Modifier.widthIn(max = 960.dp).fillMaxWidth().padding(horizontal = 12.dp),
                        verticalAlignment = Alignment.CenterVertically, horizontalArrangement = Arrangement.spacedBy(24.dp)) {
                        artwork(Modifier.size(coverSize))
                        details(Modifier.weight(1f))
                    }
                }
            } else {
                Column(Modifier.widthIn(max = 480.dp).fillMaxWidth().padding(horizontal = 28.dp),
                    horizontalAlignment = Alignment.CenterHorizontally, verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    artwork(Modifier.size(coverSize))
                    details(Modifier.fillMaxWidth())
                }
            }
        }
    }
}

@Composable private fun MiniPlayer(music: MusicState, command: (String) -> Unit, interrupted: Boolean, expand: () -> Unit) {
    Surface(Modifier.widthIn(max = 360.dp).fillMaxWidth().testTag("floating-player"),
        shape = RoundedCornerShape(20.dp), shadowElevation = 10.dp, color = Color(0xff1e383e)) {
        Row(Modifier.padding(horizontal = 8.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
            TextButton(onClick = expand, modifier = Modifier.weight(1f).semantics { contentDescription = "Expand Now Playing" }) {
                val cover = music.artwork
                if (cover != null) Image(cover.asImageBitmap(), null, Modifier.size(32.dp).clip(RoundedCornerShape(6.dp)))
                else MusicGlyph("track", Mint)
                Column(Modifier.padding(start = 8.dp).weight(1f)) {
                    Text(musicPlaybackLabel(music, interrupted), style = MaterialTheme.typography.labelSmall, color = Mint)
                    Text(music.title.ifBlank { "Sendspin music" }, color = Color.White, maxLines = 1, overflow = TextOverflow.Ellipsis)
                }
                MusicGlyph("expand", Mint, Modifier.size(18.dp))
            }
            MusicPlayPause(music, interrupted, command, prominent = false)
            MusicIconButton(Mobile.SendspinStop, "Stop music", true, command)
        }
    }
}

private fun musicPlaybackLabel(music: MusicState, interrupted: Boolean) = when {
    !music.playing -> "Paused"
    interrupted -> "Silent on this tablet"
    else -> "Now playing"
}

@Composable private fun MusicPlayPause(music: MusicState, interrupted: Boolean, command: (String) -> Unit, prominent: Boolean) {
    val resume = music.playing && interrupted
    val pause = music.playing && !interrupted
    val glyph = if (pause) Mobile.SendspinPause else Mobile.SendspinPlay
    val label = when {
        resume -> "Resume on this tablet"
        pause -> "Pause"
        else -> "Play"
    }
    val enabled = resume || glyph in music.commands
    if (prominent) {
        FilledIconButton(onClick = { command(if (resume) "resume" else glyph) }, enabled = enabled,
            modifier = Modifier.size(56.dp).semantics { contentDescription = label },
            colors = IconButtonDefaults.filledIconButtonColors(containerColor = Mint, contentColor = Ink)) {
            MusicGlyph(glyph, LocalContentColor.current)
        }
    } else {
        MusicIconButton(glyph, label, enabled) { command(if (resume) "resume" else it) }
    }
}

@Composable private fun MusicIconButton(action: String, label: String, enabled: Boolean, command: (String) -> Unit) {
    IconButton(onClick = { command(action) }, enabled = enabled, modifier = Modifier.semantics { contentDescription = label },
        colors = IconButtonDefaults.iconButtonColors(contentColor = Color(0xffd7e8e8), disabledContentColor = Color(0xff688089))) {
        MusicGlyph(action, LocalContentColor.current)
    }
}

@Composable private fun MusicGlyph(action: String, color: Color, modifier: Modifier = Modifier.size(24.dp)) {
    Canvas(modifier) {
        scale(size.width / 24f, size.height / 24f, pivot = Offset.Zero) {
            fun bar(x: Float, y: Float, width: Float, height: Float) =
                drawRoundRect(color, Offset(x, y), Size(width, height), CornerRadius(1.3f))
            fun triangle(base: Float, tip: Float) = drawPath(Path().apply {
                moveTo(base, 5f); lineTo(tip, 12f); lineTo(base, 19f); close()
            }, color)
            when (action) {
                Mobile.SendspinPlay -> triangle(8f, 19f)
                Mobile.SendspinPause -> { bar(6f, 5f, 4f, 14f); bar(14f, 5f, 4f, 14f) }
                Mobile.SendspinPrevious -> { bar(4f, 5f, 2f, 14f); triangle(19f, 7f) }
                Mobile.SendspinNext -> { triangle(5f, 17f); bar(18f, 5f, 2f, 14f) }
                Mobile.SendspinStop -> bar(6f, 6f, 12f, 12f)
                "expand" -> drawPath(Path().apply {
                    moveTo(5f, 15f); lineTo(12f, 8f); lineTo(19f, 15f)
                }, color, style = Stroke(width = 2f, cap = StrokeCap.Round))
                "track" -> {
                    bar(3f, 10f, 2.5f, 4f); bar(8f, 6f, 2.5f, 12f)
                    bar(13f, 3f, 2.5f, 18f); bar(18f, 8f, 2.5f, 8f)
                }
            }
        }
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable private fun MusicControls(music: MusicState, command: (String) -> Unit, interrupted: Boolean) {
    Column(Modifier.fillMaxWidth()) {
        val inactiveStatus = when {
            music.stopped -> "Stopped on this tablet"
            music.groupPlayback == "stopped" -> "Stopped"
            music.groupPlayback == "paused" -> "Paused"
            music.playing && interrupted -> "Silent on this tablet"
            else -> ""
        }
        val status = inactiveStatus.ifEmpty { if (music.playing) "Now playing" else music.status }
        Text(music.title.ifEmpty { "Sendspin · $status" }, maxLines = 1, overflow = TextOverflow.Ellipsis,
            style = MaterialTheme.typography.titleMedium, modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite })
        if (music.artist.isNotEmpty()) Text(music.artist, color = MaterialTheme.colorScheme.onSurfaceVariant,
            maxLines = 1, style = MaterialTheme.typography.bodySmall)
        if (music.title.isNotEmpty() && inactiveStatus.isNotEmpty()) Text(inactiveStatus, color = MaterialTheme.colorScheme.onSurfaceVariant)
        FlowRow {
            val resume = music.playing && interrupted && !music.stopped
            val pause = music.playing && !music.stopped
            val action = if (pause) Mobile.SendspinPause else Mobile.SendspinPlay
            TextButton(onClick = { command(if (resume) "resume" else action) },
                enabled = resume || action in music.commands || (music.stopped && music.playing)) {
                Text(if (resume) "Resume on this tablet" else if (pause) "Pause" else "Play")
            }
            TextButton(onClick = { command(Mobile.SendspinStop) }, enabled = !music.stopped) { Text("Stop music") }
            TextButton(onClick = { command(Mobile.SendspinPrevious) }, enabled = Mobile.SendspinPrevious in music.commands) { Text("Previous") }
            TextButton(onClick = { command(Mobile.SendspinNext) }, enabled = Mobile.SendspinNext in music.commands) { Text("Next") }
        }
    }
}

internal fun trustedNavigation(origin: String, value: String): Boolean {
    val a = Uri.parse(origin)
    val b = Uri.parse(value)
    return b.scheme in listOf("https", "http") && a.scheme == b.scheme && a.encodedAuthority == b.encodedAuthority && b.userInfo == null
}

@Composable private fun Dashboard(state: EncoreState, microphonePermissionGranted: Boolean,
    onRequestPermission: () -> Unit, onPairHomeAssistant: () -> Unit, auth: DashboardAuth?, settingsVisible: Boolean,
    origin: String, pages: List<DashboardPage>, swipeEnabled: Boolean, reducedMotion: Boolean,
    nowPlaying: (@Composable (minimize: () -> Unit) -> Unit)?, miniPlayer: @Composable (expand: () -> Unit) -> Unit) {
    val needsPermission = !state.running && !microphonePermissionGranted
    if (needsPermission || state.needsHomeAssistantSetup) {
        Box(Modifier.fillMaxSize().background(Color(0xff101d28)).padding(bottom = 88.dp), contentAlignment = Alignment.Center) {
            Column(Modifier.widthIn(max = 560.dp).verticalScroll(rememberScrollState()).padding(24.dp),
                verticalArrangement = Arrangement.spacedBy(16.dp)) {
                Text(if (needsPermission) "Set up Encore" else "Home Assistant setup",
                    style = MaterialTheme.typography.headlineMedium, color = Color.White)
                Text(when {
                    needsPermission -> "Allow microphone access for voice and local wake-word detection."
                    !state.running -> if (state.problem.isEmpty()) "Starting Encore…" else "Reopen Encore to try again."
                    state.esphome == "waiting" -> "In Home Assistant, go to Settings → Devices & services and add ${state.espHomeFriendlyName} under Discovered. You won't need to enter a key. The pairing window closes after two minutes."
                    state.esphome == "expired" -> "The pairing window expired. Start pairing again, then add the satellite in Home Assistant."
                    else -> "Pair on a trusted local network to add this satellite in Home Assistant without entering an encryption key."
                }, Modifier.semantics { liveRegion = LiveRegionMode.Polite }, color = Muted)
                if (needsPermission) {
                    Button(onClick = onRequestPermission) { Text("Allow microphone access") }
                } else if (state.running && state.esphome != "waiting") {
                    Button(onClick = onPairHomeAssistant) { Text("Pair with Home Assistant") }
                }
            }
        }
    } else {
        DashboardCarousel(pages, swipeEnabled, reducedMotion, nowPlaying, miniPlayer,
            musicFocused = state.music.playing && !state.audioInterrupted && !state.voiceActive) { page, visible ->
            if (auth != null && auth.lostOrigin == origin) {
                DashboardSignInNeeded(onPhone = if (supportsDashboardBridge()) ({ auth.start(origin) }) else null,
                    onTablet = auth::manualSignIn)
            } else {
                var reload by remember { mutableIntStateOf(state.reload) }
                LaunchedEffect(state.reload) { if (visible) reload = state.reload }
                DashboardWebView(origin, origin + page.path, reload, auth, hidden = settingsVisible || !visible)
            }
        }
    }
}

@Composable
internal fun DashboardSignInNeeded(onPhone: (() -> Unit)?, onTablet: () -> Unit) {
    Column(Modifier.fillMaxSize().background(MaterialTheme.colorScheme.surface).padding(32.dp),
        verticalArrangement = Arrangement.Center) {
        Text("Dashboard sign-in needed", style = MaterialTheme.typography.headlineMedium, color = MaterialTheme.colorScheme.onSurface)
        Text("Sign in to Home Assistant again. Your dashboards are still saved.", color = MaterialTheme.colorScheme.onSurfaceVariant)
        if (onPhone != null) Button(onClick = onPhone) { Text("Sign in with phone") }
        TextButton(onClick = onTablet) { Text("Sign in on tablet") }
    }
}

@Composable private fun SettingsContent(state: EncoreState, noiseKey: () -> String,
    musicToken: () -> String, onMusicEnable: (String?) -> Unit, onMusicDisconnect: () -> Unit,
    onMusicCommand: (String) -> Unit, onUpdate: () -> Unit, onBluetooth: () -> Unit, auth: DashboardAuth?,
    onChooseHome: () -> Unit, onNotificationSettings: () -> Unit, onSetup: () -> Unit, onMusicRevoke: () -> Unit, close: () -> Unit,
    displayOrigin: String, displayPages: List<DashboardPage>, chooseDashboards: (String) -> Unit,
    updateDashboards: (List<DashboardPage>) -> Unit) {
    var details by remember { mutableStateOf(false) }
    val dashboardSignIn = auth?.takeIf { displayOrigin.startsWith("https://") && supportsDashboardBridge() }
    val account = dashboardSignIn?.saved?.takeIf { it.origin() == displayOrigin }
    Column(Modifier.fillMaxSize().systemBarsPadding().imePadding(), horizontalAlignment = Alignment.CenterHorizontally) {
        Row(Modifier.widthIn(max = 640.dp).fillMaxWidth().padding(horizontal = 20.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
            Text("Satellite settings", Modifier.weight(1f).semantics { heading() }, style = MaterialTheme.typography.headlineSmall)
            TextButton(onClick = close) { Text("Done") }
        }
        Column(Modifier.widthIn(max = 640.dp).fillMaxWidth().verticalScroll(rememberScrollState()).padding(start = 20.dp, end = 20.dp, bottom = 20.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Surface(Modifier.fillMaxWidth(), shape = MaterialTheme.shapes.large, color = MaterialTheme.colorScheme.primaryContainer) {
                Column(Modifier.padding(horizontal = 20.dp, vertical = 16.dp), verticalArrangement = Arrangement.spacedBy(4.dp)) {
                    Text(state.status, Modifier.semantics { liveRegion = LiveRegionMode.Polite }, style = MaterialTheme.typography.titleLarge)
                    Text(state.name, style = MaterialTheme.typography.bodyMedium)
                }
            }
            if (state.music.enabled && !state.music.awaitingServer) MusicControls(state.music, onMusicCommand, state.audioInterrupted)
            HorizontalDivider()
            Column {
                Text("Device", Modifier.semantics { heading() }, style = MaterialTheme.typography.titleMedium)
                SettingsAction("Device setup", onClick = onSetup)
                SettingsAction("Choose Home app", "Set which app opens when you press Home.") { close(); onChooseHome() }
                SettingsAction("Manage notifications") { close(); onNotificationSettings() }
                SettingsAction("Bluetooth proxy", onClick = onBluetooth)
                SettingsAction("Review downloaded update", onClick = onUpdate)
            }
            HorizontalDivider()
            DashboardSettings(displayOrigin, displayPages, chooseDashboards, updateDashboards)
            if (dashboardSignIn != null) {
                account?.let { Text("Signed in as ${it.name()}", style = MaterialTheme.typography.bodyMedium) }
                TextButton(onClick = { close(); dashboardSignIn.start(displayOrigin) }) { Text("Sign in with phone") }
            }
            HorizontalDivider()
            SettingsAction(if (details) "Hide connection details" else "Connection details",
                if (details) null else "Sendspin and pairing keys",
                Modifier.semantics { stateDescription = if (details) "Expanded" else "Collapsed" }) { details = !details }
            if (details) {
                var secret by remember { mutableStateOf("") }
                var musicSecret by remember { mutableStateOf("") }
                var revoke by remember { mutableStateOf(false) }
                var serverDraft by remember { mutableStateOf<String?>(null) }
                serverDraft?.let { draft ->
                    val endpoint = MusicEndpoint.manual(draft)
                    AlertDialog(onDismissRequest = { serverDraft = null }, title = { Text("Specify a Sendspin server") },
                        text = {
                            OutlinedTextField(draft, { serverDraft = it }, label = { Text("Host and port") },
                                modifier = Modifier.fillMaxWidth(), singleLine = true, isError = draft.isNotBlank() && endpoint == null,
                                supportingText = { Text("Enter host:port, optionally followed by a path. Example: music.local:8927") })
                        },
                        confirmButton = {
                            TextButton(enabled = endpoint != null, onClick = { endpoint?.let { serverDraft = null; onMusicEnable(it.setting) } }) {
                                Text("Save and connect")
                            }
                        },
                        dismissButton = { TextButton(onClick = { serverDraft = null }) { Text("Cancel") } })
                }
                if (revoke) AlertDialog(onDismissRequest = { revoke = false }, title = { Text("Revoke Sendspin pairing?") },
                    text = { Text("Music turns off, all trusted servers are removed, and the old pairing token stops working.") },
                    confirmButton = { TextButton(onClick = { revoke = false; musicSecret = ""; onMusicRevoke() }) { Text("Revoke pairing") } },
                    dismissButton = { TextButton(onClick = { revoke = false }) { Text("Cancel") } })
                Text("Sendspin", Modifier.semantics { heading() }, style = MaterialTheme.typography.titleMedium)
                val status = if (state.music.enabled) "Sendspin · ${state.music.status}"
                    else if (state.manualMusicServer == null) "Off · finds a Sendspin server on this network when enabled" else "Off"
                Text(status, Modifier.semantics { liveRegion = LiveRegionMode.Polite }, color = MaterialTheme.colorScheme.onSurfaceVariant)
                if (state.musicProblem.isNotEmpty()) {
                    Text(state.musicProblem, Modifier.semantics { liveRegion = LiveRegionMode.Polite }, color = MaterialTheme.colorScheme.error)
                }
                state.manualMusicServer?.let { Text("Server: $it", color = MaterialTheme.colorScheme.onSurfaceVariant) }
                if (state.music.enabled) SettingsAction("Turn off music", onClick = onMusicDisconnect)
                else SettingsAction("Enable music") { onMusicEnable(state.manualMusicServer) }
                SettingsAction(if (state.manualMusicServer == null) "Specify server" else "Edit server") {
                    serverDraft = state.manualMusicServer.orEmpty()
                }
                if (state.manualMusicServer != null) SettingsAction("Connect by auto-discovery") { onMusicEnable(null) }
                Text("Pairing and recovery", Modifier.semantics { heading() }, style = MaterialTheme.typography.titleMedium)
                if (!state.needsHomeAssistantSetup) {
                    SettingsAction("Reveal ESPHome encryption key") { secret = noiseKey() }
                }
                if (secret.isNotEmpty()) SelectionContainer { Text(secret, style = MaterialTheme.typography.bodySmall) }
                SettingsAction(if (musicSecret.isEmpty()) "Reveal Sendspin pairing token" else "Hide Sendspin pairing token") {
                    musicSecret = if (musicSecret.isEmpty()) musicToken() else ""
                }
                if (musicSecret.isNotEmpty()) SelectionContainer { Text(musicSecret, style = MaterialTheme.typography.bodySmall) }
                SettingsAction("Revoke all Sendspin trust") { revoke = true }
                account?.let { Text("Dashboard session ID: ${it.clientID()}", style = MaterialTheme.typography.bodySmall) }
            }
        }
    }
}

@Composable private fun SettingsAction(label: String, detail: String? = null, modifier: Modifier = Modifier, onClick: () -> Unit) {
    Column(modifier.fillMaxWidth().clickable(role = Role.Button, onClick = onClick)
        .heightIn(min = 48.dp).padding(vertical = 8.dp), verticalArrangement = Arrangement.Center) {
        Text(label, style = MaterialTheme.typography.bodyLarge, color = MaterialTheme.colorScheme.primary)
        if (detail != null) Text(detail, style = MaterialTheme.typography.bodySmall, color = MaterialTheme.colorScheme.onSurfaceVariant)
    }
}
