package com.encore.satellite

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.dp

internal enum class SetupStage { NEW, CORE, MUSIC, BLUETOOTH, EXTRAS, DONE }
internal enum class SetupStep { MICROPHONE, NOTIFICATIONS, PAIR, VOICE, MUSIC, BLUETOOTH, EXTRAS }

internal data class AndroidSetupStatus(val notifications: Boolean = false, val home: Boolean? = null, val remoteInstalls: String? = null)

// Readiness is not a turn phase. Keep live turn labels in the existing voice dock.
internal val EncoreState.voiceReadiness: String get() = when {
    !running -> "Satellite stopped"
    !voiceKnown -> "Checking voice readiness…"
    !connected -> "Voice Incubator not connected"
    muted -> "Microphone muted"
    !microphoneReady -> "Microphone off"
    !captureReady -> captureProblem
    inhibited -> "Voice not ready"
    wakeReady -> "Wake ready"
    talkAllowed && talkReady -> "Connected · wake not ready"
    else -> "Voice not ready"
}

internal fun setupStep(stage: SetupStage, state: EncoreState, microphone: Boolean, offerNotifications: Boolean,
    bluetoothPending: Boolean?): SetupStep? {
    if (stage == SetupStage.DONE || (stage == SetupStage.NEW && !state.needsHomeAssistantSetup)) return null
    return when {
        !microphone && (stage == SetupStage.CORE || state.needsHomeAssistantSetup) -> SetupStep.MICROPHONE
        stage == SetupStage.CORE || state.needsHomeAssistantSetup -> when {
            state.running && state.esphome == "paired" -> SetupStep.VOICE
            offerNotifications && state.esphome != "waiting" && state.esphome != "expired" -> SetupStep.NOTIFICATIONS
            else -> SetupStep.PAIR
        }
        stage == SetupStage.MUSIC -> SetupStep.MUSIC
        stage == SetupStage.BLUETOOTH && bluetoothPending != false -> SetupStep.BLUETOOTH
        else -> SetupStep.EXTRAS
    }
}

