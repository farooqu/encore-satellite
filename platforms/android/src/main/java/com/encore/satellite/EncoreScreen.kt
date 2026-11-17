package com.encore.satellite

import android.annotation.SuppressLint
import android.net.Uri
import android.view.View
import android.webkit.*
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.testTag
import androidx.compose.ui.semantics.*
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import androidx.compose.ui.viewinterop.AndroidView
import kotlin.math.PI
import kotlin.math.sin

private val Ink = Color(0xff0c141d)
private val Mint = Color(0xff97e6d4)
private val Muted = Color(0xffa0afbd)

@Composable
fun EncoreScreen(state: EncoreState, onStart: () -> Unit, onTalk: () -> Unit, onStop: () -> Unit,
    onMuted: (Boolean) -> Unit, noiseKey: () -> String,
    musicToken: () -> String, onMusicConnect: (String) -> Unit, onMusicDisconnect: () -> Unit,
    onMusicCommand: (String) -> Unit, onDismiss: () -> Unit, onUpdate: () -> Unit = {},
    dashboardContent: @Composable () -> Unit = { Dashboard(state) }) {
    var settings by remember { mutableStateOf(false) }
    MaterialTheme(colorScheme = darkColorScheme(primary = Mint, background = Ink, surface = Color(0xff152330))) {
        Box(Modifier.fillMaxSize().background(Ink).systemBarsPadding()) {
            Column(Modifier.fillMaxSize()) {
                Row(Modifier.fillMaxWidth().padding(horizontal = 20.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
                    Column(Modifier.weight(1f)) {
                        Text("encore", fontSize = 22.sp, letterSpacing = 3.sp, color = Color.White)
                        Text(if (state.muted) "MICROPHONE OFF" else if (state.connected) "INCUBATOR · ${state.models} LOCAL MODELS" else "INCUBATOR · NOT CONNECTED",
                            color = if (state.muted) Color(0xffffc88b) else Muted, fontSize = 10.sp, letterSpacing = 1.sp)
                    }
                    if (!state.running) TextButton(onClick = onStart) { Text("Start satellite") }
                    else TextButton(onClick = onTalk, enabled = state.connected && !state.muted && !state.voiceActive) { Text("Talk") }
                    TextButton(onClick = { onMuted(!state.muted) }, modifier = Modifier.testTag("privacy")) {
                        Text(if (state.muted) "Unmute mic" else "Mute mic")
                    }
                    TextButton(onClick = { settings = true }, enabled = !state.voiceActive) { Text("Settings") }
                }
                Box(Modifier.weight(1f).fillMaxWidth().padding(horizontal = 12.dp).clip(RoundedCornerShape(18.dp))) {
                    Box(Modifier.fillMaxSize().semantics { if (state.voiceActive) hideFromAccessibility() }) { dashboardContent() }
                    if (state.voiceActive) {
                        // A full input shield, not just a translucent view: dashboard taps/gestures never reach WebView.
                        Box(Modifier.fillMaxSize().background(Ink.copy(alpha = .78f)).testTag("dashboard-shield")
                            .pointerInput(Unit) { awaitPointerEventScope { while (true) { awaitPointerEvent().changes.forEach { it.consume() } } } })
                    }
                }
                if (state.music.enabled) MusicControls(state.music, onMusicCommand, state.voiceActive)
                else Spacer(Modifier.height(12.dp))
            }
            if (state.voiceActive) Surface(Modifier.align(Alignment.BottomCenter).padding(bottom = if (state.music.enabled) 98.dp else 44.dp)
                .padding(horizontal = 20.dp).widthIn(max = 620.dp).fillMaxWidth(), shape = RoundedCornerShape(28.dp), color = Color(0xff152330)) {
                Column(Modifier.padding(24.dp), horizontalAlignment = Alignment.CenterHorizontally) {
                    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                        Text(when { state.alert -> "Alert"; state.phase == "listening" -> "Listening"; state.phase == "speaking" -> "Responding"; state.phase == "stopping" -> "Stopping"; else -> "Working" },
                            Modifier.weight(1f), fontSize = 21.sp, color = Color.White)
                        IconButton(onClick = onStop, modifier = Modifier.testTag("stop").semantics { contentDescription = "Stop voice turn" }) {
                            Canvas(Modifier.size(18.dp)) { drawRoundRect(Mint, cornerRadius = androidx.compose.ui.geometry.CornerRadius(3f, 3f)) }
                        }
                    }
                    HorizonBraid(if (state.microphoneVisible) state.waveform else emptyList(), Modifier.fillMaxWidth().height(94.dp))
                    Column(Modifier.heightIn(max = 180.dp).verticalScroll(rememberScrollState()).fillMaxWidth(), verticalArrangement = Arrangement.spacedBy(8.dp)) {
                        if (state.heard.isNotEmpty()) Text(state.heard, color = Color.White, fontSize = 19.sp)
                        if (state.reply.isNotEmpty()) Text(state.reply, color = Mint, fontSize = 18.sp)
                    }
                    if (!state.microphoneVisible) Text("Microphone visualization paused", color = Muted, fontSize = 11.sp)
                }
            }
            if (state.problem.isNotEmpty()) Surface(Modifier.align(Alignment.TopCenter).padding(top = 82.dp, start = 24.dp, end = 24.dp), shape = RoundedCornerShape(12.dp)) {
                Row(Modifier.padding(12.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text(state.problem, Modifier.weight(1f), color = Color(0xffffd0a4))
                    IconButton(onClick = onDismiss, Modifier.semantics { contentDescription = "Dismiss notice" }) { Text("×", fontSize = 24.sp) }
                }
            }
        }
        if (settings) SettingsDialog(state, noiseKey, musicToken, onMusicConnect, onMusicDisconnect, onUpdate) { settings = false }
    }
}

@Composable
fun HorizonBraid(samples: List<Float>, modifier: Modifier = Modifier) {
    Canvas(modifier.semantics { contentDescription = if (samples.isEmpty()) "Microphone waveform inactive" else "Live microphone waveform" }) {
        val colors = listOf(Mint, Color(0xff9eb9ff), Color(0xffd5b6e8))
        for (line in 0..2) {
            val path = Path()
            for (i in 0..128) {
                val x = i / 128f
                val at = x * (samples.size - 1).coerceAtLeast(0)
                val index = at.toInt()
                val sample = if (samples.isEmpty()) 0f else {
                    // Smooth actual PCM points. No elapsed-time oscillator or synthetic speech animation.
                    val a = samples[(index - 1).coerceAtLeast(0)]
                    val b = samples[index]
                    val c = samples[(index + 1).coerceAtMost(samples.lastIndex)]
                    (a + 2 * b + c) / 4
                }
                val envelope = sin(x * PI).toFloat()
                val braid = sin(x * PI * 4 + line * 2).toFloat()
                val y = size.height / 2 + sample * envelope * size.height * .32f * (1 - line * .17f) + braid * 3.dp.toPx()
                if (i == 0) path.moveTo(0f, y) else path.lineTo(x * size.width, y)
            }
            drawPath(path, colors[line].copy(alpha = .85f), style = Stroke(width = (1.7f - line * .3f).dp.toPx()))
        }
    }
}

@Composable private fun MusicControls(music: MusicState, command: (String) -> Unit, ducked: Boolean) {
    Surface(Modifier.padding(12.dp).fillMaxWidth(), shape = RoundedCornerShape(16.dp)) {
        Row(Modifier.padding(horizontal = 16.dp, vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
            Column(Modifier.weight(1f)) {
                Text(music.title.ifEmpty { "Sendspin · ${music.status}" }, maxLines = 1, color = Color.White)
                Text(listOf(music.artist, if (ducked) "Quiet for voice" else "").filter { it.isNotEmpty() }.joinToString(" · "), color = Muted, maxLines = 1, fontSize = 12.sp)
            }
            val action = if (music.playing) "pause" else "play"
            TextButton(onClick = { command(action) }, enabled = action in music.commands) { Text(if (music.playing) "Pause" else "Play") }
            TextButton(onClick = { command("next") }, enabled = "next" in music.commands) { Text("Next") }
        }
    }
}

internal fun trustedNavigation(origin: String, value: String): Boolean {
    val a = Uri.parse(origin)
    val b = Uri.parse(value)
    return b.scheme in listOf("https", "http") && a.scheme == b.scheme && a.encodedAuthority == b.encodedAuthority && b.userInfo == null
}

@SuppressLint("SetJavaScriptEnabled")
@Composable private fun Dashboard(state: EncoreState) {
    if (state.dashboard.isEmpty()) {
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
            view.importantForAccessibility = if (state.voiceActive) View.IMPORTANT_FOR_ACCESSIBILITY_NO_HIDE_DESCENDANTS else View.IMPORTANT_FOR_ACCESSIBILITY_AUTO
            // Also release keyboard focus: the touch shield alone cannot stop a focused dashboard input.
            if (state.voiceActive) view.clearFocus()
            view.isFocusable = !state.voiceActive
            view.isFocusableInTouchMode = !state.voiceActive
            val desired = "${state.reload}:${state.dashboard}"
            if (view.tag != desired) { view.tag = desired; view.loadUrl(state.dashboard) }
        }, modifier = Modifier.fillMaxSize().testTag("dashboard"))
    }
}

