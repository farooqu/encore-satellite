package com.encore.personal

import android.app.Activity
import android.content.res.Configuration
import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Insets
import android.graphics.PorterDuff
import android.graphics.PorterDuffColorFilter
import android.graphics.Region
import android.graphics.drawable.AdaptiveIconDrawable
import android.graphics.drawable.ColorDrawable
import android.os.Handler
import android.os.Looper
import android.view.Gravity
import android.view.View
import android.view.ViewGroup
import android.view.WindowInsets
import android.widget.Button
import android.widget.EditText
import android.widget.FrameLayout
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith
import org.robolectric.Robolectric
import org.robolectric.RobolectricTestRunner
import org.robolectric.RuntimeEnvironment
import org.robolectric.annotation.Config
import org.robolectric.annotation.GraphicsMode
import org.robolectric.Shadows.shadowOf
import java.io.File
import java.time.Duration

@RunWith(RobolectricTestRunner::class)
@Config(sdk = [35], qualifiers = "w393dp-h780dp-notnight-xhdpi")
@GraphicsMode(GraphicsMode.Mode.NATIVE)
class MicActivityTest {
    @Test fun launcherIconHasTwoPulsesInDefaultAndThemedColors() {
        val app = RuntimeEnvironment.getApplication()
        // Resolve the manifest resource directly; Robolectric's package manager supplies a default icon.
        val icon = app.getDrawable(app.applicationInfo.icon) as AdaptiveIconDrawable
        val themedMark = requireNotNull(icon.monochrome).mutate().apply { setTint(0xffdee7ff.toInt()) }
        val themed = AdaptiveIconDrawable(ColorDrawable(0xff303d60.toInt()), themedMark)

        for ((name, drawable, ink) in listOf(
            Triple("default", icon, 0xfff5f6fa.toInt()),
            Triple("themed", themed, 0xffdee7ff.toInt()),
        )) {
            val bitmap = Bitmap.createBitmap(192, 192, Bitmap.Config.ARGB_8888)
            drawable.setBounds(0, 0, bitmap.width, bitmap.height)
            drawable.draw(Canvas(bitmap))
            val row = (0 until bitmap.width).map { bitmap.getPixel(it, bitmap.height / 2) == ink }
            assertEquals("Six waveform bars in $name", 6, row.zipWithNext().count { (left, right) -> !left && right })
            val path = File("build/reports/screenshots").apply { mkdirs() }.resolve("launcher-$name.png")
            path.outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
            bitmap.recycle()
        }
    }

    @Test fun pendingWakeRequestsOneShowImmediatelyAndAcceptanceDoesNotRequestAnother() {
        val requests = mutableListOf<TurnKey>()
        val window = WakeWindow(requests::add)
        val candidate = VoiceTurn(TurnKey(4, 10), "wake", "tentative", false, "", "")
        assertFalse(candidate.canPresent)
        window.update(candidate)
        assertEquals(listOf(candidate.key), requests)
        window.update(candidate)
        window.update(candidate.copy(accepted = true, phase = "listening"))
        // Acceptance updates the existing window; it does not issue a second show.
        assertEquals(listOf(candidate.key), requests)
        window.update(null)

        val firstSeenAccepted = candidate.copy(key = TurnKey(4, 11), accepted = true, phase = "listening")
        window.update(firstSeenAccepted)
        assertEquals(listOf(candidate.key, firstSeenAccepted.key), requests)
        window.update(null)
    }

