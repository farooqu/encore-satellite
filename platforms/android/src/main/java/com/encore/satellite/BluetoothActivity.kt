package com.encore.satellite

import android.content.*
import android.content.pm.PackageManager
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
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.semantics.LiveRegionMode
import androidx.compose.ui.semantics.heading
import androidx.compose.ui.semantics.liveRegion
import androidx.compose.ui.semantics.semantics
import androidx.compose.ui.unit.dp
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.lifecycleScope
import kotlinx.coroutines.CancellationException
import kotlinx.coroutines.launch

class BluetoothActivity : ComponentActivity() {
    private var service by mutableStateOf<EncoreService?>(null)
    private var problem by mutableStateOf("")
    private val setupPreferences by lazy { getSharedPreferences("setup", MODE_PRIVATE) }
    private var permissionsNeedSettings by mutableStateOf(false)
    private val permissions = registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { results ->
        val denied = setupPreferences.getStringSet("bluetooth_denied", emptySet()).orEmpty()
        if (results.isNotEmpty()) setupPreferences.edit().putStringSet("bluetooth_denied",
            (denied - results.keys) + results.filterValues { !it }.keys).apply()
        service?.bluetooth?.let { host ->
            refreshPermissionRecovery(host)
            host.refresh()
            val state = host.state.value
            if (state.setupPending) {
                problem = "" // The observable Android grant state supplies the next step.
            } else if (state.permitted) {
                if (!state.enabled) enable()
            } else if (!state.enabled) problem = "Bluetooth remains off. Grant the requested Bluetooth/location access for beacon coverage, or leave the proxy disabled."
        }
    }
    private val connection = object : ServiceConnection {
        override fun onServiceConnected(name: ComponentName, binder: IBinder) {
            service = (binder as EncoreService.LocalBinder).service
            service?.bluetooth?.let { host ->
                refreshPermissionRecovery(host)
                host.visible(this@BluetoothActivity, lifecycle.currentState.isAtLeast(Lifecycle.State.RESUMED))
            }
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
            val openPermissions = { openSettings(Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.parse("package:$packageName"))) }
            if (state.supported && state.setupPending) {
                BluetoothFirstRun(state, problem.ifEmpty { serviceProblem },
                    onRequest = { requestPermissions(checkNotNull(host)) },
                    onPermissionRecovery = openPermissions.takeIf { permissionsNeedSettings },
                    onRadio = { openSettings(Intent(Settings.ACTION_BLUETOOTH_SETTINGS)) },
                    onLocation = { openSettings(Intent(Settings.ACTION_LOCATION_SOURCE_SETTINGS)) },
                    onApprove = { lifecycleScope.launch {
                        try {
                            checkNotNull(host).approveSetup()
                            finish()
                        } catch (cancelled: CancellationException) {
                            throw cancelled
                        } catch (failure: Exception) {
                            problem = failure.message ?: "Could not save Bluetooth setup"
                        }
                    } },
                    onEnabled = { owner?.setBluetoothEnabled(it); problem = "" }, onBack = { finish() })
                return@setContent
            }
            BluetoothSettings(state, problem.ifEmpty { serviceProblem },
                onEnabled = { if (it) consent = true else runCatching { service?.setBluetoothEnabled(false) }.onFailure { problem = "Could not save Bluetooth setting" } },
                onRetry = { host?.retry() },
                onRequest = { host?.let(::requestPermissions) },
                onPermissionRecovery = openPermissions.takeIf { permissionsNeedSettings },
                onRadio = { openSettings(Intent(Settings.ACTION_BLUETOOTH_SETTINGS)) },
                onLocation = { openSettings(Intent(Settings.ACTION_LOCATION_SOURCE_SETTINGS)) }, onBack = { finish() })
            if (consent) AlertDialog(onDismissRequest = { consent = false }, title = { Text("Enable Bluetooth proxy?") },
                text = { Text(BluetoothDisclosure) },
                confirmButton = { TextButton(onClick = {
                    consent = false
                    val grants: Array<String> = host?.permissions() ?: emptyArray()
                    if (grants.isNotEmpty()) {
                        if (state.permitted) enable() else if (permissionsNeedSettings) openPermissions()
                        else requestPermissions(checkNotNull(host))
                    }
                }) { Text(if (permissionsNeedSettings) "Open app permissions" else "Continue") } },
                dismissButton = { TextButton(onClick = { consent = false }) { Text("Cancel") } })
        }
    }
    private fun refreshPermissionRecovery(host: BluetoothHost) {
        val denied = setupPreferences.getStringSet("bluetooth_denied", emptySet()).orEmpty()
        val missing = host.permissions().filter { checkSelfPermission(it) != PackageManager.PERMISSION_GRANTED }
        // Clear grants from denial history so future revocations can use the normal prompt again.
        val stillDenied = denied.intersect(missing.toSet())
        if (stillDenied != denied) setupPreferences.edit().putStringSet("bluetooth_denied", stillDenied).apply()
        permissionsNeedSettings = stillDenied.any { !shouldShowRequestPermissionRationale(it) }
    }
    private fun requestPermissions(host: BluetoothHost) {
        val grants = host.permissions()
        if (grants.isEmpty()) return
        problem = ""
        runCatching { permissions.launch(grants) }.onFailure {
            permissionsNeedSettings = true
            problem = "Permission request unavailable on this Android build; Encore is not scanning."
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
    override fun onResume() {
        super.onResume()
        service?.bluetooth?.let { host -> refreshPermissionRecovery(host); host.visible(this, true) }
    }
    override fun onPause() { service?.bluetooth?.visible(this, false); super.onPause() }
    override fun onDestroy() { unbindService(connection); super.onDestroy() }
}

private const val BluetoothDisclosure = "Encore forwards nearby Bluetooth addresses, signal strength and raw beacon data to your paired, encrypted Home Assistant ESPHome controller while Encore is visible. This may reveal nearby devices or presence. Encore saves no beacon history. The proxy is scan-only; Bluetooth device pairing and GATT connections are unavailable."

private val bluetoothPermissionAction: String get() = if (android.os.Build.VERSION.SDK_INT >= 31)
    "Allow Bluetooth and location access" else "Allow location access"

@Composable internal fun BluetoothFirstRun(state: BluetoothState, problem: String, onRequest: () -> Unit,
    onPermissionRecovery: (() -> Unit)?, onRadio: () -> Unit, onLocation: () -> Unit, onApprove: () -> Unit,
    onEnabled: (Boolean) -> Unit, onBack: () -> Unit) {
    MaterialTheme(colorScheme = darkColorScheme()) {
        Surface(Modifier.fillMaxSize()) {
            Box(Modifier.fillMaxSize().systemBarsPadding(), contentAlignment = Alignment.Center) {
                Column(Modifier.widthIn(max = 600.dp).fillMaxWidth().verticalScroll(rememberScrollState()).padding(24.dp),
                    verticalArrangement = Arrangement.spacedBy(12.dp)) {
                    Text("Bluetooth proxy setup", style = MaterialTheme.typography.headlineMedium,
                        modifier = Modifier.semantics { heading() })
                    Text(if (state.enabled) "On · ${state.status}" else "Off · Not scanning",
                        modifier = Modifier.semantics { liveRegion = LiveRegionMode.Polite })
                    Text(BluetoothDisclosure)
                    if (!state.enabled) {
                        Button(onClick = { onEnabled(true) }) { Text("Turn on proxy") }
                    } else if (!state.permitted) {
                        val nearby = android.os.Build.VERSION.SDK_INT >= 31
                        Text(if (nearby) "Allow Nearby Devices and precise Location to receive beacons. Approximate Location is not enough."
                            else "Allow Location to receive beacons.")
                        if (onPermissionRecovery == null) {
                            Button(onClick = onRequest) { Text(bluetoothPermissionAction) }
                        } else {
                            Text("In Android settings, open Permissions, grant the access above, then return to Encore.")
                            Button(onClick = onPermissionRecovery) { Text("Open app permissions") }
                        }
                    } else if (!state.radioOn) {
                        Button(onClick = onRadio) { Text("Turn on Bluetooth in Android") }
                    } else if (!state.locationOn) {
                        Button(onClick = onLocation) { Text("Turn on Location services in Android") }
                    } else {
                        Button(onClick = onApprove) { Text("Allow scanning") }
                    }
                    if (problem.isNotEmpty()) Text(problem, color = MaterialTheme.colorScheme.error)
                    TextButton(onClick = onBack) { Text(if (state.enabled) "Not now" else "Done") }
                    if (state.enabled) TextButton(onClick = { onEnabled(false) }) { Text("Turn off proxy") }
                }
            }
        }
    }
}

@Composable internal fun BluetoothSettings(state: BluetoothState, problem: String = "", onEnabled: (Boolean) -> Unit,
    onRetry: () -> Unit, onRequest: () -> Unit, onPermissionRecovery: (() -> Unit)?, onRadio: () -> Unit,
    onLocation: () -> Unit, onBack: () -> Unit) {
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
                Text("Independent of microphone mute and Sendspin. Your choice is saved across restarts. This switch does not grant Android permissions or start scanning in the background.")
                Text("GATT connections: unavailable · Remote cache: unavailable · Pairing: unavailable")
                if (state.supported) {
                    Text("Valid advertisements received this scan: ${state.received}. Scan registration alone does not prove RF reception or Home Assistant delivery.")
                    if (state.enabled) when {
                        !state.permitted -> TextButton(onClick = onPermissionRecovery ?: onRequest) {
                            Text(if (onPermissionRecovery != null) "Open app permissions" else bluetoothPermissionAction)
                        }
                        !state.radioOn -> TextButton(onClick = onRadio) { Text("Turn on Bluetooth in Android") }
                        !state.locationOn -> TextButton(onClick = onLocation) { Text("Turn on Location services in Android") }
                        state.canRetry -> TextButton(onClick = onRetry) { Text("Retry scan") }
                    }
                }
                if (problem.isNotEmpty()) Text(problem, color = MaterialTheme.colorScheme.error)
                TextButton(onClick = onBack) { Text("Back") }
            }
        }
    }
}
