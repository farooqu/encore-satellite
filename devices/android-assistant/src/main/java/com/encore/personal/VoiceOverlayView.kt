package com.encore.personal

import android.content.Context
import android.content.res.ColorStateList
import android.content.res.Configuration
import android.graphics.Color
import android.graphics.Region
import android.graphics.Typeface
import android.graphics.drawable.GradientDrawable
import android.graphics.drawable.RippleDrawable
import android.os.Build
import android.view.Gravity
import android.view.View
import android.widget.Button
import android.widget.FrameLayout
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView

internal enum class OverlayMode { HIDDEN, CUE, PANEL, UNAVAILABLE }

internal class VoiceOverlayView(
    context: Context, close: () -> Unit, setup: () -> Unit,
) : FrameLayout(context) {
    private val cue = LinearLayout(context).apply {
        gravity = Gravity.CENTER
        importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO_HIDE_DESCENDANTS
        for (height in intArrayOf(3, 7, 3)) {
            addView(View(context), LinearLayout.LayoutParams(dp(3), dp(height)).apply {
                marginStart = dp(1); marginEnd = dp(1)
            })
        }
    }
    private var sheetLimit = 0
    private val sheet = object : LinearLayout(context) {
        override fun onMeasure(widthMeasureSpec: Int, heightMeasureSpec: Int) {
            super.onMeasure(widthMeasureSpec, MeasureSpec.makeMeasureSpec(sheetLimit, MeasureSpec.AT_MOST))
        }
    }.apply {
        orientation = LinearLayout.VERTICAL
        elevation = dp(8).toFloat()
    }
    private val brand = ImageView(context).apply {
        setImageResource(R.drawable.ic_second_pulse)
        contentDescription = "Encore Satellite Assistant"
    }
    private data class Line(val text: String, val icon: Int = 0, val speaker: String? = null, val placeholder: Boolean = false)
    private var lines = emptyList<Line>()
    private var foreground = 0
    private var secondary = 0
    private var accent = 0
    private var scrollTarget: Int? = null
    private val transcript = LinearLayout(context).apply {
        orientation = LinearLayout.VERTICAL
        accessibilityLiveRegion = View.ACCESSIBILITY_LIVE_REGION_POLITE
    }
    private val scroll = object : ScrollView(context) {
        override fun onLayout(changed: Boolean, l: Int, t: Int, r: Int, b: Int) {
            super.onLayout(changed, l, t, r, b)
            scrollTarget?.let { scrollTo(0, if (it == Int.MAX_VALUE) transcript.height else it) }
            scrollTarget = null
        }
    }.apply {
        addView(transcript, LayoutParams(-1, -2))
    }
    private val openSetup = button("Open setup", setup)
    private val closeButton = button("×", close).apply {
        contentDescription = "Close voice panel"
        textSize = 23f
        setPadding(0, 0, 0, 0)
    }

    init {
        addView(cue, LayoutParams(dp(38), dp(20), Gravity.BOTTOM or Gravity.CENTER_HORIZONTAL))
        addView(sheet, LayoutParams(-1, -2, Gravity.BOTTOM))
        val header = FrameLayout(context).apply {
            addView(brand, LayoutParams(dp(36), dp(36), Gravity.START or Gravity.CENTER_VERTICAL).apply { marginStart = dp(3) })
            addView(closeButton, LayoutParams(dp(48), dp(48), Gravity.END or Gravity.CENTER_VERTICAL))
        }
        // Keep the brand and close button visible while the conversation scrolls.
        sheet.addView(header, LinearLayout.LayoutParams(-1, -2))
        sheet.addView(openSetup, LinearLayout.LayoutParams(-2, -2).apply { gravity = Gravity.END })
        sheet.addView(scroll, LinearLayout.LayoutParams(-1, -2))
        // Inset the controls, not the sheet background, above system navigation.
        @Suppress("DEPRECATION") // Same inset path on the prototype's API 26 minimum.
        setOnApplyWindowInsetsListener { _, insets ->
            place(insets.systemWindowInsetLeft, insets.systemWindowInsetRight, insets.systemWindowInsetBottom)
            insets
        }
        place(0, 0, 0)
        applySystemColors(resources.configuration)
        render(OverlayMode.HIDDEN, null, false)
    }

    override fun onConfigurationChanged(newConfig: Configuration) {
        super.onConfigurationChanged(newConfig)
        applySystemColors(newConfig)
    }

    private fun applySystemColors(config: Configuration) {
        val night = config.uiMode and Configuration.UI_MODE_NIGHT_MASK == Configuration.UI_MODE_NIGHT_YES
        // Read wallpaper-derived tonal resources rather than the host app's accent.
        val surface: Int
        if (Build.VERSION.SDK_INT >= 31) {
            surface = context.getColor(if (night) android.R.color.system_neutral1_900 else android.R.color.system_neutral1_10)
            foreground = context.getColor(if (night) android.R.color.system_neutral1_50 else android.R.color.system_neutral1_900)
            secondary = context.getColor(if (night) android.R.color.system_neutral2_200 else android.R.color.system_neutral2_700)
            accent = context.getColor(if (night) android.R.color.system_accent1_200 else android.R.color.system_accent1_700)
        } else {
            surface = if (night) 0xff25282d.toInt() else 0xfff7f7fa.toInt()
            foreground = if (night) 0xfff5f6fa.toInt() else 0xff25282d.toInt()
            secondary = if (night) 0xffc4c7cf.toInt() else 0xff50545d.toInt()
            accent = foreground
        }
        sheet.background = GradientDrawable().apply {
            setColor(surface)
            val r = dp(26).toFloat()
            cornerRadii = floatArrayOf(r, r, r, r, 0f, 0f, 0f, 0f)
        }
        cue.background = rounded(surface, 12).apply { setStroke(dp(1), accent and 0x00ffffff or 0x68000000) }
        for (i in 0 until cue.childCount) cue.getChildAt(i).background = rounded(accent, 2)
        brand.setColorFilter(accent)
        drawLines()
        closeButton.setTextColor(secondary)
        closeButton.background = ripple(secondary, Color.TRANSPARENT)
        openSetup.setTextColor(surface)
        openSetup.background = ripple(surface, accent)
    }

    override fun onMeasure(widthMeasureSpec: Int, heightMeasureSpec: Int) {
        // The voice session root fills the screen; the cap includes navigation padding.
        sheetLimit = MeasureSpec.getSize(heightMeasureSpec) / 2
        super.onMeasure(widthMeasureSpec, heightMeasureSpec)
    }

    internal fun render(mode: OverlayMode, turn: VoiceTurn?, armed: Boolean,
        earlier: List<VoiceTurn> = emptyList(), finished: Boolean = false,
    ) {
        val showSheet = mode == OverlayMode.PANEL || mode == OverlayMode.UNAVAILABLE
        cue.visibility = if (mode == OverlayMode.CUE) View.VISIBLE else View.GONE
        sheet.visibility = if (showSheet) View.VISIBLE else View.GONE
        val next = buildList {
            if (mode == OverlayMode.UNAVAILABLE) {
                add(Line(if (armed) "Voice unavailable" else "Mic paused", placeholder = true))
                add(Line(if (armed) "Check your Incubator connection in setup." else "Open setup to arm your phone on trusted Wi-Fi."))
            } else if (mode == OverlayMode.PANEL && turn != null) {
                for (entry in earlier + turn) {
                    val current = entry === turn && !finished
                    val heardIcon = if (current && entry.phase == "working") R.drawable.ic_voice_working else R.drawable.ic_voice_wave
                    if (entry.heard.isNotBlank()) add(Line(entry.heard, heardIcon, "You"))
                    if (entry.reply.isNotBlank()) add(Line(entry.reply, R.drawable.ic_voice_replying, "Encore"))
                    if (!current) continue
                    when {
                        entry.pending -> add(Line("Checking wake…", placeholder = true))
                        entry.phase == "speaking" && entry.reply.isBlank() ->
                            add(Line("Replying…", R.drawable.ic_voice_replying, placeholder = true))
                        entry.phase == "error" -> add(Line("Couldn't complete the request."))
                        entry.phase == "working" && entry.heard.isBlank() ->
                            add(Line("Working…", R.drawable.ic_voice_working, placeholder = true))
                        entry.phase == "listening" && entry.heard.isBlank() ->
                            add(Line("Listening…", R.drawable.ic_voice_wave, placeholder = true))
                    }
                }
            }
        }
        if (next != lines) {
            // Follow new text unless the reader has scrolled back into earlier turns.
            scrollTarget = when {
                !showSheet || next.isEmpty() -> 0
                scrollTarget != null -> scrollTarget
                transcript.height - scroll.height - scroll.scrollY <= dp(8) -> Int.MAX_VALUE
                else -> scroll.scrollY
            }
            lines = next
            drawLines()
        }
        openSetup.visibility = if (mode == OverlayMode.UNAVAILABLE) View.VISIBLE else View.GONE
    }

    private fun drawLines() {
        transcript.removeAllViews()
        lines.forEachIndexed { index, line ->
            val latest = index == lines.lastIndex
            transcript.addView(LinearLayout(context).apply {
                gravity = Gravity.TOP
                setPadding(0, dp(2), dp(6), dp(3))
                addView(View(context).apply {
                    if (latest) background = rounded(accent, 2)
                }, LinearLayout.LayoutParams(dp(2), dp(19)).apply { marginEnd = dp(10) })
                addView(ImageView(context).apply {
                    if (line.icon != 0) setImageResource(line.icon)
                    setColorFilter(accent)
                    importantForAccessibility = View.IMPORTANT_FOR_ACCESSIBILITY_NO
                }, LinearLayout.LayoutParams(dp(17), dp(17)).apply { marginEnd = dp(8); topMargin = dp(2) })
                addView(text(if (line.placeholder) 13f else 17f, bold = latest && !line.placeholder).apply {
                    text = line.text
                    contentDescription = line.speaker?.let { "$it: ${line.text}" }
                    setTextColor(if (latest && !line.placeholder) this@VoiceOverlayView.foreground else secondary)
                }, LinearLayout.LayoutParams(0, -2, 1f))
            }, LinearLayout.LayoutParams(-1, -2))
        }
    }

    internal fun touchablePanel(region: Region) {
        if (sheet.visibility == View.VISIBLE) {
            val location = IntArray(2)
            sheet.getLocationInWindow(location)
            region.set(location[0], location[1], location[0] + sheet.width, location[1] + sheet.height)
        } else region.setEmpty()
    }

    private fun place(left: Int, right: Int, bottom: Int) {
        sheet.setPadding(left + dp(18), dp(3), right + dp(18), bottom + dp(8))
        cue.layoutParams = (cue.layoutParams as LayoutParams).apply { bottomMargin = bottom + dp(8) }
    }

    private fun text(size: Float, bold: Boolean = false) = TextView(context).apply {
        textSize = size
        if (bold) typeface = Typeface.create("sans-serif-medium", Typeface.NORMAL)
    }

    private fun button(label: String, action: () -> Unit) = Button(context).apply {
        text = label
        isAllCaps = false
        textSize = 14f
        minWidth = dp(48)
        minimumWidth = dp(48)
        minHeight = dp(48)
        minimumHeight = dp(48)
        setPadding(dp(14), 0, dp(14), 0)
        setOnClickListener { action() }
    }

    private fun ripple(ink: Int, fill: Int) = RippleDrawable(
        ColorStateList.valueOf(ink and 0x00ffffff or 0x38000000), rounded(fill, 24), rounded(Color.WHITE, 24),
    )

    private fun rounded(color: Int, radius: Int) = GradientDrawable().apply {
        setColor(color)
        cornerRadius = dp(radius).toFloat()
    }

    private fun dp(value: Int) = (value * resources.displayMetrics.density + .5f).toInt()
}
