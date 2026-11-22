package com.encore.satellite

import android.Manifest
import android.bluetooth.BluetoothDevice
import android.bluetooth.BluetoothManager
import android.bluetooth.le.*
import android.content.Context
import android.content.pm.PackageManager
import android.location.LocationManager
import android.os.Build
import android.os.PowerManager
import com.encore.engine.mobile.Bluetooth
import com.encore.engine.mobile.Runtime
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.withContext

data class BluetoothState(
    val supported: Boolean = false,
    val enabled: Boolean = false,
    val status: String = "Checking Bluetooth support",
    val detail: String = "",
    val permitted: Boolean = false,
    val received: Long = 0,
    val setupPending: Boolean = false,
    val radioOn: Boolean = false,
    val locationOn: Boolean = false,
    val canRetry: Boolean = false,
) {
    companion object {
        fun evaluate(sdk: Int, supported: Boolean, enabled: Boolean, permitted: Boolean,
            radio: Boolean, location: Boolean, visible: Boolean, running: Boolean,
            scanning: Boolean, received: Long, failure: String, setupPending: Boolean = false): BluetoothState {
            val status = when {
                !supported && sdk < 26 -> "Advertisement proxy unavailable on API $sdk"
                !supported -> "Unavailable on this device"
                !enabled -> "Disabled"
                !permitted -> "Permission needed"
                !radio -> "Bluetooth is off"
                !location -> "Location services needed for beacon coverage"
                setupPending -> "Finish Bluetooth setup to start scanning"
                !running -> "Satellite stopped"
                !visible -> "Paused in background"
                failure.isNotEmpty() -> failure
                !scanning -> "Waiting for encrypted controller"
                received == 0L -> "Scan requested; awaiting advertisements"
                else -> "Receiving advertisements"
            }
            return BluetoothState(supported, enabled, status,
                "Scan-only raw advertisement proxy. " + (if (sdk < 35) "Address type is explicitly unknown on this Android version. " else "Public APIs supply address type when available. ") +
                "GATT connections, remote caching and pairing are unavailable. Background scanning is paused.",
                permitted, received, setupPending, radio, location, failure.isNotEmpty())
        }
    }
}

/** Android owns permission/radio/activity state; Go owns saved intent and the
 * encrypted radio lease. No reflection, private parcel decoding or radio toggles. */
class BluetoothHost(private val context: Context) : Bluetooth {
    private val adapter get() = context.getSystemService(BluetoothManager::class.java)?.adapter
    private val scanHost: Bluetooth? = if (Build.VERSION.SDK_INT >= 26 &&
        context.packageManager.hasSystemFeature(PackageManager.FEATURE_BLUETOOTH_LE) &&
        runCatching { adapter != null }.getOrDefault(false)) this else null
    val supported: Boolean get() = scanHost != null
    private var engine: Runtime? = null
    private val viewers = mutableSetOf<Any>()
    private var running = false
    private var allowed = false
    private var failure = ""
    private var generation = 0L
    private var callback: ScanCallback? = null
    private var scanner: BluetoothLeScanner? = null
    private var retired: ScanCallback? = null
    private var received = 0L
    private val mutable = MutableStateFlow(BluetoothState())
    val state = mutable.asStateFlow()

    fun attach(runtime: Runtime) {
        runtime.configureBluetooth(scanHost)
        engine = runtime
        refresh()
    }

    fun permissions(): Array<String> = when {
        scanHost == null -> emptyArray()
        Build.VERSION.SDK_INT >= 31 -> arrayOf(Manifest.permission.BLUETOOTH_SCAN, Manifest.permission.BLUETOOTH_CONNECT,
            Manifest.permission.ACCESS_COARSE_LOCATION, Manifest.permission.ACCESS_FINE_LOCATION)
        else -> arrayOf(Manifest.permission.ACCESS_FINE_LOCATION)
    }

    private fun permitted() = permissions().isNotEmpty() && permissions().all {
        context.checkSelfPermission(it) == PackageManager.PERMISSION_GRANTED
    }

    fun visible(owner: Any, visible: Boolean) {
        synchronized(this) { if (visible) viewers.add(owner) else viewers.remove(owner) }
        refresh()
    }

    fun running(value: Boolean) { synchronized(this) { running = value }; refresh() }
    fun retry() { synchronized(this) { failure = "" }; refresh() }

    suspend fun approveSetup() {
        refresh()
        val current = state.value
        check(current.supported && current.enabled && current.permitted && current.radioOn && current.locationOn) {
            "Bluetooth or Location access changed. Check the settings and try again."
        }
        val runtime = checkNotNull(engine)
        withContext(Dispatchers.IO) { runtime.approveBluetoothSetup() }
        refresh()
    }

