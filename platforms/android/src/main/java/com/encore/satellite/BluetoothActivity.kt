package com.encore.satellite

import android.content.*
import android.net.Uri
import android.os.Bundle
import android.os.IBinder
import android.provider.Settings
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.collectAsStateWithLifecycle

class BluetoothActivity : ComponentActivity() {
    private var service by mutableStateOf<EncoreService?>(null)
    private var problem by mutableStateOf("")
    private val permissions = registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) {
        service?.bluetooth?.refresh()
        if (service?.bluetooth?.state?.value?.permitted == true) enable()
        else problem = "Bluetooth remains off. Grant the requested Bluetooth/location access for beacon coverage, or leave the proxy disabled."
    }
    private val connection = object : ServiceConnection {
        override fun onServiceConnected(name: ComponentName, binder: IBinder) {
            service = (binder as EncoreService.LocalBinder).service
            service?.bluetooth?.visible(this@BluetoothActivity, lifecycle.currentState.isAtLeast(Lifecycle.State.RESUMED))
        }
        override fun onServiceDisconnected(name: ComponentName) { service = null }
    }
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        bindService(Intent(this, EncoreService::class.java), connection, BIND_AUTO_CREATE)
        setContent {
            val host = service?.bluetooth
            val state = if (host != null) host.state.collectAsStateWithLifecycle().value else BluetoothState()
            val owner = service
            val serviceProblem = if (owner != null) owner.state.collectAsStateWithLifecycle().value.problem else ""
            var consent by remember { mutableStateOf(false) }
            BluetoothSettings(state, problem.ifEmpty { serviceProblem },
                onEnabled = { if (it) consent = true else runCatching { service?.setBluetoothEnabled(false) }.onFailure { problem = "Could not save Bluetooth setting" } },
                onRetry = { host?.retry() },
                onPermissions = { openSettings(Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.parse("package:$packageName"))) },
                onRadio = { openSettings(Intent(Settings.ACTION_BLUETOOTH_SETTINGS)) },
                onLocation = { openSettings(Intent(Settings.ACTION_LOCATION_SOURCE_SETTINGS)) }, onBack = { finish() })
            if (consent) AlertDialog(onDismissRequest = { consent = false }, title = { Text("Enable Bluetooth proxy?") },
                text = { Text("Nearby Bluetooth addresses, signal strength and raw beacon data will be forwarded to your paired encrypted ESPHome controller while Encore is visible. Location access preserves beacon coverage; data may reveal nearby devices or presence. No beacon history is saved by Encore. This does not enable the microphone or music. GATT and pairing are unavailable.") },
                confirmButton = { TextButton(onClick = {
                    consent = false
                    val grants: Array<String> = host?.permissions() ?: emptyArray()
                    if (grants.isNotEmpty()) {
                        if (state.permitted) enable() else runCatching { permissions.launch(grants) }
                            .onFailure { problem = "Permission request unavailable on this Android build; proxy remains off" }
                    }
                }) { Text("Continue") } }, dismissButton = { TextButton(onClick = { consent = false }) { Text("Cancel") } })
        }
    }
    private fun openSettings(intent: Intent) {
        runCatching { startActivity(intent) }
            .onFailure { problem = "Requested settings screen unavailable on this Android build" }
    }
    private fun enable() {
        runCatching {
            startForegroundService(Intent(this, EncoreService::class.java).setAction("bluetooth"))
        }.onFailure { problem = "Could not start Bluetooth service; reopen Encore and retry" }
    }
    override fun onResume() { super.onResume(); service?.bluetooth?.visible(this, true) }
    override fun onPause() { service?.bluetooth?.visible(this, false); super.onPause() }
    override fun onDestroy() { unbindService(connection); super.onDestroy() }
}

@Composable fun BluetoothSettings(state: BluetoothState, problem: String = "", onEnabled: (Boolean) -> Unit,
    onRetry: () -> Unit, onPermissions: () -> Unit, onRadio: () -> Unit, onLocation: () -> Unit, onBack: () -> Unit) {
    MaterialTheme(colorScheme = darkColorScheme()) {
        Surface(Modifier.fillMaxSize()) {
            Column(Modifier.systemBarsPadding().verticalScroll(rememberScrollState()).padding(24.dp),
                verticalArrangement = Arrangement.spacedBy(16.dp)) {
                Text("Bluetooth proxy", style = MaterialTheme.typography.headlineMedium)
                Text(state.status, style = MaterialTheme.typography.titleMedium)
                Text(state.detail)
                Row(horizontalArrangement = Arrangement.spacedBy(16.dp)) {
                    Text("Enable proxy", Modifier.weight(1f))
                    Switch(checked = state.enabled, onCheckedChange = onEnabled, enabled = state.supported || state.enabled)
                }
                Text("Independent of microphone mute and Sendspin. Default is off. Enabling preserves your choice across restarts, but does not grant permissions or start scanning in the background.")
                Text("GATT connections: unavailable · Remote cache: unavailable · Pairing: unavailable")
                if (state.supported) {
                    Text("Valid advertisements received this scan: ${state.received}. Scan registration alone does not prove RF reception or Home Assistant delivery.")
                    TextButton(onClick = onPermissions) { Text("App permissions") }
                    TextButton(onClick = onRadio) { Text("Bluetooth radio settings") }
                    TextButton(onClick = onLocation) { Text("Location services") }
                    TextButton(onClick = onRetry) { Text("Retry scan") }
                }
                if (problem.isNotEmpty()) Text(problem, color = MaterialTheme.colorScheme.error)
                TextButton(onClick = onBack) { Text("Back") }
            }
        }
    }
}
