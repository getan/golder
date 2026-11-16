package com.getan.golder.mobile.androiduse

import android.accessibilityservice.AccessibilityServiceInfo
import android.graphics.Color
import android.graphics.Rect
import android.os.SystemClock
import android.util.Log
import android.view.accessibility.AccessibilityNodeInfo
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.getan.golder.mobile.SettingsStore
import java.io.FileInputStream
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

/** Exercises the in-app thumbnail and expanded preview using the test display. */
@RunWith(AndroidJUnit4::class)
class BackgroundPreviewUiTest {
    @Test(timeout = 90_000)
    fun previewCanBeOpenedAndDisappearsOnRelease() {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val context = instrumentation.targetContext
        val chinese = SettingsStore.load(context).language == "zh"
        val thumbnailText = if (chinese) "打开后台实时画面" else "Open live background display"
        val dialogText = if (chinese) "后台实时画面" else "Live background display"
        val takeoverText = if (chinese) "接管操作" else "Take over"
        val handbackText = if (chinese) "交还 Agent" else "Hand back"
        val imageText = if (chinese) "可操作的后台画面" else "Interactive background display"
        val automation = instrumentation.uiAutomation
        val serviceInfo = automation.serviceInfo
        serviceInfo.flags = serviceInfo.flags or AccessibilityServiceInfo.FLAG_RETRIEVE_INTERACTIVE_WINDOWS
        automation.serviceInfo = serviceInfo
        instrumentation.uiAutomation.executeShellCommand(
            "am start -n com.getan.golder.mobile/.MainActivity",
        ).close()
        assertTrue("Golder did not become the active window; ${windowSummary()}", waitForGolderWindow())
        val start = Phase2Session.start(context)
        assertTrue("background helper failed: $start", start.optBoolean("ok"))
        try {
            val opened = Phase2Session.openApp(context, instrumentation.context.packageName)
            assertTrue("test activity failed to launch: $opened", opened.optBoolean("ok"))
            assertTrue("background launch changed the main-screen foreground; ${windowSummary()}", mainForeground()?.startsWith("com.getan.golder.mobile/") == true)
            val holdMs = InstrumentationRegistry.getArguments().getString("holdPreviewMs")?.toLongOrNull() ?: 0L
            if (holdMs > 0) {
                Log.i("GolderPreviewUiTest", "preview fixture opened")
                Thread.sleep(holdMs.coerceAtMost(20_000))
            }
            val thumbnail = waitForText(thumbnailText)
            assertNotNull("background preview thumbnail did not appear; ${windowSummary()}", thumbnail)
            var clickable = thumbnail
            while (clickable != null && !clickable.isClickable) clickable = clickable.parent
            assertNotNull("preview thumbnail is not clickable", clickable)
            assertTrue("could not expand preview", clickable!!.performAction(AccessibilityNodeInfo.ACTION_CLICK))
            assertNotNull("expanded preview did not appear", waitForText(dialogText))
            val takeOver = waitForClickable(takeoverText)
            assertNotNull("takeover button did not become available", takeOver)
            assertTrue("could not request takeover", takeOver!!.performAction(AccessibilityNodeInfo.ACTION_CLICK))
            assertTrue("takeover was not acquired: ${ManualTakeover.state.value}", waitForStage(ManualTakeover.Stage.ACTIVE))
            val image = waitForText(imageText)
            assertNotNull("interactive preview did not appear", image)
            val bounds = Rect()
            image!!.getBoundsInScreen(bounds)
            instrumentation.uiAutomation.executeShellCommand("input -d 0 tap ${bounds.centerX()} ${bounds.centerY()}").close()
            assertTrue("tap on preview did not reach the background activity", waitForBlueFrame())
            // The UI deliberately holds handback briefly while the frame catches up.
            Thread.sleep(400)
            val handBack = waitForClickable(handbackText)
            assertNotNull("handback button did not become available", handBack)
            handBack!!.getBoundsInScreen(bounds)
            instrumentation.uiAutomation.executeShellCommand("input -d 0 tap ${bounds.centerX()} ${bounds.centerY()}").close()
            assertTrue("handback did not complete", waitForStage(ManualTakeover.Stage.IDLE))
            assertTrue("handback did not invalidate the previous observation", Phase2Session.status(context).optBoolean("observationRequired"))
            assertTrue("preview interaction changed the main-screen foreground", mainForeground()?.startsWith("com.getan.golder.mobile/") == true)
            assertTrue(Phase2Session.release(context).optBoolean("ok"))
            assertTrue("preview dialog remained after release", waitUntilGone(dialogText))
        } finally {
            if (ManualTakeover.state.value.stage == ManualTakeover.Stage.ACTIVE) {
                ManualTakeover.handBack()
                waitForStage(ManualTakeover.Stage.IDLE)
            } else if (ManualTakeover.state.value.stage == ManualTakeover.Stage.WAITING) {
                ManualTakeover.cancelWaiting()
                waitForStage(ManualTakeover.Stage.IDLE)
            }
            Phase2Session.stop(context)
        }
    }

