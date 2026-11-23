package com.encore.personal

import android.content.Context
import android.content.res.ColorStateList
import android.content.res.Configuration
import android.graphics.Color
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.graphics.drawable.RippleDrawable
import android.graphics.drawable.StateListDrawable
import android.os.Build
import android.text.InputType
import android.view.Gravity
import android.view.View
import android.widget.Button
import android.widget.EditText
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.Switch
import android.widget.TextView

internal class MicScreen(
    context: Context,
    onDiscover: () -> Unit,
    onWakeWord: (Boolean) -> Unit,
    onTalk: () -> Unit,
    onChooseAssistant: () -> Unit,
) : ScrollView(context) {
    val dark = resources.configuration.uiMode and Configuration.UI_MODE_NIGHT_MASK == Configuration.UI_MODE_NIGHT_YES
    val surface = tone(android.R.color.system_neutral1_10, android.R.color.system_neutral1_900, android.R.attr.colorBackground)
    private val ink = tone(android.R.color.system_neutral1_900, android.R.color.system_neutral1_50, android.R.attr.textColorPrimary)
    private val muted = tone(android.R.color.system_neutral2_700, android.R.color.system_neutral2_200, android.R.attr.textColorSecondary)
    private val border = tone(android.R.color.system_neutral2_200, android.R.color.system_neutral2_600, android.R.attr.colorControlNormal)
    private val accent = tone(android.R.color.system_accent1_700, android.R.color.system_accent1_200, android.R.attr.colorAccent)
    private val onAccent = if (Color.luminance(accent) > 0.179f) Color.BLACK else Color.WHITE
    private val accentCard = tone(android.R.color.system_accent1_100, android.R.color.system_accent1_800, android.R.attr.colorBackgroundFloating)
    private val onAccentCard = tone(android.R.color.system_accent1_900, android.R.color.system_accent1_100, android.R.attr.textColorPrimary)

    private val status: TextView
    private val guidance: TextView
    private val setup: LinearLayout
    private val connection: LinearLayout
    private val discover: Button
    private val manual: LinearLayout
    private val server: EditText
    @Suppress("UseSwitchCompatOrMaterialCode") // The platform switch follows the phone's theme.
    private val wakeWord: Switch
    private val talk: Button
    private val endpoint: TextView
    private val problem: TextView
    private val transcript: TextView
    private var assistant: Button? = null

    var serverAddress: String
        get() = server.text.toString()
        set(value) = server.setText(value)

    init {
        fitsSystemWindows = true
        isFillViewport = true
        setBackgroundColor(surface)
        val content = column().apply {
            setPadding(dp(20), dp(16), dp(20), dp(20))
            isFocusableInTouchMode = true
        }
        addView(content)
        val header = LinearLayout(context).apply { gravity = Gravity.CENTER_VERTICAL }
        content.addView(header)
        header.addView(symbol(R.drawable.ic_second_pulse, accent), LinearLayout.LayoutParams(dp(54), dp(54)))
        val wordmark = column()
        header.addView(wordmark, LinearLayout.LayoutParams(-1, -2).apply { marginStart = dp(6) })
        wordmark.label("Encore", 28f, ink, heading = true)
        wordmark.label("Personal voice assistant", 14f, muted, margin = 2)

        val mic = content.panel(accentCard)
        status = mic.label("", 26f, onAccentCard, heading = true).apply {
            accessibilityLiveRegion = View.ACCESSIBILITY_LIVE_REGION_POLITE
        }
        guidance = mic.label("", 15f, onAccentCard, margin = 5)
        talk = mic.action("Talk", filled = true, margin = 16, onTalk)
        transcript = mic.label("", 15f, onAccentCard, margin = 14)
        problem = mic.label("", 14f, onAccentCard, margin = 12, medium = true).apply {
            accessibilityLiveRegion = View.ACCESSIBILITY_LIVE_REGION_POLITE
        }

        val wake = content.section()
        wakeWord = Switch(context).apply {
            text = "Listen for wake word"
            textSize = 18f
            setTextColor(ink)
            minHeight = dp(48)
            setOnClickListener { onWakeWord(isChecked) }
        }
        wake.addView(wakeWord, LinearLayout.LayoutParams(-1, -2))
        wake.label("Keeps the mic active in the background. Talk and the Android assistant shortcut work without it.",
            14f, muted)

        connection = content.section(divider = true)
        connection.label("Voice Incubator", 20f, ink, heading = true)
        endpoint = connection.label("", 14f, muted, margin = 6)
        setup = column().also { connection.addView(it) }
        setup.label("Find your server once on trusted home Wi-Fi.", 14f, muted, margin = 4)
        discover = setup.action("Discover on home Wi-Fi", filled = true, margin = 12, onDiscover)
        manual = column().also { setup.addView(it) }
        server = EditText(context).apply {
            id = View.generateViewId()
            hint = "e.g. 192.168.1.20"
            inputType = InputType.TYPE_CLASS_TEXT or InputType.TYPE_TEXT_VARIATION_URI
            setSingleLine()
            setTextColor(ink)
            setHintTextColor(muted)
            textSize = 16f
            minHeight = dp(50)
            background = rounded(surface, 8).apply { setStroke(dp(1), border) }
            setPaddingRelative(dp(14), dp(8), dp(14), dp(8))
        }
        manual.label("Server address · manual option", 13f, muted, margin = 12, medium = true).labelFor = server.id
        manual.addView(server, LinearLayout.LayoutParams(-1, -2).apply { topMargin = dp(5) })

        if (resources.getBoolean(R.bool.assistant_overlay)) {
            val shortcut = content.section(divider = true)
            shortcut.label("Android assistant shortcut", 20f, ink, heading = true)
            shortcut.label("Use your phone’s assistant gesture or button to talk to Encore. This replaces your current default assistant.",
                14f, muted, margin = 5)
            assistant = shortcut.action("Set as default assistant", filled = false, margin = 8, onChooseAssistant)
        }
        content.requestFocus()
    }

    fun updateAssistantRole(held: Boolean) {
        assistant?.text = if (held) "Manage default assistant" else "Set as default assistant"
    }

    fun render(state: MicState, pairing: Boolean, localProblem: String) {
        status.show(if (!state.armed && serverAddress.isBlank()) "Connect to Incubator" else state.status)
        guidance.show(when {
            state.pendingTalk -> "You can cancel while it connects."
            state.active -> "Tap Stop to end this request."
            state.wakeWord && !state.canTalk -> "Encore will reconnect when Incubator is available."
            state.wakeWord -> "You can also tap Talk to start a request."
            !state.armed && serverAddress.isBlank() -> "Find your Incubator server below to get started."
            else -> "The mic opens when you start a request."
        })
        problem.show(localProblem.ifEmpty { state.problem })
        transcript.show(state.transcript)
        endpoint.show(if (state.armed) state.endpoint else "")
        connection.visibility = if (!state.armed || state.endpoint.isNotEmpty()) View.VISIBLE else View.GONE
        setup.visibility = if (state.armed) View.GONE else View.VISIBLE
        discover.show(if (state.armed) "" else if (pairing) "Cancel discovery" else "Discover on home Wi-Fi")
        manual.visibility = if (state.armed || pairing) View.GONE else View.VISIBLE
        server.visibility = manual.visibility
        wakeWord.isChecked = state.wakeWord
        wakeWord.isEnabled = !pairing
        val talkLabel = if (state.pendingTalk) "Cancel" else if (state.active) "Stop" else "Talk"
        if (talk.text.toString() != talkLabel) talk.text = talkLabel
        talk.isEnabled = !pairing
    }

    private fun tone(light: Int, night: Int, themeAttribute: Int): Int =
        if (Build.VERSION.SDK_INT >= 31) context.getColor(if (dark) night else light)
        else context.obtainStyledAttributes(intArrayOf(themeAttribute)).let { colors ->
            colors.getColor(0, Color.TRANSPARENT).also { colors.recycle() }
        }

    private fun column() = LinearLayout(context).apply { orientation = LinearLayout.VERTICAL }

    private fun LinearLayout.panel(color: Int) = column().apply {
        background = rounded(color, 24)
        setPadding(dp(18), dp(16), dp(18), dp(16))
        this@panel.addView(this, LinearLayout.LayoutParams(-1, -2).apply { topMargin = dp(16) })
    }

    private fun LinearLayout.section(divider: Boolean = false) = column().apply {
        if (divider) addView(View(context).apply { setBackgroundColor(border) },
            LinearLayout.LayoutParams(-1, dp(1)).apply { bottomMargin = dp(12) })
        this@section.addView(this, LinearLayout.LayoutParams(-1, -2).apply { topMargin = dp(16) })
    }

    private fun symbol(resource: Int, foreground: Int) = ImageView(context).apply {
        setImageResource(resource)
        setColorFilter(foreground)
        importantForAccessibility = IMPORTANT_FOR_ACCESSIBILITY_NO
    }

    private fun LinearLayout.label(value: String, size: Float, color: Int, margin: Int = 0, heading: Boolean = false,
        medium: Boolean = false) = TextView(context).apply {
        text = value
        textSize = size
        setTextColor(color)
        setLineSpacing(dp(2).toFloat(), 1f)
        if (heading || medium) typeface = Typeface.create("sans-serif-medium", Typeface.NORMAL)
        if (Build.VERSION.SDK_INT >= 28) isAccessibilityHeading = heading
        this@label.addView(this, LinearLayout.LayoutParams(-1, -2).apply { topMargin = dp(margin) })
    }

    private fun LinearLayout.action(label: String, filled: Boolean, margin: Int, click: () -> Unit) =
        button(label, filled, click).also {
            addView(it, LinearLayout.LayoutParams(if (filled) -1 else -2, -2).apply { topMargin = dp(margin) })
        }

    private fun button(label: String, filled: Boolean, click: () -> Unit, textColor: Int = accent) = Button(context).apply {
        val enabledInk = if (filled) onAccent else textColor
        val enabledFill = if (filled) accent else Color.TRANSPARENT
        val disabled = intArrayOf(-android.R.attr.state_enabled)
        val fills = StateListDrawable().apply {
            addState(disabled, rounded(if (filled) border and 0x00ffffff or 0x45000000 else Color.TRANSPARENT, 14))
            addState(intArrayOf(), rounded(enabledFill, 14))
        }
        text = label
        isAllCaps = false
        textSize = 15f
        typeface = Typeface.create("sans-serif-medium", Typeface.NORMAL)
        setTextColor(ColorStateList(arrayOf(disabled, intArrayOf()), intArrayOf(muted, enabledInk)))
        background = RippleDrawable(ColorStateList.valueOf((enabledInk and 0x00ffffff) or 0x26000000), fills, rounded(Color.WHITE, 14))
        minHeight = dp(48)
        minimumHeight = dp(48)
        stateListAnimator = null
        setPaddingRelative(dp(14), dp(8), dp(14), dp(8))
        setOnClickListener { click() }
    }

    private fun rounded(color: Int, radius: Int) = GradientDrawable().apply {
        cornerRadius = dp(radius).toFloat()
        setColor(color)
    }

    private fun TextView.show(value: String) {
        if (text.toString() != value) text = value
        visibility = if (value.isEmpty()) View.GONE else View.VISIBLE
    }

    private fun dp(value: Int) = (value * resources.displayMetrics.density).toInt()
}