@Composable private fun SettingsDialog(state: EncoreState, noiseKey: () -> String,
    musicToken: () -> String, onMusicConnect: (String) -> Unit, onMusicDisconnect: () -> Unit, onUpdate: () -> Unit, close: () -> Unit) {
    var sendspin by remember { mutableStateOf("") }
    var secret by remember { mutableStateOf("") }
    AlertDialog(onDismissRequest = close, title = { Text("Satellite settings") }, confirmButton = { TextButton(onClick = close) { Text("Done") } },
        text = {
            Column(Modifier.verticalScroll(rememberScrollState()), verticalArrangement = Arrangement.spacedBy(12.dp)) {
                Text("${state.name} · Incubator only", color = Mint)
                Text("Incubator voice uses trusted-LAN TCP 6055. ESPHome dashboard controls use encrypted TCP 6053. No Assist or BLE proxy in this build.", fontSize = 12.sp)
                Text("Set one full Dashboard URL through ESPHome, such as https://home.example/lovelace/home. It supplies both the HA address and dashboard path; no local origin setup is needed. Sign in to HA in the dashboard itself. Navigation stays within the configured origin.", fontSize = 12.sp)
                TextButton(onClick = { secret = noiseKey() }) { Text("Reveal ESPHome encryption key") }
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
