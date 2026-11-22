package com.encore.personal

import android.Manifest
import android.app.Activity
import android.app.role.RoleManager
import android.content.ComponentName
import android.content.Intent
import android.content.ServiceConnection
import android.content.pm.PackageManager
import android.graphics.Typeface
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.IBinder
import android.os.Looper
import android.provider.Settings
import android.text.InputType
import android.view.View
import android.view.WindowManager
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView

class MicActivity : Activity() {
    private val handler = Handler(Looper.getMainLooper())
    private val settings by lazy { getSharedPreferences("personal_mic", MODE_PRIVATE) }
    private var service: MicService? = null
    private var bound = false
    private var localProblem = ""
    private lateinit var server: EditText
    private lateinit var status: TextView
    private lateinit var endpoint: TextView
    private lateinit var problem: TextView
    private lateinit var transcript: TextView
    private lateinit var arm: Button
    private lateinit var talk: Button

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

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        window.addFlags(WindowManager.LayoutParams.FLAG_SECURE)
        val content = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(24), dp(28), dp(24), dp(24))
            isFocusableInTouchMode = true
        }
        fun text(label: String, size: Float, margin: Int = 0): TextView = TextView(this).apply {
            text = label
            textSize = size
            content.addView(this, LinearLayout.LayoutParams(-1, -2).apply { topMargin = dp(margin) })
        }
        text(getString(R.string.app_name), 28f).setTypeface(null, Typeface.BOLD)
        text("Voice Incubator on your phone", 16f, 4)
        status = text("", 22f, 30).apply {
            setTypeface(null, Typeface.BOLD)
            accessibilityLiveRegion = View.ACCESSIBILITY_LIVE_REGION_POLITE
        }
        text("Arm on trusted home Wi-Fi. Wake detection runs on your phone. Pause to release the microphone.", 16f, 10)
        server = EditText(this).apply {
            id = View.generateViewId()
            hint = "Incubator server IPv4 address"
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI
            setSingleLine()
            setText(settings.getString(MicService.SERVER_IP, ""))
            content.addView(this, LinearLayout.LayoutParams(-1, -2).apply { topMargin = dp(16) })
        }
        arm = Button(this).apply {
            isAllCaps = false
            textSize = 16f
            setOnClickListener {
                if (service?.state?.endpoint?.isNotEmpty() == true) service?.pause() else requestArm()
                render(service?.state ?: MicState())
            }
            content.addView(this, LinearLayout.LayoutParams(-1, -2).apply { topMargin = dp(8) })
        }
        talk = Button(this).apply {
            isAllCaps = false
            textSize = 16f
            setOnClickListener { if (service?.state?.active == true) service?.stopTurn() else service?.talk() }
            content.addView(this, LinearLayout.LayoutParams(-1, -2))
        }
        endpoint = text("", 14f, 8)
        problem = text("", 16f, 8).apply { accessibilityLiveRegion = View.ACCESSIBILITY_LIVE_REGION_POLITE }
        transcript = text("", 16f, 16)
        if (resources.getBoolean(R.bool.assistant_overlay)) {
            content.addView(Button(this).apply {
                isAllCaps = false
                text = "Set up assistant overlay"
                setOnClickListener { chooseAssistant() }
            }, LinearLayout.LayoutParams(-1, -2).apply { topMargin = dp(12) })
        }
        setContentView(ScrollView(this).apply {
            fitsSystemWindows = true
            addView(content)
        })
        content.requestFocus()
        render(MicState())
    }

    override fun onStart() {
        super.onStart()
        bound = bindService(Intent(this, MicService::class.java), connection, BIND_AUTO_CREATE)
        handler.post(poll)
    }

    override fun onStop() {
        handler.removeCallbacks(poll)
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

    private fun requestArm() {
        val address = server.text.toString().trim()
        val bytes = address.split('.')
        if (bytes.size != 4 || bytes.any { part -> (part.toIntOrNull() ?: -1) !in 0..255 || part.toIntOrNull().toString() != part }) {
            localProblem = "Enter the Incubator server’s IPv4 address, for example 192.168.1.20."
            return
        }
        localProblem = ""
        val permissions = buildList {
            add(Manifest.permission.RECORD_AUDIO)
            if (Build.VERSION.SDK_INT >= 33) add(Manifest.permission.POST_NOTIFICATIONS)
        }.filter { checkSelfPermission(it) != PackageManager.PERMISSION_GRANTED }
        if (permissions.isNotEmpty()) {
            requestPermissions(permissions.toTypedArray(), 1)
            return
        }
        settings.edit().putString(MicService.SERVER_IP, address).apply()
        runCatching { startForegroundService(Intent(this, MicService::class.java).setAction(MicService.ARM)
            .putExtra(MicService.SERVER_IP, address)) }
            .onFailure { localProblem = "Could not arm. Open the app and try again." }
    }

    override fun onRequestPermissionsResult(requestCode: Int, permissions: Array<String>, results: IntArray) {
        super.onRequestPermissionsResult(requestCode, permissions, results)
        if (requestCode != 1) return
        if (results.isNotEmpty() && results.all { it == PackageManager.PERMISSION_GRANTED }) requestArm()
        else localProblem = "Allow microphone and notifications in Android Settings before arming."
        render(service?.state ?: MicState())
    }

    internal fun render(state: MicState) {
        val armed = state.endpoint.isNotEmpty()
        status.show(state.status)
        endpoint.show(state.endpoint)
        problem.show(localProblem.ifEmpty { state.problem })
        transcript.show(state.transcript)
        arm.show(if (armed) "Pause mic" else "Arm mic")
        talk.show(if (state.active) "Stop" else "Talk")
        talk.visibility = if (armed) View.VISIBLE else View.GONE
        talk.isEnabled = state.canTalk || state.active
        server.visibility = if (armed) View.GONE else View.VISIBLE
    }

    private fun TextView.show(value: String) {
        if (text.toString() != value) text = value
        visibility = if (value.isEmpty()) View.GONE else View.VISIBLE
    }

    private fun dp(value: Int) = (value * resources.displayMetrics.density).toInt()
}