    @Test fun completedCallLingersButFollowUpsAndNewCallsDoNotInheritTheTimer() {
        val loop = shadowOf(Looper.getMainLooper())
        var expired = 0
        val call = OverlayCall(Handler(Looper.getMainLooper())) { expired++ }
        val first = VoiceTurn(TurnKey(4, 8), "wake", "speaking", true, "Turn on the porch light.", "It's on.")
        val follow = first.copy(key = TurnKey(4, 9), invocation = "follow_up", phase = "listening", heard = "", reply = "")
        call.start(first)
        assertTrue(call.update(first.copy(phase = "tentative", accepted = false, heard = "", reply = ""), first.key, false))
        assertEquals(first, call.turn)
        assertTrue(call.update(follow, follow.key, false))
        assertEquals(listOf(first), call.earlier)
        assertFalse(call.finished)
        assertFalse(call.update(null, follow.key, false))
        assertFalse(call.update(follow.copy(key = TurnKey(5, 1)), TurnKey(5, 1), false))
        assertTrue(call.update(null, follow.key, true))
        assertTrue(call.finished)
        loop.idleFor(Duration.ofMillis(5999))
        assertEquals(0, expired)
        assertTrue(call.update(null, follow.key, true))
        loop.idleFor(Duration.ofMillis(1))
        assertEquals(1, expired)

        call.start(first)
        assertTrue(call.update(null, first.key, true))
        call.start(follow)
        assertTrue(call.earlier.isEmpty())
        assertFalse(call.finished)
        loop.idleFor(Duration.ofMillis(6000))
        assertEquals(1, expired)
    }

