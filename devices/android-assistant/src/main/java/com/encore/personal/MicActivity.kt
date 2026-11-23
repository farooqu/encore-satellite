package com.encore.personal

import android.Manifest
import android.app.Activity
import android.app.AlertDialog
import android.app.role.RoleManager
import android.content.ComponentName
import android.content.Intent
import android.content.ServiceConnection
import android.content.pm.PackageManager
import android.net.ConnectivityManager
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.provider.Settings
import android.view.View
import android.view.WindowInsetsController
import android.view.WindowManager
import com.encore.engine.mobile.Mobile
import com.encore.satellite.AudioIo
import java.net.InetSocketAddress
import java.net.ServerSocket

class MicActivity : Activity() {
    private val handler = Handler(Looper.getMainLooper())
    private val settings by lazy { getSharedPreferences(MicService.PREFERENCES, MODE_PRIVATE) }
    private var service: MicService? = null
    private var bound = false
    private var localProblem = ""
    private var pairing: ServerSocket? = null
    private var pairingRegistration: NsdManager.RegistrationListener? = null
    private var setupDialog: AlertDialog? = null
    private lateinit var screen: MicScreen

    private val connection = object : ServiceConnection {
        override fun onServiceConnected(name: ComponentName, binder: IBinder) {
            service = (binder as MicService.LocalBinder).service
            render(service!!.state)
        }
        override fun onServiceDisconnected(name: ComponentName) { service = null }
    }
    private val poll = object : Runnable {
        override fun run() {
            render(service?.state ?: MicState())
            handler.postDelayed(this, 150)
        }
    }

