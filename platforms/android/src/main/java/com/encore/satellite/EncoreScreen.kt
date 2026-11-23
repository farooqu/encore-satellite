package com.encore.satellite

import android.content.Context
import android.net.Uri
import android.os.Build
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.Canvas
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
import androidx.compose.ui.focus.focusProperties
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.input.pointer.PointerEventPass
import androidx.compose.ui.input.pointer.changedToUpIgnoreConsumed
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.*
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
    musicToken: () -> String, onMusicConnect: (String) -> Unit, onMusicDisconnect: () -> Unit,
    onMusicCommand: (String) -> Unit, onDismiss: () -> Unit, onUpdate: () -> Unit = {},
    onBluetooth: () -> Unit = {},
    reducedMotion: Boolean = systemReducedMotion(),
    onMonitorOverlay: (Long, Boolean) -> Unit = { _, _ -> },
    onPairHomeAssistant: () -> Unit = {},
    microphonePermissionGranted: Boolean = true,
    dashboardAuth: DashboardAuth? = null,
    dashboardContent: (@Composable () -> Unit)? = null,
    onChooseHome: () -> Unit = {}, onNotificationSettings: () -> Unit = {}, onSetup: () -> Unit = {},
    onStopAlert: (Long) -> Unit = { onStop() }) {
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
                        dashboardAuth, settingsVisible, displayOrigin, displayPages, swipeEnabled, reducedMotion)
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
                        else SettingsContent(state, noiseKey, musicToken, onMusicConnect, onMusicDisconnect, onMusicCommand, onUpdate, onBluetooth,
                            dashboardAuth, onChooseHome, onNotificationSettings, { closeSettings(); onSetup() }, ::closeSettings,
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

@OptIn(ExperimentalLayoutApi::class)
@Composable private fun MusicControls(music: MusicState, command: (String) -> Unit, interrupted: Boolean) {
    Column(Modifier.fillMaxWidth()) {
        Text(music.title.ifEmpty { "Sendspin · ${music.status}" }, maxLines = 1, style = MaterialTheme.typography.titleMedium)
        if (music.artist.isNotEmpty()) Text(music.artist, color = MaterialTheme.colorScheme.onSurfaceVariant,
            maxLines = 1, style = MaterialTheme.typography.bodySmall)
        FlowRow {
            if (music.playing && interrupted) TextButton(onClick = { command("resume") }) { Text("Resume audio") }
            val action = if (music.playing) Mobile.SendspinPause else Mobile.SendspinPlay
            TextButton(onClick = { command(action) }, enabled = action in music.commands) { Text(if (music.playing) "Pause" else "Play") }
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
    origin: String, pages: List<DashboardPage>, swipeEnabled: Boolean, reducedMotion: Boolean) {
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
        DashboardCarousel(pages, swipeEnabled, reducedMotion) { page, visible ->
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
    musicToken: () -> String, onMusicConnect: (String) -> Unit, onMusicDisconnect: () -> Unit,
    onMusicCommand: (String) -> Unit, onUpdate: () -> Unit, onBluetooth: () -> Unit, auth: DashboardAuth?,
    onChooseHome: () -> Unit, onNotificationSettings: () -> Unit, onSetup: () -> Unit, close: () -> Unit,
    displayOrigin: String, displayPages: List<DashboardPage>, chooseDashboards: (String) -> Unit,
    updateDashboards: (List<DashboardPage>) -> Unit) {
    var details by remember { mutableStateOf(false) }
    var sendspin by remember { mutableStateOf("") }
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
            if (state.music.enabled) MusicControls(state.music, onMusicCommand, state.audioInterrupted)
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
                Text("Sendspin", Modifier.semantics { heading() }, style = MaterialTheme.typography.titleMedium)
                OutlinedTextField(sendspin, { sendspin = it }, Modifier.fillMaxWidth(), label = { Text("Sendspin host:port") }, singleLine = true)
                Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                    TextButton(onClick = { onMusicConnect(sendspin) }) { Text("Connect music") }
                    TextButton(onClick = onMusicDisconnect) { Text("Disconnect") }
                }
                Text("Pairing and recovery", Modifier.semantics { heading() }, style = MaterialTheme.typography.titleMedium)
                if (!state.needsHomeAssistantSetup) {
                    SettingsAction("Reveal ESPHome encryption key") { secret = noiseKey() }
                }
                SettingsAction("Reveal Sendspin pairing token") { secret = musicToken() }
                if (secret.isNotEmpty()) SelectionContainer { Text(secret, style = MaterialTheme.typography.bodySmall) }
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