    @Test fun edgePanelFitsContentUntilHalfScreenAndKeepsCloseReachable() {
        Robolectric.buildActivity(Activity::class.java).setup().use { controller ->
            val context = controller.get()
            val root = FrameLayout(context).apply { setBackgroundColor(0xfff4f3ff.toInt()) }
            val background = LinearLayout(context).apply {
                orientation = LinearLayout.VERTICAL
                setPadding(40, 88, 40, 0)
                val labels = listOf("Messages", "Jordan\nSee you by the canal.", "Maya\nI added it to the list.", "Lee\nThanks for the heads-up!")
                labels.forEachIndexed { i, label ->
                    addView(TextView(context).apply {
                        text = label
                        setTextColor(0xff29223d.toInt())
                        textSize = if (i == 0) 30f else 17f
                        setPadding(0, 0, 0, 32)
                    })
                }
            }
            root.addView(background, FrameLayout.LayoutParams(-1, -1))
            var closes = 0
            val overlay = VoiceOverlayView(context, { closes++ }, {})
            root.addView(overlay, FrameLayout.LayoutParams(-1, -1))
            val bar = View(context).apply { setBackgroundColor(0xff76897f.toInt()) }
            root.addView(bar, FrameLayout.LayoutParams(164, 6, Gravity.BOTTOM or Gravity.CENTER_HORIZONTAL).apply { bottomMargin = 12 })
            context.setContentView(root)
            @Suppress("DEPRECATION")
            overlay.dispatchApplyWindowInsets(WindowInsets.Builder().setSystemWindowInsets(Insets.of(0, 0, 0, 42)).build())
            val region = Region()
            val candidate = VoiceTurn(TurnKey(1, 2), "wake", "tentative", false, "", "")

            overlay.render(OverlayMode.CUE, candidate, true)
            capture(root, "overlay-cue")
            overlay.touchablePanel(region)
            assertTrue(region.isEmpty)

            overlay.render(OverlayMode.HIDDEN, null, true)
            capture(root, "overlay-dismissed")
            overlay.touchablePanel(region)
            assertTrue(region.isEmpty)

            val accepted = candidate.copy(accepted = true, phase = "listening")
            overlay.render(OverlayMode.PANEL, accepted, true)
            capture(root, "overlay-listening")
            overlay.touchablePanel(region)
            val listeningHeight = region.bounds.height()
            val close = overlay.descendants().filterIsInstance<Button>().single { it.visibility == View.VISIBLE }
            assertEquals("Close voice panel", close.contentDescription)
            val location = IntArray(2).also { close.getLocationInWindow(it) }
            assertTrue(region.contains(location[0] + close.width / 2, location[1] + close.height / 2))
            assertFalse(region.contains(location[0], location[1] - 500))
            val label = overlay.descendants().filterIsInstance<TextView>().single { it.isShown && it.text == "Listening…" }
            val (brand, phaseIcon) = overlay.descendants().filterIsInstance<ImageView>().toList()
            fun transcriptText() = overlay.descendants().filterIsInstance<ScrollView>().single()
                .descendants().filterIsInstance<TextView>().map { it.text.toString() }.toList()
            assertEquals("Encore Satellite Assistant", brand.contentDescription)
            fun position(view: View) = IntArray(2).also { view.getLocationInWindow(it) }
            assertTrue(position(brand)[1] + brand.height <= position(phaseIcon)[1])
            val textStart = position(label)[0]
            assertTrue(position(phaseIcon)[0] + phaseIcon.width <= textStart)

            overlay.render(OverlayMode.PANEL, accepted.copy(phase = "working"), true)
            layout(root)
            overlay.touchablePanel(region)
            assertEquals("Working without a transcript should stay compact", listeningHeight, region.bounds.height())
            overlay.render(OverlayMode.PANEL, accepted.copy(phase = "working", heard = "Turn on the porch light."), true)
            capture(root, "overlay-working")
            val copy = overlay.descendants().filterIsInstance<TextView>().single { it.isShown && it.text == "Turn on the porch light." }
            assertFalse(label.isShown)
            assertEquals(textStart, position(copy)[0])
            assertTrue(brand.isShown)
            val answered = accepted.copy(phase = "speaking", heard = "Turn on the porch light.", reply = "The porch light is on.")
            overlay.render(OverlayMode.PANEL, answered, true)
            capture(root, "overlay-replying")
            assertEquals(listOf(answered.heard, answered.reply), transcriptText())
            overlay.touchablePanel(region)
            assertTrue("A short exchange should only grow to fit", region.bounds.height() in (listeningHeight + 1)..779)

            val follow = accepted.copy(key = TurnKey(1, 3), invocation = "follow_up")
            overlay.render(OverlayMode.PANEL, follow, true, listOf(answered))
            capture(root, "overlay-follow-up")
            assertEquals(listOf(answered.heard, answered.reply, "Listening…"), transcriptText())
            val second = follow.copy(phase = "speaking", heard = "What about the garage?", reply = "The garage door is closed.")
            overlay.render(OverlayMode.PANEL, second, true, listOf(answered), finished = true)
            capture(root, "overlay-complete")
            assertEquals(listOf(answered.heard, answered.reply, second.heard, second.reply), transcriptText())

            val longReply = (1..25).joinToString("\n") { "$it. Check the porch light and close the garage door." }
            val long = answered.copy(reply = longReply)
            overlay.render(OverlayMode.PANEL, long, true)
            capture(root, "overlay-long-reply")
            overlay.touchablePanel(region)
            assertEquals(780, region.bounds.height())
            val scroll = overlay.descendants().filterIsInstance<ScrollView>().single()
            assertFalse("New transcript text follows the bottom", scroll.canScrollVertically(1))
            val brandTop = position(brand)[1]
            scroll.scrollTo(0, 96)
            overlay.render(OverlayMode.PANEL, follow, true, listOf(long))
            layout(root)
            assertEquals("Follow-up doesn't pull a reader away from history", 96, scroll.scrollY)
            scroll.scrollTo(0, scroll.getChildAt(0).height)
            assertFalse("The final line should be reachable", scroll.canScrollVertically(1))
            assertEquals("Brand stays above the scrolling reply", brandTop, position(brand)[1])
            val closeLocation = IntArray(2).also { close.getLocationInWindow(it) }
            assertTrue(region.contains(closeLocation[0] + close.width / 2, closeLocation[1] + close.height / 2))
            close.performClick()
            assertEquals(1, closes)

            layout(root, height = 640)
            overlay.touchablePanel(region)
            assertEquals("Cap follows the available height", 320, region.bounds.height())
            overlay.render(OverlayMode.UNAVAILABLE, null, false)
            capture(root, "overlay-paused")

            overlay.render(OverlayMode.HIDDEN, null, false)
            assertTrue(transcriptText().isEmpty())
            overlay.render(OverlayMode.PANEL, accepted, true)
            fun listeningLabel() = overlay.descendants().filterIsInstance<TextView>().single { it.isShown && it.text == "Listening…" }
            fun listeningIcon() = overlay.descendants().filterIsInstance<ImageView>().last()
            assertEquals(context.getColor(android.R.color.system_neutral2_700), listeningLabel().currentTextColor)
            assertEquals(PorterDuffColorFilter(context.getColor(android.R.color.system_accent1_700), PorterDuff.Mode.SRC_ATOP),
                listeningIcon().colorFilter)

            // Recolor an existing overlay without changing its active turn or bounds.
            overlay.dispatchConfigurationChanged(Configuration(context.resources.configuration).apply {
                uiMode = (uiMode and Configuration.UI_MODE_NIGHT_MASK.inv()) or Configuration.UI_MODE_NIGHT_YES
            })
            capture(root, "overlay-dark-listening")
            assertEquals(context.getColor(android.R.color.system_neutral2_200), listeningLabel().currentTextColor)
            assertEquals(PorterDuffColorFilter(context.getColor(android.R.color.system_accent1_200), PorterDuff.Mode.SRC_ATOP),
                listeningIcon().colorFilter)
            assertEquals(listeningIcon().colorFilter, brand.colorFilter)
            overlay.touchablePanel(region)
            assertEquals(listeningHeight, region.bounds.height())

            overlay.render(OverlayMode.PANEL, second, true, listOf(answered), finished = true)
            capture(root, "overlay-dark-complete")
            overlay.render(OverlayMode.CUE, candidate, true)
            capture(root, "overlay-dark-cue")
            overlay.render(OverlayMode.UNAVAILABLE, null, false)
            capture(root, "overlay-dark-paused")
        }
    }