    @Suppress("DEPRECATION") // System bars before the edge-to-edge APIs.
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.addFlags(WindowManager.LayoutParams.FLAG_SECURE)
        screen = MicScreen(this,
            onDiscover = {
                if (pairing == null) confirmDiscovery() else stopDiscovery()
                render(service?.state ?: MicState())
            },
            onWakeWord = { enabled ->
                if (enabled) requestVoice(MicService.WAKE_WORD) else service?.setWakeWord(false)
            },
            onTalk = {
                if (service?.state?.let { it.active || it.pendingTalk } == true) service?.stopTurn()
                else requestVoice(MicService.TALK)
                render(service?.state ?: MicState())
            },
            onChooseAssistant = ::chooseAssistant,
        )
        screen.serverAddress = settings.getString(MicService.SERVER_IP, "").orEmpty()
        setContentView(screen)
        window.decorView.setBackgroundColor(screen.surface)
        if (Build.VERSION.SDK_INT < 35) {
            window.statusBarColor = screen.surface
            window.navigationBarColor = screen.surface
        }
        if (Build.VERSION.SDK_INT >= 30) {
            val mask = WindowInsetsController.APPEARANCE_LIGHT_STATUS_BARS or WindowInsetsController.APPEARANCE_LIGHT_NAVIGATION_BARS
            window.insetsController?.setSystemBarsAppearance(if (screen.dark) 0 else mask, mask)
        } else {
            window.decorView.systemUiVisibility = if (screen.dark) 0 else
                View.SYSTEM_UI_FLAG_LIGHT_STATUS_BAR or View.SYSTEM_UI_FLAG_LIGHT_NAVIGATION_BAR
        }
        render(MicState())
    }

    override fun onStart() {
        super.onStart()
        bound = bindService(Intent(this, MicService::class.java), connection, BIND_AUTO_CREATE)
        handler.post(poll)
    }

    override fun onResume() {
        super.onResume()
        if (Build.VERSION.SDK_INT >= 31) screen.updateAssistantRole(
            getSystemService(RoleManager::class.java).isRoleHeld(RoleManager.ROLE_ASSISTANT))
    }

    override fun onStop() {
        handler.removeCallbacks(poll)
        setupDialog?.dismiss()
        setupDialog = null
        if (pairing != null) stopDiscovery()
        if (bound) unbindService(connection)
        bound = false
        service = null
        super.onStop()
    }

    private fun chooseAssistant() {
        if (Build.VERSION.SDK_INT >= 29) {
            val roles = getSystemService(RoleManager::class.java)
            if (roles.isRoleAvailable(RoleManager.ROLE_ASSISTANT) && !roles.isRoleHeld(RoleManager.ROLE_ASSISTANT)) {
                @Suppress("DEPRECATION")
                startActivityForResult(roles.createRequestRoleIntent(RoleManager.ROLE_ASSISTANT), 2)
                return
            }
        }
        startActivity(Intent(Settings.ACTION_MANAGE_DEFAULT_APPS_SETTINGS))
    }

    private fun confirmDiscovery() {
        setupDialog = AlertDialog.Builder(this).setTitle("On trusted home Wi-Fi?")
            .setMessage("Incubator connects to this phone to reveal its address. Keep this screen open. The mic stays off during setup.")
            .setNegativeButton("Cancel", null)
            .setPositiveButton("Discover") { _, _ -> startDiscovery() }.show()
    }

    private fun startDiscovery() {
        if (MicService.armedMic != null) return
        val connectivity = getSystemService(ConnectivityManager::class.java)
        val (network, address) = connectivity.wifiAddress() ?: run {
            localProblem = "Join trusted home Wi-Fi to discover Incubator, or enter its address."
            return
        }
        val socket = ServerSocket()
        pairing = socket
        try {
            socket.reuseAddress = true
            socket.bind(InetSocketAddress(address, Mobile.Port.toInt()))
            socket.soTimeout = 30_000
            val engine = Mobile.open(noBackupFilesDir.resolve("voice").absolutePath, AudioIo(this, voiceAec = false))
            val info = try { engine.incubatorService(network) } finally { engine.close() }
            val listener = object : NsdManager.RegistrationListener {
                override fun onServiceRegistered(info: NsdServiceInfo) {}
                override fun onServiceUnregistered(info: NsdServiceInfo) {}
                override fun onUnregistrationFailed(info: NsdServiceInfo, code: Int) {}
                override fun onRegistrationFailed(info: NsdServiceInfo, code: Int) {
                    handler.post { if (pairing === socket) stopDiscovery("Discovery unavailable. Enter the server address instead.") }
                }
            }
            getSystemService(NsdManager::class.java).registerService(info, NsdManager.PROTOCOL_DNS_SD, listener)
            pairingRegistration = listener
            localProblem = "Waiting for Incubator. If needed, connect to ${info.serviceName} or ${address.hostAddress}:${Mobile.Port} from Incubator."
            Thread {
                val found = runCatching { socket.accept().use { it.inetAddress.hostAddress.orEmpty() } }.getOrDefault("")
                handler.post {
                    if (pairing !== socket) return@post
                    if (connectivity.wifiAddress(address.hostAddress)?.first != network) {
                        stopDiscovery("Wi-Fi changed. Try discovery again on your trusted network.")
                        return@post
                    }
                    if (!validAddress(found)) {
                        stopDiscovery("No Incubator connection found. Try again or enter its address.")
                        return@post
                    }
                    stopDiscovery()
                    setupDialog = AlertDialog.Builder(this).setTitle("Save Incubator server?")
                        .setMessage("A device connected from $found. Incubator doesn’t verify its identity. Save only if you recognize this as your server.")
                        .setNegativeButton("Cancel", null)
                        .setPositiveButton("Save server") { _, _ ->
                            settings.edit().putString(MicService.SERVER_IP, found).apply()
                            screen.serverAddress = found
                            localProblem = "Server saved. Tap Talk when you’re ready."
                        }.show()
                }
            }.start()
        } catch (_: Exception) {
            stopDiscovery("Could not start discovery. Try again or enter the server address.")
        } catch (_: LinkageError) {
            stopDiscovery("Native voice engine unavailable")
        }
    }

    private fun stopDiscovery(message: String = "") {
        pairing?.let { runCatching { it.close() } }
        pairing = null
        pairingRegistration?.let { runCatching { getSystemService(NsdManager::class.java).unregisterService(it) } }
        pairingRegistration = null
        localProblem = message
    }

    private fun validAddress(address: String): Boolean {
        val bytes = address.split('.')
        if (bytes.size != 4 || bytes.any { part -> (part.toIntOrNull() ?: -1) !in 0..255 || part.toIntOrNull().toString() != part }) return false
        return bytes[0].toInt() in 1..223 && bytes[0] != "127" && !(bytes[0] == "169" && bytes[1] == "254")
    }

    private fun requestVoice(action: String) {
        val address = screen.serverAddress.trim()
        if (!validAddress(address)) {
            localProblem = "Enter the Incubator server’s IPv4 address, for example 192.168.1.20."
            return
        }
        localProblem = ""
        settings.edit().putString(MicService.SERVER_IP, address).apply()
        val permissions = buildList {
            add(Manifest.permission.RECORD_AUDIO)
            if (Build.VERSION.SDK_INT >= 33) add(Manifest.permission.POST_NOTIFICATIONS)
        }.filter { checkSelfPermission(it) != PackageManager.PERMISSION_GRANTED }
        if (permissions.isNotEmpty()) {
            requestPermissions(permissions.toTypedArray(), if (action == MicService.WAKE_WORD) 3 else 1)
            return
        }
        runCatching { startForegroundService(Intent(this, MicService::class.java).setAction(action)
            .putExtra(MicService.SERVER_IP, address)) }
            .onFailure { localProblem = "Could not start voice capture. Try again." }
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<String>, results: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, results)
        val action = when (requestCode) {
            1 -> MicService.TALK
            3 -> MicService.WAKE_WORD
            else -> return
        }
        if (results.isNotEmpty() && results.all { it == PackageManager.PERMISSION_GRANTED }) requestVoice(action)
        else localProblem = "Allow microphone and notifications in Android Settings to use voice."
        render(service?.state ?: MicState())
    }

    internal fun render(state: MicState) = screen.render(state, pairing != null, localProblem)
}
