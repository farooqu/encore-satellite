package com.encore.satellite

import android.Manifest
import android.content.*
import android.content.pm.PackageManager
import android.os.Bundle
import android.os.IBinder
import android.view.WindowManager
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.*
import androidx.lifecycle.compose.collectAsStateWithLifecycle

class MainActivity : ComponentActivity() {
    private var service by mutableStateOf<EncoreService?>(null)
    private var permissionProblem by mutableStateOf("")
    private var unmuteAfterPermission = false
    private val permissions = registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) {
        if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED) startSatellite(unmuteAfterPermission)
        else permissionProblem = "Microphone permission is required for local wake-word detection."
    }
    private val connection = object : ServiceConnection {
        override fun onServiceConnected(name: ComponentName, binder: IBinder) { service = (binder as EncoreService.LocalBinder).service }
        override fun onServiceDisconnected(name: ComponentName) { service = null }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON or WindowManager.LayoutParams.FLAG_SECURE)
        bindService(Intent(this, EncoreService::class.java), connection, BIND_AUTO_CREATE)
        setContent {
            val connectedService = service
            val state = if (connectedService != null) connectedService.state.collectAsStateWithLifecycle().value else EncoreState()
            EncoreScreen(state.copy(problem = permissionProblem.ifEmpty { state.problem }),
                onStart = { requestMicrophone(false) }, onTalk = { service?.talk() }, onStop = { service?.stopTurn() },
                onMuted = { if (it) service?.setMuted(true) else requestMicrophone(true) },
                noiseKey = { service?.noiseKey().orEmpty() }, musicToken = { service?.musicToken().orEmpty() },
                onMusicConnect = { startForegroundService(Intent(this, EncoreService::class.java).setAction("music").putExtra("address", it)) },
                onMusicDisconnect = { service?.disconnectMusic() },
                onMusicCommand = { service?.musicCommand(it) },
                onDismiss = { permissionProblem = ""; service?.dismissProblem() })
        }
    }

    private fun requestMicrophone(unmute: Boolean) {
        permissionProblem = ""
        unmuteAfterPermission = unmute
        if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED) startSatellite(unmute)
        else permissions.launch(if (android.os.Build.VERSION.SDK_INT >= 33)
            arrayOf(Manifest.permission.RECORD_AUDIO, Manifest.permission.POST_NOTIFICATIONS) else arrayOf(Manifest.permission.RECORD_AUDIO))
    }
    private fun startSatellite(unmute: Boolean) { startForegroundService(Intent(this, EncoreService::class.java).putExtra("unmute", unmute)) }
    override fun onDestroy() { unbindService(connection); super.onDestroy() }
}
