package com.encore.satellite

import android.annotation.SuppressLint
import android.net.Uri
import android.view.View
import android.webkit.*
import androidx.activity.compose.BackHandler
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
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
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.semantics.*
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
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
    onChooseHome: () -> Unit = {}, onNotificationSettings: () -> Unit = {}, onSetup: () -> Unit = {}) {
    val voiceVisible = state.interactionVisible || state.microphoneVisible
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
    var settingsVisible by remember(legacyVisible || panel != null) { mutableStateOf(false) }
    fun closeSettings() { settingsVisible = false }
    fun closePanel() {
        transcript = null
        if (panel?.finished == false) onStop()
    }
    MaterialTheme(colorScheme = darkColorScheme(primary = Mint, background = Ink, surface = Color(0xff152330))) {
        BoxWithConstraints(Modifier.fillMaxSize().background(Ink)) {
            val landscape = maxWidth > maxHeight
            Box(Modifier.fillMaxSize().semantics { if (settingsVisible) hideFromAccessibility() }
                .focusProperties { canFocus = !settingsVisible }) {
                Box(Modifier.fillMaxSize()) {
                    if (dashboardContent != null) dashboardContent()
                    else Dashboard(state, microphonePermissionGranted, onStart, onPairHomeAssistant, dashboardAuth, settingsVisible)
                }
                if (!settingsVisible && panel == null) {
                    val buttons: @Composable () -> Unit = {
                        val startMicrophone = state.running && !state.microphoneReady && !state.muted
                        FilledIconButton(onClick = when { legacyVisible -> onStop; startMicrophone -> onStart; else -> onTalk },
                            enabled = legacyVisible || startMicrophone ||
                                (state.running && state.voiceKnown && state.connected && state.microphoneReady && state.captureReady &&
                                    !state.muted && !state.inhibited && state.talkAllowed && state.talkReady),
                            colors = IconButtonDefaults.filledIconButtonColors(containerColor = Ink, contentColor = Mint,
                                disabledContainerColor = Color(0xff38434d), disabledContentColor = Color(0xff9ca8b1)),
                            modifier = Modifier.size(56.dp).testTag(if (legacyVisible) "stop" else "talk").semantics {
                                contentDescription = when { legacyVisible -> "Stop voice turn"; startMicrophone -> "Start microphone"; else -> "Trigger assistant" }
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
                        MuteButton(state, onMuted)
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
            if (legacyVisible) {
                BackHandler(onBack = onStop)
                VoiceEdgeGlow(state, reducedMotion, Modifier.matchParentSize(), onMonitorOverlay)
            } else if (panel != null) {
                BackHandler(onBack = ::closePanel)
                VoiceTranscriptPanel(panel, state, Modifier.align(Alignment.BottomCenter).widthIn(max = 640.dp)
                    .fillMaxWidth().heightIn(max = maxHeight / 2), onClose = ::closePanel)
            } else if (settingsVisible) {
                // Keep Settings in the Activity window so a remote turn can take over without losing window focus.
                BackHandler(onBack = ::closeSettings)
                Surface(Modifier.fillMaxSize().testTag("settings-screen").semantics { paneTitle = "Satellite settings" }) {
                    SettingsContent(state, noiseKey, musicToken, onMusicConnect, onMusicDisconnect, onMusicCommand, onUpdate, onBluetooth,
                        dashboardAuth, onChooseHome, onNotificationSettings, { closeSettings(); onSetup() }, ::closeSettings)
                }
            } else if (pendingWake) VoiceWakeCue(Modifier.align(Alignment.BottomCenter).navigationBarsPadding()
                .padding(bottom = if (landscape) 8.dp else 82.dp))
            if (!settingsVisible && state.problem.isNotEmpty()) {
                Surface(Modifier.align(Alignment.TopCenter).statusBarsPadding().padding(horizontal = 20.dp, vertical = 4.dp),
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
            if (attempt.origin == state.origin) DashboardEnrollmentDialog(state.name, attempt.origin, attempt.qr,
                attempt.candidate?.name(), attempt.error, onCancel = dashboardAuth::cancel,
                onAccept = { dashboardAuth.accept(attempt) }, onRetry = { dashboardAuth.start(attempt.origin) })
            else if (state.running) LaunchedEffect(attempt) { dashboardAuth.cancel() }
        }
    }
}

@Composable private fun MuteButton(state: EncoreState, onMuted: (Boolean) -> Unit) {
    FilledIconToggleButton(checked = state.muted, onCheckedChange = onMuted, enabled = state.microphoneReady || state.muted,
        colors = IconButtonDefaults.filledIconToggleButtonColors(containerColor = Ink, contentColor = Mint,
            checkedContainerColor = Color(0xffa23b45), checkedContentColor = Color.White),
        modifier = Modifier.size(56.dp).testTag("privacy").semantics {
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
    Surface(Modifier.fillMaxWidth(), shape = RoundedCornerShape(16.dp)) {
        Column(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
            Text(music.title.ifEmpty { "Sendspin · ${music.status}" }, maxLines = 1, color = Color.White)
            if (music.artist.isNotEmpty()) Text(music.artist, color = Muted, maxLines = 1, fontSize = 12.sp)
            FlowRow {
                if (music.playing && interrupted) TextButton(onClick = { command("resume") }) { Text("Resume audio") }
                val action = if (music.playing) Mobile.SendspinPause else Mobile.SendspinPlay
                TextButton(onClick = { command(action) }, enabled = action in music.commands) { Text(if (music.playing) "Pause" else "Play") }
                TextButton(onClick = { command(Mobile.SendspinNext) }, enabled = Mobile.SendspinNext in music.commands) { Text("Next") }
            }
        }
    }
}

internal fun trustedNavigation(origin: String, value: String): Boolean {
    val a = Uri.parse(origin)
    val b = Uri.parse(value)
    return b.scheme in listOf("https", "http") && a.scheme == b.scheme && a.encodedAuthority == b.encodedAuthority && b.userInfo == null
}

@Composable private fun Dashboard(state: EncoreState, microphonePermissionGranted: Boolean,
    onRequestPermission: () -> Unit, onPairHomeAssistant: () -> Unit, auth: DashboardAuth?, settingsVisible: Boolean) {
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
    } else if (state.dashboard.isEmpty()) {
        Column(Modifier.fillMaxSize().background(Color(0xff101d28)).padding(32.dp), verticalArrangement = Arrangement.Center) {
            Text("Voice satellite", color = Color.White, fontSize = 30.sp)
            Spacer(Modifier.height(16.dp))
            Text(state.voiceReadiness, color = Muted, fontSize = 17.sp, modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite })
        }
    } else if (auth != null && auth.lostOrigin == state.origin) {
        DashboardSignInNeeded(onPhone = if (supportsDashboardBridge()) ({ auth.start(state.origin) }) else null,
            onTablet = auth::manualSignIn)
    } else DashboardWebView(state, auth, settingsVisible)
}

@Composable
internal fun DashboardSignInNeeded(onPhone: (() -> Unit)?, onTablet: () -> Unit) {
    Column(Modifier.fillMaxSize().background(Color(0xff101d28)).padding(32.dp),
        verticalArrangement = Arrangement.Center) {
        Text("Dashboard sign-in needed", style = MaterialTheme.typography.headlineMedium, color = Color.White)
        Text("Your Home Assistant session ended. Sign in again without changing this satellite's dashboard URL.", color = Muted)
        if (onPhone != null) Button(onClick = onPhone) { Text("Sign in with phone") }
        TextButton(onClick = onTablet) { Text("Sign in on tablet") }
    }
}

@SuppressLint("SetJavaScriptEnabled")
@Composable private fun DashboardWebView(state: EncoreState, auth: DashboardAuth?, settingsVisible: Boolean) {
    val session = auth?.saved?.takeIf { it.origin() == state.origin && supportsDashboardBridge() }
    key(state.origin, session) {
        var webView by remember { mutableStateOf<WebView?>(null) }
        DisposableEffect(Unit) { onDispose { webView?.destroy(); webView = null } }
        AndroidView(factory = { context -> WebView(context).apply {
            webView = this
            setBackgroundColor(android.graphics.Color.rgb(16, 29, 40))
            settings.javaScriptEnabled = true
            settings.domStorageEnabled = true
            settings.allowFileAccess = false
            settings.allowContentAccess = false
            settings.mixedContentMode = WebSettings.MIXED_CONTENT_NEVER_ALLOW
            settings.setSupportMultipleWindows(false)
            CookieManager.getInstance().setAcceptThirdPartyCookies(this, false)
            webViewClient = object : WebViewClient() {
                override fun shouldOverrideUrlLoading(view: WebView, request: WebResourceRequest) = !trustedNavigation(state.origin, request.url.toString())
            }
            if (session != null) auth.installBridge(this, session)
            // No addJavascriptInterface, downloads, external intents or microphone/camera WebChromeClient grants.
        } }, update = { view ->
            view.importantForAccessibility = if (settingsVisible) View.IMPORTANT_FOR_ACCESSIBILITY_NO_HIDE_DESCENDANTS else View.IMPORTANT_FOR_ACCESSIBILITY_AUTO
            if (settingsVisible) view.clearFocus()
            view.isFocusable = !settingsVisible
            view.isFocusableInTouchMode = !settingsVisible
            val desired = "${state.reload}:${state.dashboard}"
            if (view.tag != desired) { view.tag = desired; view.loadUrl(dashboardLoadUrl(state.dashboard, session != null)) }
        }, modifier = Modifier.fillMaxSize().testTag("dashboard"))
    }
}

@Composable private fun SettingsContent(state: EncoreState, noiseKey: () -> String,
    musicToken: () -> String, onMusicConnect: (String) -> Unit, onMusicDisconnect: () -> Unit,
    onMusicCommand: (String) -> Unit, onUpdate: () -> Unit, onBluetooth: () -> Unit, auth: DashboardAuth?,
    onChooseHome: () -> Unit, onNotificationSettings: () -> Unit, onSetup: () -> Unit, close: () -> Unit) {
    var sendspin by remember { mutableStateOf("") }
    var secret by remember { mutableStateOf("") }
    Column(Modifier.fillMaxSize().systemBarsPadding().imePadding(), horizontalAlignment = Alignment.CenterHorizontally) {
        Row(Modifier.widthIn(max = 640.dp).fillMaxWidth().padding(horizontal = 20.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
            Text("Satellite settings", Modifier.weight(1f).semantics { heading() }, style = MaterialTheme.typography.titleLarge)
            TextButton(onClick = close) { Text("Done") }
        }
        Column(Modifier.widthIn(max = 640.dp).fillMaxWidth().verticalScroll(rememberScrollState()).padding(start = 20.dp, end = 20.dp, bottom = 20.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp)) {
            Text("${state.name} · Incubator only", color = Mint)
            Text(state.status, Modifier.semantics { liveRegion = LiveRegionMode.Polite }, color = Muted)
            TextButton(onClick = onSetup) { Text("Setup") }
            TextButton(onClick = { close(); onNotificationSettings() }) { Text("Manage notifications") }
            Text("Android Home", Modifier.semantics { heading() }, style = MaterialTheme.typography.titleMedium)
            Text("Choose Encore Incubator in Android to open Encore when you press Home. To switch back, choose your previous launcher in the same screen.")
            TextButton(onClick = { close(); onChooseHome() }) { Text("Choose Home app") }
            HorizontalDivider()
            if (state.music.enabled) MusicControls(state.music, onMusicCommand, state.audioInterrupted)
            Text("Incubator voice uses trusted-LAN TCP 6055. ESPHome controls use encrypted TCP 6053. Bluetooth capabilities depend on the running Android APIs; no Assist backend.", fontSize = 12.sp)
            TextButton(onClick = onBluetooth) { Text("Bluetooth proxy setup") }
            Text("Set one full Dashboard URL through ESPHome, such as https://home.example/lovelace/home. Sign in on the tablet, or use your phone with Home Assistant 2026.11+ over HTTPS. Navigation stays within the configured origin.", fontSize = 12.sp)
            if (auth != null && state.origin.startsWith("https://") && supportsDashboardBridge()) {
                auth.saved?.takeIf { it.origin() == state.origin }?.let {
                    Text("Dashboard account: ${it.name()}\nSession ID: ${it.clientID()}", fontSize = 12.sp)
                }
                TextButton(onClick = { close(); auth.start(state.origin) }) { Text("Sign in with phone") }
            }
            if (!state.needsHomeAssistantSetup) {
                TextButton(onClick = { secret = noiseKey() }) { Text("Reveal ESPHome encryption key") }
            }
            Text("Choose stable/dev, check, and request updates through ESPHome. Installing a downloaded APK needs approval on this device.", fontSize = 12.sp)
            TextButton(onClick = onUpdate) { Text("Review downloaded update") }
            HorizontalDivider()
            OutlinedTextField(sendspin, { sendspin = it }, label = { Text("Sendspin host:port") }, singleLine = true)
            Row {
                TextButton(onClick = { onMusicConnect(sendspin) }) { Text("Connect music") }
                TextButton(onClick = onMusicDisconnect) { Text("Disconnect") }
            }
            TextButton(onClick = { secret = musicToken() }) { Text("Reveal Sendspin pairing token") }
            if (secret.isNotEmpty()) SelectionContainer { Text(secret, fontSize = 12.sp) }
        }
    }
}