@Composable internal fun SatelliteSetup(step: SetupStep, state: EncoreState, android: AndroidSetupStatus,
    onMicrophone: () -> Unit, onAppPermissions: (() -> Unit)?, onNotifications: () -> Unit, onSkipNotifications: () -> Unit,
    onPair: () -> Unit, onVoiceContinue: () -> Unit, onMusicEnable: () -> Unit, onMusicDisable: () -> Unit, onMusicContinue: () -> Unit,
    onBluetooth: () -> Unit, onExtras: () -> Unit,
    onChooseHome: () -> Unit, onRemoteInstalls: () -> Unit, onDone: () -> Unit) {
    BackHandler(enabled = step == SetupStep.EXTRAS, onBack = onDone)
    BoxWithConstraints(Modifier.fillMaxSize().systemBarsPadding(), contentAlignment = Alignment.Center) {
        val landscape = maxWidth > maxHeight
        Column(Modifier.padding(end = if (landscape) 76.dp else 0.dp, bottom = if (landscape) 0.dp else 76.dp)
            .widthIn(max = 600.dp).fillMaxWidth().verticalScroll(rememberScrollState()).padding(24.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp)) {
            val title = when (step) {
                SetupStep.MICROPHONE -> "Set up Encore"
                SetupStep.NOTIFICATIONS -> "Notifications (optional)"
                SetupStep.PAIR -> "Home Assistant setup"
                SetupStep.VOICE -> "Connect voice"
                SetupStep.MUSIC -> "Music (optional)"
                SetupStep.BLUETOOTH -> "Bluetooth proxy"
                SetupStep.EXTRAS -> "Finish setup"
            }
            Text(title, style = MaterialTheme.typography.headlineMedium, color = MaterialTheme.colorScheme.onSurface,
                modifier = Modifier.semantics { heading() })
            when (step) {
                SetupStep.MICROPHONE -> {
                    SetupText("Allow microphone access to use voice and local wake-word detection.")
                    if (onAppPermissions == null) {
                        Button(onClick = onMicrophone) { Text("Allow microphone access") }
                    } else {
                        SetupText("In Android settings, choose Permissions → Microphone, then allow access and return to Encore.")
                        Button(onClick = onAppPermissions) { Text("Open app permissions") }
                    }
                }
                SetupStep.NOTIFICATIONS -> {
                    SetupText("Encore can show satellite controls and let you know when an update is ready. Notifications aren't required for voice. You can manage them later in Encore Settings.")
                    Button(onClick = onNotifications) { Text("Allow notifications") }
                    TextButton(onClick = onSkipNotifications) { Text("Not now") }
                }
                SetupStep.PAIR -> {
                    val description = when {
                        !state.running -> if (state.problem.isEmpty()) "Starting Encore…" else "Encore couldn't start. Try again."
                        state.esphome == "waiting" -> "In Home Assistant, go to Settings → Devices & services and add ${state.espHomeFriendlyName} under Discovered. No key entry is needed. The pairing window closes after two minutes."
                        state.esphome == "expired" -> "The pairing window expired. Start pairing again, then add the satellite in Home Assistant."
                        else -> "Pair on a trusted local network, then add this satellite in Home Assistant under Settings → Devices & services."
                    }
                    SetupText(description, Modifier.semantics { liveRegion = LiveRegionMode.Polite })
                    if (state.running && state.esphome in setOf("unpaired", "expired")) {
                        Button(onClick = onPair) { Text("Pair with Home Assistant") }
                    } else if (!state.running && state.problem.isNotEmpty()) {
                        Button(onClick = onMicrophone) { Text("Retry start") }
                    }
                }
                SetupStep.VOICE -> {
                    SetupText("Home Assistant is paired. Voice Incubator connects separately. On your trusted local network, select ${state.name} in Voice Incubator, or use the tablet’s IP address and port 6055.")
                    SetupText(state.voiceReadiness, Modifier.semantics { liveRegion = LiveRegionMode.Polite })
                    SetupText("When voice is ready, use the assistant button to test a turn. You can continue setup before voice connects.")
                    Button(onClick = onVoiceContinue) { Text("Continue setup") }
                }
                SetupStep.MUSIC -> {
                    SetupText("Use this tablet as a Music Assistant player with Sendspin. When Music Assistant is connected to Home Assistant and supports automatic pairing, there’s no token to enter.")
                    SetupText(if (state.music.enabled) "Music is on. You can continue without waiting for it to connect."
                        else "Music is off. You can turn it on here or later in Settings.")
                    Button(onClick = onMusicContinue) { Text("Continue setup") }
                    if (state.music.enabled) {
                        TextButton(onClick = onMusicDisable) { Text("Turn off music") }
                    } else {
                        TextButton(onClick = onMusicEnable, enabled = state.running && state.esphome == "paired") { Text("Enable music") }
                    }
                    if (state.musicProblem.isNotEmpty()) {
                        SetupText(state.musicProblem, Modifier.semantics { liveRegion = LiveRegionMode.Polite })
                    }
                }
                SetupStep.BLUETOOTH -> {
                    SetupText("This satellite can relay nearby Bluetooth advertisements to Home Assistant. The proxy is On for this fresh install, but it won't scan until you allow access and scanning.")
                    Button(onClick = onBluetooth) { Text("Set up Bluetooth proxy") }
                    TextButton(onClick = onExtras) { Text("Not now") }
                }
                SetupStep.EXTRAS -> {
                    SetupText("All of these are optional. Open Setup from Encore Settings to revisit them.")
                    HorizontalDivider()
                    SetupHeading("Dashboards")
                    SetupText("Encore shows a clock when no dashboard is selected. After setup, open Settings → Dashboards to choose views by name.")
                    HorizontalDivider()
                    SetupHeading("Android Home")
                    SetupText(when (android.home) {
                        true -> "Encore is your Home app. You can switch back to your previous launcher in Android."
                        false -> "Keep your launcher, or choose Encore Incubator to open it when you press Home. This is not kiosk mode."
                        null -> "Android hasn’t reported a Home app. You can check or change it in Android Settings. This is not kiosk mode."
                    })
                    TextButton(onClick = onChooseHome) { Text("Choose Home app") }
                    HorizontalDivider()
                    SetupHeading("Advanced · Remote installs")
                    SetupText(if (android.remoteInstalls == null) "This Android version uses local install review. Checking for updates never installs them."
                        else "${android.remoteInstalls}. An explicit Install request in Home Assistant may install a verified update if Android allows it. Checking for updates never installs them.")
                    if (android.remoteInstalls != null) TextButton(onClick = onRemoteInstalls) { Text("Remote install settings") }
                    Button(onClick = onDone) { Text("Done") }
                }
            }
        }
    }
}

@Composable private fun SetupText(text: String, modifier: Modifier = Modifier) {
    Text(text, modifier = modifier, color = MaterialTheme.colorScheme.onSurface)
}

@Composable private fun SetupHeading(text: String) {
    Text(text, style = MaterialTheme.typography.titleMedium, color = MaterialTheme.colorScheme.onSurface,
        modifier = Modifier.semantics { heading() })
}
