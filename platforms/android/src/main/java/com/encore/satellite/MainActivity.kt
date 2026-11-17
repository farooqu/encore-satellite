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
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.Lifecycle

class MainActivity : ComponentActivity() {
    private var service by mutableStateOf<EncoreService?>(null)
    private var permissionProblem by mutableStateOf("")
    private var unmuteAfterPermission = false
    private val permissions = registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) {
        if (checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED) startSatellite(unmuteAfterPermission)
        else permissionProblem = "Microphone permission is required for local wake-word detection."
    }
    private val connection = object : ServiceConnection {
        override fun onServiceConnected(name: ComponentName, binder: IBinder) {
            service = (binder as EncoreService.LocalBinder).service
            service?.bluetooth?.visible(this@MainActivity, lifecycle.currentState.isAtLeast(Lifecycle.State.RESUMED))
        }
        override fun onServiceDisconnected(name: ComponentName) { service = null }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.addFlags(WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON or WindowManager.LayoutParams.FLAG_SECURE)
        WindowCompat.setDecorFitsSystemWindows(window, false)
        WindowCompat.getInsetsController(window, window.decorView).apply {
            systemBarsBehavior = WindowInsetsControllerCompat.BEHAVIOR_SHOW_TRANSIENT_BARS_BY_SWIPE
            hide(WindowInsetsCompat.Type.systemBars())
        }
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
                onDismiss = { permissionProblem = ""; service?.dismissProblem() },
                onUpdate = { startActivity(Intent(this, UpdateActivity::class.java)) },
                onBluetooth = { startActivity(Intent(this, BluetoothActivity::class.java)) })
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
    override fun onResume() { super.onResume(); service?.bluetooth?.visible(this, true) }
    override fun onPause() { service?.bluetooth?.visible(this, false); super.onPause() }
    override fun onDestroy() { unbindService(connection); super.onDestroy() }
}
