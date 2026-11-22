package com.encore.satellite

import android.Manifest
import android.app.NotificationManager
import android.app.role.RoleManager
import android.content.*
import android.content.pm.PackageManager
import android.net.Uri
import android.os.Build
import android.os.Bundle
import android.os.IBinder
import android.provider.Settings
import android.view.WindowManager
import android.widget.Toast
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.*
import androidx.core.view.WindowCompat
import androidx.core.view.WindowInsetsCompat
import androidx.core.view.WindowInsetsControllerCompat
import androidx.lifecycle.compose.collectAsStateWithLifecycle
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.ViewModelProvider

class MainActivity : ComponentActivity() {
    private var service by mutableStateOf<EncoreService?>(null)
    private var microphonePermissionGranted by mutableStateOf(false)
    private var microphoneNeedsSettings by mutableStateOf(false)
    private var permissionProblem by mutableStateOf("")
    private var unmuteAfterPermission = false
    private var startRequested = false
    private val setupPreferences by lazy { getSharedPreferences("setup", MODE_PRIVATE) }
    private var setupStage by mutableStateOf(SetupStage.NEW)
    private var notificationsReviewed by mutableStateOf(false)
    private var androidSetup by mutableStateOf(AndroidSetupStatus())
    private val bluetoothSetup = registerForActivityResult(ActivityResultContracts.StartActivityForResult()) {
        if (setupStage == SetupStage.BLUETOOTH) saveSetupStage(SetupStage.EXTRAS)
    }
    private val notifications = registerForActivityResult(ActivityResultContracts.RequestPermission()) {
        refreshAndroidSetup()
        reviewNotifications()
    }
    private val microphone = registerForActivityResult(ActivityResultContracts.RequestMultiplePermissions()) { results ->
        // An empty result means the system prompt was interrupted, not denied.
        if (results[Manifest.permission.RECORD_AUDIO] == false) setupPreferences.edit().putBoolean("microphone_denied", true).apply()
        refreshMicrophonePermission()
        when {
            !microphonePermissionGranted -> permissionProblem = "Microphone permission is required for local wake-word detection."
            unmuteAfterPermission || service?.state?.value?.running == true -> startSatellite(unmuteAfterPermission)
            else -> startIfPermitted()
        }
    }
    private val connection = object : ServiceConnection {
        override fun onServiceConnected(name: ComponentName, binder: IBinder) {
            service = (binder as EncoreService.LocalBinder).service
            service?.bluetooth?.visible(this@MainActivity, lifecycle.currentState.isAtLeast(Lifecycle.State.RESUMED))
            service?.monitorScreenVisible(lifecycle.currentState.isAtLeast(Lifecycle.State.RESUMED) && hasWindowFocus())
            if (lifecycle.currentState.isAtLeast(Lifecycle.State.RESUMED)) startIfPermitted()
        }
        override fun onServiceDisconnected(name: ComponentName) { service = null }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setupStage = SetupStage.entries.firstOrNull { it.name == setupPreferences.getString("stage", null) } ?: SetupStage.NEW
        notificationsReviewed = setupPreferences.getBoolean("notifications_reviewed", false)
        val dashboardAuth = ViewModelProvider(this)[DashboardAuth::class.java]
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
            val bluetoothHost = connectedService?.bluetooth
            val bluetooth = bluetoothHost?.state?.collectAsStateWithLifecycle()?.value ?: BluetoothState()
            val bluetoothPending = if (bluetoothHost == null) null else bluetooth.supported && bluetooth.enabled && bluetooth.setupPending
            val setup = setupStep(setupStage, state, microphonePermissionGranted,
                Build.VERSION.SDK_INT >= 33 && !notificationsReviewed && !androidSetup.notifications,
                bluetoothPending)
            LaunchedEffect(setupStage, state.needsHomeAssistantSetup, bluetoothPending) {
                if (setupStage == SetupStage.NEW && state.needsHomeAssistantSetup) saveSetupStage(SetupStage.CORE)
                if (setupStage == SetupStage.BLUETOOTH && bluetoothPending == false) saveSetupStage(SetupStage.EXTRAS)
            }
            // The microphone setup screen already explains why access is needed.
            val displayedState = if (setup == SetupStep.MICROPHONE || permissionProblem.isEmpty()) state
                else state.copy(problem = permissionProblem)
            EncoreScreen(displayedState,
                onStart = { requestMicrophone(false) }, onTalk = { service?.talk() }, onStop = { service?.stopTurn() },
                onMuted = { if (it) service?.setMuted(true) else requestMicrophone(true) },
                noiseKey = { service?.noiseKey().orEmpty() }, musicToken = { service?.musicToken().orEmpty() },
                onMusicConnect = { startForegroundService(Intent(this, EncoreService::class.java).setAction("music").putExtra("address", it)) },
                onMusicDisconnect = { service?.disconnectMusic() },
                onMusicCommand = { service?.musicCommand(it) },
                onDismiss = { permissionProblem = ""; service?.dismissProblem() },
                onUpdate = { startActivity(Intent(this, UpdateActivity::class.java)) },
                onBluetooth = { startActivity(Intent(this, BluetoothActivity::class.java)) },
                onChooseHome = { openAndroidSettings(Settings.ACTION_HOME_SETTINGS) },
                onNotificationSettings = { openAndroidSettings(Settings.ACTION_APP_NOTIFICATION_SETTINGS, extras = Bundle().apply { putString(Settings.EXTRA_APP_PACKAGE, packageName) }) },
                onPairHomeAssistant = { service?.pairHomeAssistant() },
                microphonePermissionGranted = microphonePermissionGranted,
                dashboardAuth = dashboardAuth,
                dashboardContent = setup?.let { step -> @Composable {
                    SatelliteSetup(step, displayedState, androidSetup,
                        onMicrophone = { requestMicrophone(false) },
                        onAppPermissions = if (microphoneNeedsSettings) {
                            { openAndroidSettings(Settings.ACTION_APPLICATION_DETAILS_SETTINGS, Uri.parse("package:$packageName")) }
                        } else null,
                        onNotifications = { notifications.launch(Manifest.permission.POST_NOTIFICATIONS) },
                        onSkipNotifications = ::reviewNotifications,
                        onPair = { service?.pairHomeAssistant() },
                        onVoiceContinue = { saveSetupStage(if (bluetoothPending == true) SetupStage.BLUETOOTH else SetupStage.EXTRAS) },
                        onBluetooth = { bluetoothSetup.launch(Intent(this, BluetoothActivity::class.java)) },
                        onExtras = { saveSetupStage(SetupStage.EXTRAS) },
                        onChooseHome = { openAndroidSettings(Settings.ACTION_HOME_SETTINGS) },
                        onRemoteInstalls = { startActivity(Intent(this, UpdateActivity::class.java).putExtra(UpdateActivity.REMOTE_SETTINGS, true)) },
                        onDone = { saveSetupStage(SetupStage.DONE) })
                } },
                onSetup = { if (setupStage == SetupStage.NEW || setupStage == SetupStage.DONE) {
                    saveSetupStage(if (state.needsHomeAssistantSetup) SetupStage.CORE else SetupStage.EXTRAS)
                } },
                onMonitorOverlay = { id, visible -> connectedService?.setMonitorOverlay(id, visible) })
        }
    }

    private fun openAndroidSettings(action: String, data: Uri? = null, extras: Bundle? = null) {
        fun open(intent: Intent) = runCatching {
            startActivity(intent.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
        }.isSuccess
        val intent = Intent(action, data).apply { if (extras != null) putExtras(extras) }
        if (open(intent)) return
        if (action == Settings.ACTION_HOME_SETTINGS && open(Intent(Settings.ACTION_SETTINGS))) {
            Toast.makeText(this, "In Android Settings, look for Apps → Default apps → Home app.", Toast.LENGTH_LONG).show()
            return
        }
        Toast.makeText(this, "Android settings are unavailable on this device", Toast.LENGTH_LONG).show()
    }

    private fun saveSetupStage(stage: SetupStage) {
        setupPreferences.edit().putString("stage", stage.name).apply()
        setupStage = stage
    }

    private fun reviewNotifications() {
        setupPreferences.edit().putBoolean("notifications_reviewed", true).apply()
        notificationsReviewed = true
    }

    private fun refreshAndroidSetup() {
        val home = runCatching {
            if (Build.VERSION.SDK_INT >= 29) {
                getSystemService(RoleManager::class.java)?.takeIf { it.isRoleAvailable(RoleManager.ROLE_HOME) }
                    ?.isRoleHeld(RoleManager.ROLE_HOME)
            } else {
                packageManager.resolveActivity(Intent(Intent.ACTION_MAIN).addCategory(Intent.CATEGORY_HOME), PackageManager.MATCH_DEFAULT_ONLY)
                    ?.activityInfo?.takeUnless { it.packageName == "android" }?.let { it.packageName == packageName }
            }
        }.getOrNull()
        val installer = ApkInstall(this)
        val notificationsAllowed = (Build.VERSION.SDK_INT < 33 ||
            checkSelfPermission(Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED) &&
            getSystemService(NotificationManager::class.java).areNotificationsEnabled()
        androidSetup = AndroidSetupStatus(notificationsAllowed, home,
            if (!installer.supported) null else when {
                installer.canInstall -> "On · Android allows this source"
                installer.optedIn -> "Paused · Android source approval needed"
                else -> "Off"
            })
    }

    private fun hasMicrophonePermission() = checkSelfPermission(Manifest.permission.RECORD_AUDIO) == PackageManager.PERMISSION_GRANTED

    private fun refreshMicrophonePermission() {
        microphonePermissionGranted = hasMicrophonePermission()
        val denied = setupPreferences.getBoolean("microphone_denied", false)
        // Android also returns false for rationale before the first request.
        microphoneNeedsSettings = !microphonePermissionGranted && denied &&
            !shouldShowRequestPermissionRationale(Manifest.permission.RECORD_AUDIO)
        if (microphonePermissionGranted && denied) setupPreferences.edit().remove("microphone_denied").apply()
    }

    private fun requestMicrophone(unmute: Boolean) {
        permissionProblem = ""
        unmuteAfterPermission = unmute
        if (hasMicrophonePermission()) startSatellite(unmute)
        else microphone.launch(arrayOf(Manifest.permission.RECORD_AUDIO))
    }

    private fun startIfPermitted() {
        val connected = service ?: return
        if (microphonePermissionGranted && !startRequested && (!connected.state.value.running || connected.awaitingUpdateMicrophone)) startSatellite(false)
    }

    private fun startSatellite(unmute: Boolean) {
        startRequested = true
        startForegroundService(Intent(this, EncoreService::class.java).putExtra("unmute", unmute))
    }

    override fun onResume() {
        super.onResume()
        refreshMicrophonePermission()
        refreshAndroidSetup()
        if (microphonePermissionGranted) permissionProblem = ""
        startIfPermitted()
        service?.bluetooth?.visible(this, true)
        service?.monitorScreenVisible(hasWindowFocus())
    }
    override fun onWindowFocusChanged(hasFocus: Boolean) {
        super.onWindowFocusChanged(hasFocus)
        service?.monitorScreenVisible(hasFocus && lifecycle.currentState.isAtLeast(Lifecycle.State.RESUMED))
    }
    override fun onPause() {
        service?.monitorScreenVisible(false)
        service?.bluetooth?.visible(this, false)
        super.onPause()
    }
    override fun onStop() {
        startRequested = false
        super.onStop()
    }
    override fun onDestroy() { unbindService(connection); super.onDestroy() }
}