    @Test fun pauseAndTalkAreSeparateAndReadinessControlsWhatIsAvailable() {
        Robolectric.buildActivity(MicActivity::class.java).create().use { controller ->
            val activity = controller.get()
            val root = activity.findViewById<View>(android.R.id.content)
            val controls = root.descendants().filterIsInstance<Button>().toList()
            val server = root.descendants().filterIsInstance<EditText>().single()
            val (arm, talk) = controls
            assertEquals("Arm mic", arm.text)
            assertEquals(View.VISIBLE, server.visibility)
            assertEquals(View.GONE, talk.visibility)
            capture(root, "paused")

            val connected = MicState(endpoint = "encore-personal-a1b2c3 · 192.168.1.71:6055", status = "Waiting for Incubator")
            activity.render(connected)
            assertEquals("Pause mic", arm.text)
            assertEquals(View.GONE, server.visibility)
            assertEquals(View.VISIBLE, talk.visibility)
            assertFalse(talk.isEnabled)

            activity.render(connected.copy(canTalk = true, status = "Listening for your wake word"))
            assertEquals("Talk", talk.text)
            assertTrue(talk.isEnabled)
            capture(root, "armed")

            activity.render(connected.copy(active = true, status = "Replying", transcript = "What’s on my list?\n\nYou have two items."))
            assertEquals("Stop", talk.text)
            assertTrue(talk.isEnabled)
            assertEquals("Pause mic", arm.text)
            capture(root, "replying")

            activity.render(MicState())
            assertEquals("Arm mic", arm.text)
            assertEquals(View.GONE, talk.visibility)
            assertFalse(root.descendants().filterIsInstance<TextView>().any { it.text.contains("two items") })

            server.setText("192.168.1.258")
            arm.performClick()
            assertTrue(root.descendants().filterIsInstance<TextView>().any { it.text.startsWith("Enter the Incubator") })
        }
    }

    private fun View.descendants(): Sequence<View> = sequence {
        yield(this@descendants)
        if (this@descendants is ViewGroup) for (i in 0 until childCount) yieldAll(getChildAt(i).descendants())
    }

    private fun layout(root: View, height: Int = 1560) {
        root.measure(View.MeasureSpec.makeMeasureSpec(786, View.MeasureSpec.EXACTLY),
            View.MeasureSpec.makeMeasureSpec(height, View.MeasureSpec.EXACTLY))
        root.layout(0, 0, 786, height)
    }

    private fun capture(root: View, state: String) {
        layout(root)
        val bitmap = Bitmap.createBitmap(786, 1560, Bitmap.Config.ARGB_8888)
        bitmap.eraseColor(0xff121212.toInt())
        root.draw(Canvas(bitmap))
        val path = File("build/reports/screenshots").apply { mkdirs() }.resolve("personal-mic-$state.png")
        path.outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 100, it) }
        bitmap.recycle()
    }
}
