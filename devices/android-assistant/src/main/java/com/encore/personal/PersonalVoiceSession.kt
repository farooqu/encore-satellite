package com.encore.personal

import android.content.Context
import android.content.Intent
import android.graphics.Color
import android.graphics.drawable.ColorDrawable
import android.os.Build
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.service.voice.VoiceInteractionSession
import android.view.View
import android.view.WindowManager.LayoutParams
import org.json.JSONObject

// One overlay invocation can contain multiple server turns. Nothing survives clear().
internal class OverlayCall(private val handler: Handler, expire: () -> Unit) {
    private val timeout = Runnable { expire() }
    private val previous = mutableListOf<VoiceTurn>()
    val earlier: List<VoiceTurn> get() = previous
    var turn: VoiceTurn? = null
        private set
    var finished = false
        private set

    fun start(live: VoiceTurn?) {
        clear()
        turn = live
    }

    fun update(live: VoiceTurn?, key: TurnKey, done: Boolean): Boolean {
        val prior = turn ?: return false
        // Android may attach a new window from a live read before queued snapshots arrive.
        if (key.generation < prior.key.generation ||
            (key.generation == prior.key.generation && key.turn < prior.key.turn)) return true
        if (live == null) {
            if (!done || key != prior.key || !prior.canPresent ||
                (prior.heard.isBlank() && prior.reply.isBlank() && previous.isEmpty())) return false
            if (!finished) {
                finished = true
                handler.postDelayed(timeout, 6000)
            }
            return true
        }
        if (finished || (!live.canPresent && !live.pending)) return false
        if (key != prior.key) {
            if (!prior.canPresent || key.generation != prior.key.generation || live.invocation != "follow_up") return false
            if (prior.heard.isNotBlank() || prior.reply.isNotBlank()) previous.add(prior)
        } else if ((prior.canPresent && live.pending) ||
            (prior.heard.isNotBlank() && live.heard.isBlank()) ||
            (prior.reply.isNotBlank() && live.reply.isBlank())) {
            return true
        }
        turn = live
        return true
    }

    fun clear() {
        handler.removeCallbacks(timeout)
        previous.clear()
        turn = null
        finished = false
    }
}

internal class PersonalVoiceSession(context: Context) : VoiceInteractionSession(context) {
    private var mic: MicService? = null
    private val call = OverlayCall(Handler(Looper.getMainLooper()), ::dismiss)
    private var mode = OverlayMode.HIDDEN
    private var panel: VoiceOverlayView? = null
    private var manual = false

    override fun onCreate() {
        super.onCreate()
        setDisabledShowContext(SHOW_WITH_ASSIST or SHOW_WITH_SCREENSHOT)
        setKeepAwake(false)
    }

    override fun onPrepareShow(args: Bundle?, showFlags: Int) {
        super.onPrepareShow(args, showFlags)
        if (args?.containsKey(PersonalAssistant.WAKE_REQUEST) == true) {
            // System delivery is asynchronous. A late show must not reveal a
            // rejected wake or replace an already visible manual interaction.
            PersonalAssistant.ready?.currentWake(args.getLong(PersonalAssistant.WAKE_REQUEST))?.let { (source, live) ->
                if (!manual) {
                    mic = source
                    call.start(live)
                    mode = if (live.accepted) OverlayMode.PANEL else OverlayMode.CUE
                }
            }
        } else {
            manual = true
            mic = MicService.armedMic
            mic?.talk()
            call.start(mic?.voiceTurn())
            mode = if (call.turn != null) OverlayMode.PANEL else OverlayMode.UNAVAILABLE
        }
        setUiEnabled(mode != OverlayMode.HIDDEN)
    }

    override fun onCreateContentView(): View = VoiceOverlayView(context, ::stop, ::openSetup).also { panel = it }

    override fun onShow(args: Bundle?, showFlags: Int) {
        super.onShow(args, showFlags)
        if (mode == OverlayMode.HIDDEN) {
            hide()
            return
        }
        PersonalAssistant.ready?.session = this
        render()
    }

    internal fun owns(source: MicService, key: TurnKey) = mic === source && call.turn?.key == key && mode != OverlayMode.HIDDEN

    internal fun invalidate(source: MicService) { if (mic === source) dismiss() }

    internal fun update(source: MicService, observed: JSONObject, current: VoiceTurn?) {
        if (mic !== source || mode == OverlayMode.HIDDEN || call.turn == null) return
        val live = VoiceTurn.from(observed)
        // A queued success must not reveal a wake that is no longer accepted on the phone.
        if (mode == OverlayMode.CUE && live?.canPresent == true &&
            (current?.key != live.key || !current.canPresent)) return
        val key = TurnKey(observed.getLong("generation"), observed.getLong("turn"))
        val reason = observed.optString("reason")
        val done = mode == OverlayMode.PANEL && observed.optString("phase") == "idle" &&
            (reason == "done" || reason == "not_recognized") && !observed.optBoolean("active") && !observed.optBoolean("inhibited")
        if (!call.update(live, key, done)) {
            dismiss()
            return
        }
        mode = if (call.turn?.canPresent == true || manual) OverlayMode.PANEL else OverlayMode.CUE
        render()
    }

    @Suppress("DEPRECATION") // Older voice-interaction windows can set SHOW_WHEN_LOCKED by flag.
    private fun render() {
        val window = window.window ?: return
        window.setBackgroundDrawable(ColorDrawable(Color.TRANSPARENT))
        window.setWindowAnimations(0)
        window.clearFlags(LayoutParams.FLAG_DIM_BEHIND or LayoutParams.FLAG_KEEP_SCREEN_ON or LayoutParams.FLAG_SHOW_WHEN_LOCKED)
        window.addFlags(LayoutParams.FLAG_NOT_TOUCH_MODAL)
        val passive = LayoutParams.FLAG_NOT_TOUCHABLE or LayoutParams.FLAG_NOT_FOCUSABLE
        if (mode == OverlayMode.CUE) {
            window.clearFlags(LayoutParams.FLAG_SECURE)
            window.addFlags(passive)
        } else {
            window.addFlags(LayoutParams.FLAG_SECURE)
            window.clearFlags(passive)
        }
        panel?.render(mode, call.turn, mic != null, call.earlier, call.finished)
    }

    override fun onComputeInsets(outInsets: Insets) {
        super.onComputeInsets(outInsets)
        outInsets.touchableInsets = Insets.TOUCHABLE_INSETS_REGION
        panel?.touchablePanel(outInsets.touchableRegion)
    }

    private fun stop() {
        if (!call.finished) call.turn?.key?.let { mic?.stopTurn(it) }
        dismiss()
    }

    private fun openSetup() {
        val intent = Intent(context, MicActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        if (Build.VERSION.SDK_INT >= 29) startAssistantActivity(intent) else context.startActivity(intent)
        dismiss()
    }

    internal fun dismiss() {
        if (mode == OverlayMode.HIDDEN) return
        clear()
        hide()
    }

    private fun clear() {
        if (PersonalAssistant.ready?.session === this) PersonalAssistant.ready?.session = null
        mode = OverlayMode.HIDDEN
        call.clear()
        panel?.render(mode, null, false)
        manual = false
        mic = null
    }

    override fun onBackPressed() { if (mode == OverlayMode.PANEL) stop() else dismiss() }

    override fun onLockscreenShown() { dismiss() }

    override fun onHide() { clear(); super.onHide() }

    override fun onDestroy() { clear(); panel = null; super.onDestroy() }
}