    // Called on the service's main thread and on Activity resume, including
    // return from Settings. Revocation can also kill the process; restart stays
    // paused until a visible activity and all grants are present again.
    fun refresh() {
        val enabled = engine?.bluetoothEnabled() ?: false
        val setupPending = engine?.bluetoothSetupPending() ?: false
        val permission = permitted()
        val radio = permission && runCatching { adapter?.isEnabled == true }.getOrDefault(false)
        val location = runCatching {
            val manager = context.getSystemService(LocationManager::class.java)
            if (Build.VERSION.SDK_INT >= 28) manager.isLocationEnabled
            else manager.isProviderEnabled(LocationManager.GPS_PROVIDER) || manager.isProviderEnabled(LocationManager.NETWORK_PROVIDER)
        }.getOrDefault(false)
        val pause: Boolean
        synchronized(this) {
            if (permission && retired != null) runCatching { stopScan(generation) }
            val visible = viewers.isNotEmpty() && context.getSystemService(PowerManager::class.java).isInteractive
            val next = BluetoothState.evaluate(Build.VERSION.SDK_INT, supported, enabled, permission,
                radio, location, visible, running, callback != null, received, failure, setupPending)
            pause = !supported || !enabled || setupPending || !permission || !radio || !location || !visible || !running || failure.isNotEmpty()
            if (!radio || !permission || !enabled) failure = ""
            mutable.value = next
            if (allowed == !pause) return
            allowed = !pause
        }
        engine?.pauseBluetooth(pause)
    }

    @Synchronized override fun startScan(generation: Long) {
        check(allowed && permitted()) { "BLE scan not available" }
        check(callback == null && retired == null) { "BLE cleanup must finish before another scan" }
        val listener = object : ScanCallback() {
            override fun onScanResult(callbackType: Int, result: ScanResult) {
                val data = result.scanRecord?.bytes?.copyOf() ?: return
                synchronized(this@BluetoothHost) {
                    if (callback !== this || this@BluetoothHost.generation != generation || !allowed) return
                }
                // Unknown metadata is explicit (255), never guessed as public
                // or random. The shared proxy only permits it without GATT.
                runCatching {
                    if (!result.isLegacy || data.isEmpty() || data.size > 62) return
                    val type = if (Build.VERSION.SDK_INT >= 35) when (result.device.addressType) {
                        BluetoothDevice.ADDRESS_TYPE_PUBLIC -> 0
                        BluetoothDevice.ADDRESS_TYPE_RANDOM -> 1
                        BluetoothDevice.ADDRESS_TYPE_UNKNOWN -> 255
                        else -> return // Anonymous is not unknown; it has no usable MAC.
                    } else 255
                    if (result.rssi !in -127..20) return
                    engine?.bluetoothAdvertisement(generation, result.device.address, type.toLong(), result.rssi.toLong(), data)
                    synchronized(this@BluetoothHost) { received++ }
                }.onFailure { fail(generation, "Bluetooth permission or radio lost") }
            }
            override fun onBatchScanResults(results: MutableList<ScanResult>) { results.forEach { onScanResult(0, it) } }
            override fun onScanFailed(errorCode: Int) { fail(generation, "Scan failed ($errorCode); retry from Bluetooth settings") }
        }
        this.generation = generation
        callback = listener
        received = 0
        try {
            val native = checkNotNull(adapter?.bluetoothLeScanner) { "Bluetooth radio unavailable" }
            scanner = native
            native.startScan(emptyList(), ScanSettings.Builder().setLegacy(true)
                .setScanMode(ScanSettings.SCAN_MODE_LOW_POWER).setReportDelay(0).build(), listener)
        } catch (error: SecurityException) {
            failure = "Bluetooth permission lost; restore access and retry"
            throw error
        } catch (error: Exception) {
            failure = "Bluetooth scan could not start; check permissions and retry"
            throw error
        }
    }

    private fun fail(token: Long, message: String) {
        synchronized(this) {
            if (callback == null || token != generation) return
            failure = message
            allowed = false
        }
        engine?.pauseBluetooth(true)
        engine?.bluetoothScanFailed(token)
    }

    @Synchronized override fun stopScan(generation: Long) {
        if (generation != this.generation) return
        val previous = retired ?: callback
        callback = null // Fence before Android IPC; revoked grants can deny stop.
        retired = previous
        if (previous != null) {
            try { scanner?.stopScan(previous) }
            catch (error: SecurityException) {
                failure = "Scan cleanup pending; restore Bluetooth permissions and retry"
                throw error
            }
            catch (error: Exception) {
                failure = "Scan cleanup pending; restore permissions/radio and retry"
                throw error
            }
        }
        retired = null
        scanner = null
    }
}
