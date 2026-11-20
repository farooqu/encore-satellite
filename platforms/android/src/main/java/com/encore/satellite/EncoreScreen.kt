package com.encore.satellite

import android.annotation.SuppressLint
import android.net.Uri
import android.view.View
import android.webkit.*
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.detectHorizontalDragGestures
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.blur
import androidx.compose.ui.draw.clip
import androidx.compose.ui.draw.drawWithContent
import androidx.compose.ui.geometry.CornerRadius
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.BlendMode
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.CompositingStrategy
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.ui.graphics.lerp
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalView
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.*
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import com.encore.engine.mobile.Mobile
import kotlinx.coroutines.launch

private val Ink = Color(0xff0c141d)
private val Mint = Color(0xff97e6d4)
private val Muted = Color(0xffa0afbd)

@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun EncoreScreen(state: EncoreState, onStart: () -> Unit, onTalk: () -> Unit, onStop: () -> Unit,
    onMuted: (Boolean) -> Unit, noiseKey: () -> String,
    musicToken: () -> String, onMusicConnect: (String) -> Unit, onMusicDisconnect: () -> Unit,
    onMusicCommand: (String) -> Unit, onDismiss: () -> Unit, onUpdate: () -> Unit = {},
    onBluetooth: () -> Unit = {},
    reducedMotion: Boolean = systemReducedMotion(),
    onMonitorOverlay: (Long, Boolean) -> Unit = { _, _ -> },
    onPairHomeAssistant: () -> Unit = {},
    dashboardContent: @Composable () -> Unit = { Dashboard(state, onPairHomeAssistant) }) {
    var settings by remember { mutableStateOf(false) }
    val drawer = rememberDrawerState(DrawerValue.Closed)
    val scope = rememberCoroutineScope()
    val monitorId = state.monitorId.takeIf { state.microphoneVisible } ?: 0L
    LaunchedEffect(monitorId) {
        if (monitorId != 0L) { settings = false; drawer.close() }
    }
    MaterialTheme(colorScheme = darkColorScheme(primary = Mint, background = Ink, surface = Color(0xff152330))) {
        ModalNavigationDrawer(drawerState = drawer, gesturesEnabled = drawer.isOpen, drawerContent = {
            ModalDrawerSheet(Modifier.width(340.dp)) {
                if (drawer.isOpen || drawer.targetValue == DrawerValue.Open) Column(Modifier.fillMaxHeight().verticalScroll(rememberScrollState()).padding(20.dp),
                    verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    Text("Satellite controls", style = MaterialTheme.typography.titleLarge)
                    Text(state.status, Modifier.semantics { liveRegion = LiveRegionMode.Polite }, color = Muted)
                    if (!state.running) TextButton(onClick = { onStart(); scope.launch { drawer.close() } }) { Text("Start satellite") }
                    else if (!state.microphoneReady && !state.muted) TextButton(onClick = { onStart(); scope.launch { drawer.close() } }) { Text("Start microphone") }
                    else TextButton(onClick = { onTalk(); scope.launch { drawer.close() } },
                        enabled = state.voiceKnown && state.connected && state.microphoneReady && state.captureReady && !state.muted &&
                            !state.inhibited && state.talkAllowed && state.talkReady && !state.interactionVisible) { Text("Talk") }
                    if (state.interactionVisible) TextButton(onClick = { onStop(); scope.launch { drawer.close() } }) { Text("Stop voice turn") }
                    TextButton(onClick = { onMuted(!state.muted) }, enabled = state.microphoneReady || state.muted) { Text(if (state.muted) "Unmute mic" else "Mute mic") }
                    TextButton(onClick = { settings = true; scope.launch { drawer.close() } },
                        enabled = !state.interactionVisible) { Text("Settings") }
                    if (state.music.enabled) MusicControls(state.music, onMusicCommand, state.voiceActive, state.audioInterrupted)
                    TextButton(onClick = { scope.launch { drawer.close() } }) { Text("Close controls") }
                }
            }
        }) {
            BoxWithConstraints(Modifier.fillMaxSize().background(Ink)) {
                Box(Modifier.fillMaxSize().blur(if (state.interactionVisible) 24.dp else 0.dp)
                    .semantics { if (state.interactionVisible) hideFromAccessibility() }) { dashboardContent() }
                if (state.interactionVisible) {
                    // The light frost is also the gesture shield; it never forwards taps to WebView.
                    Box(Modifier.fillMaxSize().background(Brush.verticalGradient(listOf(
                        Color(0xffedf3f6).copy(alpha = .88f), Color(0xffedf3f6).copy(alpha = .82f),
                        Color(0xffedf3f6).copy(alpha = .94f)))).testTag("dashboard-shield")
                        .pointerInput(Unit) { awaitPointerEventScope { while (true) { awaitPointerEvent().changes.forEach { it.consume() } } } })
                    val top = maxHeight * if (maxHeight < 320.dp) .07f else .22f
                    VoiceOverlay(state, reducedMotion,
                        Modifier.align(Alignment.TopCenter).padding(top = top, start = 8.dp, end = 8.dp).widthIn(max = 880.dp)
                            .fillMaxWidth(.96f).height((maxHeight - top - 96.dp).coerceAtLeast(72.dp)),
                        monitorId.takeUnless { settings || drawer.isOpen || drawer.targetValue == DrawerValue.Open } ?: 0L,
                        onMonitorOverlay)
                }
                if (state.problem.isNotEmpty()) Surface(Modifier.align(Alignment.TopCenter).statusBarsPadding()
                    .padding(horizontal = 20.dp, vertical = 4.dp), shape = RoundedCornerShape(12.dp)) {
                    Row(Modifier.padding(horizontal = 12.dp, vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                        Text(state.problem, Modifier.weight(1f), color = Color(0xffffd0a4), maxLines = 2,
                            overflow = androidx.compose.ui.text.style.TextOverflow.Ellipsis)
                        IconButton(onClick = onDismiss, Modifier.semantics { contentDescription = "Dismiss notice" }) { Text("×", fontSize = 24.sp) }
                    }
                }
                // Restrict the closed-drawer gesture to the edge, preserving dashboard swipes elsewhere.
                Box(Modifier.align(Alignment.CenterStart).width(20.dp).fillMaxHeight().testTag("controls-edge")
                    .pointerInput(monitorId) {
                        var distance = 0f
                        detectHorizontalDragGestures(onDragStart = { distance = 0f }, onDragEnd = {
                            if (distance > 48.dp.toPx() && monitorId == 0L) scope.launch { drawer.open() }
                        }) { change, drag -> change.consume(); distance += drag }
                    })
                Row(Modifier.align(Alignment.BottomCenter).navigationBarsPadding().padding(16.dp),
                    horizontalArrangement = Arrangement.spacedBy(16.dp)) {
                    FilledIconButton(onClick = {
                        if (state.interactionVisible) onStop() else if (!state.running || (!state.microphoneReady && !state.muted)) onStart() else onTalk()
                    }, enabled = state.interactionVisible || !state.running || (!state.microphoneReady && !state.muted) ||
                        (state.voiceKnown && state.connected && state.microphoneReady && state.captureReady &&
                            !state.muted && !state.inhibited && state.talkAllowed && state.talkReady),
                        colors = IconButtonDefaults.filledIconButtonColors(containerColor = Ink, contentColor = Mint,
                            disabledContainerColor = Color(0xff38434d), disabledContentColor = Color(0xff9ca8b1)),
                        modifier = Modifier.size(56.dp).clip(CircleShape)
                            .testTag(if (state.interactionVisible) "stop" else "talk").semantics {
                                contentDescription = when {
                                    state.interactionVisible -> "Stop voice turn"
                                    !state.running -> "Start satellite"
                                    !state.microphoneReady && !state.muted -> "Start microphone"
                                    else -> "Talk"
                                }
                                stateDescription = state.status
                                customActions = if (monitorId != 0L) emptyList() else listOf(CustomAccessibilityAction("Open satellite controls") {
                                    scope.launch { drawer.open() }; true
                                })
                            }) {
                        val color = LocalContentColor.current
                        if (state.interactionVisible) Canvas(Modifier.size(18.dp)) {
                            drawRoundRect(color, cornerRadius = CornerRadius(3.dp.toPx(), 3.dp.toPx()))
                        } else MicrophoneGlyph(color, false)
                    }
                    FilledIconToggleButton(checked = state.muted, onCheckedChange = onMuted, enabled = state.microphoneReady || state.muted,
                        colors = IconButtonDefaults.filledIconToggleButtonColors(containerColor = Ink, contentColor = Mint,
                            checkedContainerColor = Color(0xffa23b45), checkedContentColor = Color.White),
                        modifier = Modifier.size(56.dp).clip(CircleShape).testTag("privacy").semantics {
                            contentDescription = if (state.muted) "Unmute mic" else "Mute mic"
                            stateDescription = if (state.muted) "Microphone muted" else "Microphone unmuted"
                            // Alternate drawer access without a visible menu button.
                            customActions = if (monitorId != 0L) emptyList() else listOf(CustomAccessibilityAction("Open satellite controls") {
                                scope.launch { drawer.open() }; true
                            })
                        }) {
                        MicrophoneGlyph(LocalContentColor.current, true)
                    }
                }
            }
        }
        if (settings) SettingsDialog(state, noiseKey, musicToken, onMusicConnect, onMusicDisconnect, onUpdate, onBluetooth) { settings = false }
    }
}

@Composable private fun MicrophoneGlyph(color: Color, crossedOut: Boolean) {
    Canvas(Modifier.size(26.dp)) {
        val unit = size.width / 24
        val stroke = 1.8f * unit
        drawRoundRect(color, Offset(9 * unit, 3 * unit), Size(6 * unit, 11 * unit),
            CornerRadius(3 * unit), style = Stroke(stroke))
        drawArc(color, 0f, 180f, false, Offset(6 * unit, 6 * unit), Size(12 * unit, 12 * unit),
            style = Stroke(stroke, cap = StrokeCap.Round))
        drawLine(color, Offset(12 * unit, 18 * unit), Offset(12 * unit, 21 * unit), stroke, StrokeCap.Round)
        drawLine(color, Offset(9 * unit, 21 * unit), Offset(15 * unit, 21 * unit), stroke, StrokeCap.Round)
        if (crossedOut) drawLine(color, Offset(3 * unit, 3 * unit), Offset(21 * unit, 21 * unit), stroke, StrokeCap.Round)
    }
}

@Composable private fun VoiceOverlay(state: EncoreState, reducedMotion: Boolean, modifier: Modifier,
    monitorId: Long, onMonitorOverlay: (Long, Boolean) -> Unit) {
    Column(modifier.testTag("voice-overlay").padding(12.dp)) {
        val indicator = if (monitorId != 0L) key(monitorId) {
            val view = LocalView.current
            val notify by rememberUpdatedState(onMonitorOverlay)
            val drawn = remember(view) { Runnable { if (view.isShown && view.hasWindowFocus()) notify(monitorId, true) } }
            val reported = remember(view) { booleanArrayOf(false) }
            DisposableEffect(view, drawn) {
                onDispose {
                    view.removeCallbacks(drawn)
                    notify(monitorId, false)
                }
            }
            Modifier.drawWithContent {
                drawContent()
                if (!reported[0] && state.waveform.isNotEmpty() && view.isShown && view.hasWindowFocus()) {
                    reported[0] = true
                    view.postOnAnimation(drawn)
                }
            }
        } else Modifier
        VoiceVisualization(state, reducedMotion,
            Modifier.fillMaxWidth().height(48.dp).testTag("voice-visual").then(indicator), lightSurface = true)
        if (state.heard.isNotEmpty() || state.reply.isNotEmpty()) key(state.generation, state.turn) {
            Box(Modifier.weight(1f).fillMaxWidth().testTag("voice-transcripts")
                .graphicsLayer { compositingStrategy = CompositingStrategy.Offscreen }
                .drawWithContent {
                    drawContent()
                    // Fade actual text alpha; never paint an opaque strip over the frost.
                    drawRect(Brush.verticalGradient(listOf(Color.Transparent, Color.Black), endY = 20.dp.toPx()),
                        blendMode = BlendMode.DstIn)
                }) {
                Column(Modifier.fillMaxWidth().verticalScroll(rememberScrollState()).padding(vertical = 16.dp),
                    verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    if (state.heard.isNotEmpty()) Text(state.heard, Modifier.fillMaxWidth(),
                        color = lerp(state.inputBraidColor, Ink, .64f).copy(alpha = .88f),
                        fontSize = 19.sp, lineHeight = 27.sp, textAlign = TextAlign.Center)
                    if (state.reply.isNotEmpty()) Text(state.reply, Modifier.fillMaxWidth(), color = lerp(OutputLilac, Ink, .58f),
                        fontSize = 21.sp, lineHeight = 29.sp, textAlign = TextAlign.Center)
                }
            }
        }
    }
}

@OptIn(ExperimentalLayoutApi::class)
@Composable private fun MusicControls(music: MusicState, command: (String) -> Unit, ducked: Boolean, interrupted: Boolean) {
    Surface(Modifier.padding(12.dp).fillMaxWidth(), shape = RoundedCornerShape(16.dp)) {
        Column(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
            Text(music.title.ifEmpty { "Sendspin · ${music.status}" }, maxLines = 1, color = Color.White)
            Text(listOf(music.artist, if (ducked) "Quiet for voice" else "").filter { it.isNotEmpty() }.joinToString(" · "), color = Muted, maxLines = 1, fontSize = 12.sp)
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

@SuppressLint("SetJavaScriptEnabled")
@Composable private fun Dashboard(state: EncoreState, onPairHomeAssistant: () -> Unit) {
    if (state.needsHomeAssistantSetup) {
        Box(Modifier.fillMaxSize().background(Color(0xff101d28)).padding(bottom = 88.dp), contentAlignment = Alignment.Center) {
            Column(Modifier.widthIn(max = 560.dp).verticalScroll(rememberScrollState()).padding(24.dp),
                verticalArrangement = Arrangement.spacedBy(16.dp)) {
                Text("Home Assistant setup", style = MaterialTheme.typography.headlineMedium, color = Color.White)
                Text(when {
                    !state.running -> "Start the satellite to set up its ESPHome controls."
                    state.esphome == "waiting" -> "In Home Assistant, go to Settings → Devices & services and add ${state.name} under Discovered. You won't need to enter a key. The pairing window closes after two minutes."
                    state.esphome == "expired" -> "The pairing window expired. Start pairing again, then add the satellite in Home Assistant."
                    else -> "Pair on a trusted local network to add this satellite in Home Assistant without entering an encryption key."
                }, Modifier.semantics { liveRegion = LiveRegionMode.Polite }, color = Muted)
                if (state.running && state.esphome != "waiting") {
                    Button(onClick = onPairHomeAssistant) { Text("Pair with Home Assistant") }
                }
            }
        }
    } else if (state.dashboard.isEmpty()) {
        Column(Modifier.fillMaxSize().background(Color(0xff101d28)).padding(32.dp), verticalArrangement = Arrangement.Center) {
            Text("Your home, at a glance.", color = Color.White, fontSize = 30.sp)
            Spacer(Modifier.height(16.dp))
            Text("Set the full Dashboard URL in the device's ESPHome integration. One URL includes your HA address and dashboard path.", color = Muted, fontSize = 17.sp)
        }
    } else key(state.origin) {
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
            // No addJavascriptInterface, downloads, external intents or microphone/camera WebChromeClient grants.
        } }, update = { view ->
            view.importantForAccessibility = if (state.interactionVisible) View.IMPORTANT_FOR_ACCESSIBILITY_NO_HIDE_DESCENDANTS else View.IMPORTANT_FOR_ACCESSIBILITY_AUTO
            // Also release keyboard focus: the touch shield alone cannot stop a focused dashboard input.
            if (state.interactionVisible) view.clearFocus()
            view.isFocusable = !state.interactionVisible
            view.isFocusableInTouchMode = !state.interactionVisible
            val desired = "${state.reload}:${state.dashboard}"
            if (view.tag != desired) { view.tag = desired; view.loadUrl(state.dashboard) }
        }, modifier = Modifier.fillMaxSize().testTag("dashboard"))
    }
}

@Composable private fun SettingsDialog(state: EncoreState, noiseKey: () -> String,
    musicToken: () -> String, onMusicConnect: (String) -> Unit, onMusicDisconnect: () -> Unit, onUpdate: () -> Unit, onBluetooth: () -> Unit, close: () -> Unit) {
    var sendspin by remember { mutableStateOf("") }
    var secret by remember { mutableStateOf("") }
    AlertDialog(onDismissRequest = close, title = { Text("Satellite settings") }, confirmButton = { TextButton(onClick = close) { Text("Done") } },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                Text("${state.name} · Incubator only", color = Mint)
                Text("Incubator voice uses trusted-LAN TCP 6055. ESPHome controls use encrypted TCP 6053. Bluetooth capabilities depend on the running Android APIs; no Assist backend.", fontSize = 12.sp)
                TextButton(onClick = onBluetooth) { Text("Bluetooth proxy setup") }
                Text("Set one full Dashboard URL through ESPHome, such as https://home.example/lovelace/home. It supplies both the HA address and dashboard path; no local origin setup is needed. Sign in to HA in the dashboard itself. Navigation stays within the configured origin.", fontSize = 12.sp)
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
        })
}