    private fun waitForClickable(text: String): AccessibilityNodeInfo? {
        val deadline = SystemClock.uptimeMillis() + 8_000
        while (SystemClock.uptimeMillis() < deadline) {
            var node = golderRoots().firstNotNullOfOrNull { findNode(it, text) }
            while (node != null && !node.isClickable) node = node.parent
            if (node?.isEnabled == true) return node
            Thread.sleep(100)
        }
        return null
    }

    private fun waitForStage(stage: ManualTakeover.Stage): Boolean {
        val deadline = SystemClock.uptimeMillis() + 8_000
        while (SystemClock.uptimeMillis() < deadline) {
            if (ManualTakeover.state.value.stage == stage) return true
            Thread.sleep(100)
        }
        return false
    }

    private fun waitForBlueFrame(): Boolean {
        val deadline = SystemClock.uptimeMillis() + 8_000
        while (SystemClock.uptimeMillis() < deadline) {
            val frame = BackgroundPreview.state.value?.frame
            if (frame != null) {
                val color = frame.getPixel(frame.width / 2, frame.height / 2)
                if (Color.blue(color) > 200 && Color.red(color) < 80 && Color.green(color) < 80) return true
            }
            Thread.sleep(100)
        }
        return false
    }

    private fun waitForText(text: String): AccessibilityNodeInfo? {
        val deadline = SystemClock.uptimeMillis() + 8_000
        while (SystemClock.uptimeMillis() < deadline) {
            val match = golderRoots().firstNotNullOfOrNull { findNode(it, text) }
            if (match != null) return match
            Thread.sleep(100)
        }
        return null
    }

    private fun waitForGolderWindow(): Boolean {
        val deadline = SystemClock.uptimeMillis() + 8_000
        while (SystemClock.uptimeMillis() < deadline) {
            if (golderRoots().isNotEmpty() && mainForeground()?.startsWith("com.getan.golder.mobile/") == true) return true
            Thread.sleep(100)
        }
        return false
    }

    private fun waitUntilGone(text: String): Boolean {
        val deadline = SystemClock.uptimeMillis() + 4_000
        while (SystemClock.uptimeMillis() < deadline) {
            if (golderRoots().all { findNode(it, text) == null }) return true
            Thread.sleep(100)
        }
        return false
    }

    private fun golderRoots(): List<AccessibilityNodeInfo> =
        InstrumentationRegistry.getInstrumentation().uiAutomation.windows
            .filter { it.displayId == 0 }
            .mapNotNull { it.root }
            .filter { it.packageName?.toString() == "com.getan.golder.mobile" }

    private fun findNode(root: AccessibilityNodeInfo, text: String): AccessibilityNodeInfo? {
        val nodes = ArrayDeque<AccessibilityNodeInfo>()
        nodes.add(root)
        var visited = 0
        while (nodes.isNotEmpty() && visited++ < 5_000) {
            val node = nodes.removeFirst()
            if (node.text?.toString()?.contains(text) == true || node.contentDescription?.toString()?.contains(text) == true) {
                return node
            }
            for (i in 0 until node.childCount) node.getChild(i)?.let(nodes::add)
        }
        return null
    }

    private fun windowSummary(): String {
        val windows = InstrumentationRegistry.getInstrumentation().uiAutomation.windows
        return "windows=${windows.map { "${it.displayId}:${it.root?.packageName}:active=${it.isActive}:focused=${it.isFocused}" }}, " +
            "mainForeground=${mainForeground()}, " +
            "previewActive=${BackgroundPreview.state.value != null}, " +
            "frameReady=${BackgroundPreview.state.value?.frame != null}"
    }

    private fun mainForeground(): String? {
        val descriptor = InstrumentationRegistry.getInstrumentation().uiAutomation
            .executeShellCommand("dumpsys activity activities")
        val dump = descriptor.use { FileInputStream(it.fileDescriptor).bufferedReader().readText() }
        val section = dump.substringAfter("Display #0 (activities from top to bottom):", "")
            .substringBefore("Display #1 (activities from top to bottom):")
        return Regex("topResumedActivity=ActivityRecord\\{[^}]*? ([^ ]+/[^ ]+) ")
            .find(section)?.groupValues?.get(1)
    }
}
