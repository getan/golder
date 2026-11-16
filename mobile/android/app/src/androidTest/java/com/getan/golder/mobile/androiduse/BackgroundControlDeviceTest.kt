package com.getan.golder.mobile.androiduse

import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import android.graphics.Color
import android.os.ParcelFileDescriptor
import android.os.SystemClock
import android.provider.Settings
import java.io.FileInputStream
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Test
import org.junit.runner.RunWith

/** Opt-in device check using an isolated activity from the test APK. */
@RunWith(AndroidJUnit4::class)
class BackgroundControlDeviceTest {
    /**
     * With wireless debugging off the phone keeps advertising a stale port, so
     * the failure used to surface as a bare "connection refused" from the helper.
     */
    @Test(timeout = 60_000)
    fun startExplainsThatWirelessDebuggingIsOff() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val enabled = Settings.Global.getInt(context.contentResolver, "adb_wifi_enabled", 0) == 1
        assumeTrue("wireless debugging is enabled; needs it off to test this branch", !enabled)
        val result = Phase2Controller.startAutomatically(context)
        assertTrue("start unexpectedly succeeded without wireless debugging", result.isFailure)
        val message = result.exceptionOrNull()?.message.orEmpty()
        assertTrue("unclear failure: $message", message.contains("wireless debugging", ignoreCase = true))
        assertFalse("stale-port dial error leaked: $message", message.contains("connection refused"))
    }

    @Test(timeout = 90_000)
    fun isolatesActivityAndPreservesMainDisplay() {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val context = instrumentation.targetContext
        val testPackage = instrumentation.context.packageName
        val before = displaySection(activityDump(), 0)
        assertTrue("main display section not found", before.isNotBlank())
        assertFalse("test activity already has a main-screen task", before.contains(testPackage))
        val beforeTop = topResumed(before)
        assertNotNull("main-screen foreground could not be determined", beforeTop)
        val started = Phase2Controller.startAutomatically(context)
        assertTrue("background helper failed: ${started.exceptionOrNull()?.message}", started.isSuccess)
        try {
            val shell = Phase2Controller.shell()
            assertNotNull("background helper is not connected", shell)
            Phase2Display(shell!!, 320, 480, 160).use { display ->
                assertTrue("virtual display creation failed", display.displayId > 0)
                assertEquals("test activity failed to launch on the virtual display", display.displayId, shell.startApp(display.displayId, testPackage))
                BackgroundPreview.begin(display, display.displayId, testPackage, display.width, display.height)
                display.startPreview { frame -> BackgroundPreview.publish(display, frame) }
                assertTrue("test activity did not draw its first frame", awaitPreviewColor(display.displayId, red = true))
                assertTrue("tap injection failed", shell.injectTap(display.displayId, 100, 100))
                val during = activityDump()
                assertTrue("test activity is not on the virtual display", displaySection(during, display.displayId).contains(testPackage))
                val main = displaySection(during, 0)
                assertFalse("test activity appeared on the main display", main.contains(testPackage))
                assertEquals("main-screen foreground changed", beforeTop, topResumed(main))
            }
            Thread.sleep(1_000)
            val after = displaySection(activityDump(), 0)
            assertTrue("main display section disappeared", after.isNotBlank())
            assertFalse("test activity migrated to the main display when released", after.contains(testPackage))
            assertEquals("main-screen foreground changed after release", beforeTop, topResumed(after))
        } finally {
            Phase2Controller.stop(context)
        }
    }

    @Test(timeout = 90_000)
    fun doesNotMoveExistingMainScreenTask() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val before = displaySection(activityDump(), 0)
        val packageName = "com.getan.snake"
        assumeTrue("no existing test-game task on the main display", before.contains(packageName))
        val beforeTop = topResumed(before)
        assertNotNull("main-screen foreground could not be determined", beforeTop)
        val started = Phase2Controller.startAutomatically(context)
        assertTrue("background helper failed: ${started.exceptionOrNull()?.message}", started.isSuccess)
        try {
            val shell = Phase2Controller.shell()
            assertNotNull(shell)
            Phase2Display(shell!!, 320, 480, 160).use { display ->
                assertTrue("virtual display creation failed", display.displayId > 0)
                assertEquals("existing task must not be moved", -2, shell.startApp(display.displayId, packageName))
                val after = displaySection(activityDump(), 0)
                assertTrue("existing task disappeared from the main display", after.contains(packageName))
                assertEquals("main-screen foreground changed", beforeTop, topResumed(after))
            }
        } finally {
            Phase2Controller.stop(context)
        }
    }

    @Test(timeout = 90_000)
    fun previewUpdatesAndClearsWhenDisplayIsReleased() {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val context = instrumentation.targetContext
        val testPackage = instrumentation.context.packageName
        val started = Phase2Session.start(context)
        assertTrue("background helper failed: $started", started.optBoolean("ok"))
        try {
            val opened = Phase2Session.openApp(context, testPackage)
            assertTrue("test activity failed to launch: $opened", opened.optBoolean("ok"))
            val displayId = opened.optInt("displayId", -1)
            assertTrue("invalid virtual display", displayId > 0)
            val red = awaitPreviewColor(displayId, red = true)
            assertTrue("preview never showed the initial red frame: ${previewDescription()}", red)
            val green = awaitPreviewColor(displayId, red = false)
            assertTrue("preview never updated to the green frame: ${previewDescription()}", green)
            assertTrue(Phase2Session.release(context).optBoolean("ok"))
            assertEquals("preview should clear on release", null, BackgroundPreview.state.value)
            Thread.sleep(500)
            assertEquals("a late frame restored a released preview", null, BackgroundPreview.state.value)
        } finally {
            Phase2Session.stop(context)
        }
    }

    @Test(timeout = 90_000)
    fun previewClearsWhenHelperExits() {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val context = instrumentation.targetContext
        val started = Phase2Session.start(context)
        assertTrue("background helper failed: $started", started.optBoolean("ok"))
        try {
            val opened = Phase2Session.openApp(context, instrumentation.context.packageName)
            assertTrue("test activity failed to launch: $opened", opened.optBoolean("ok"))
            val displayId = opened.optInt("displayId", -1)
            assertTrue("preview did not receive a test frame", awaitPreviewColor(displayId, red = true))
            val shell = Phase2Controller.shell()
            assertNotNull("helper disconnected before the test", shell)
            runCatching { shell!!.destroy() }
            val deadline = SystemClock.uptimeMillis() + 5_000
            while (SystemClock.uptimeMillis() < deadline &&
                (Phase2Controller.shell() != null || BackgroundPreview.state.value != null)
            ) Thread.sleep(100)
            assertEquals("dead helper remained registered", null, Phase2Controller.shell())
            assertEquals("preview remained visible after helper exit", null, BackgroundPreview.state.value)
        } finally {
            Phase2Session.stop(context)
        }
    }

    @Test(timeout = 90_000)
    fun manualTakeoverDeliversGesturesAndRequiresFreshObservation() {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val context = instrumentation.targetContext
        val mainBefore = topResumed(displaySection(activityDump(), 0))
        assertNotNull("main-screen foreground could not be determined", mainBefore)
        val started = Phase2Session.start(context)
        assertTrue("background helper failed: $started", started.optBoolean("ok"))
        var manual = false
        try {
            val opened = Phase2Session.openApp(context, instrumentation.context.packageName)
            assertTrue("test activity failed to launch: $opened", opened.optBoolean("ok"))
            val id = opened.optInt("displayId", -1)
            assertTrue("background frame did not arrive", awaitPreviewColor(id, red = true))
            val preview = BackgroundPreview.state.value!!
            val x = preview.width / 2
            val y = preview.height / 2
            assertTrue("manual handoff failed", Phase2Session.beginManual(context, id))
            manual = true
            val modelTap = JSONObject().put("action", "phase2_tap")
            assertFalse("agent could act during takeover", Phase2Session.guardAgentRequest(modelTap) {
                Phase2Session.tap(context, x, y)
            }.optBoolean("ok"))
            assertFalse("agent could resume during takeover", Phase2Session.guardAgentRequest(JSONObject().put("action", "resume")) {
                JSONObject().put("ok", true)
            }.optBoolean("ok"))
            assertFalse("input was accepted on a different display", Phase2Session.manualStroke(context, id + 1,
                intArrayOf(x, x), intArrayOf(y, y), longArrayOf(0, 50)))
            assertTrue("manual tap injection failed", Phase2Session.manualStroke(context, id,
                intArrayOf(x, x), intArrayOf(y, y), longArrayOf(0, 50)))
            assertTrue("tap was not delivered to the test activity: ${previewDescription()}", awaitCenterColor(id, Color.BLUE))
            assertTrue("manual swipe injection failed", Phase2Session.manualStroke(context, id,
                intArrayOf(x, x, x), intArrayOf(y, y + 80, y + 160), longArrayOf(0, 120, 240)))
            assertTrue("swipe was not delivered to the test activity: ${previewDescription()}", awaitCenterColor(id, Color.YELLOW))
            assertEquals("manual input changed the main-screen foreground", mainBefore, topResumed(displaySection(activityDump(), 0)))

            Phase2Session.endManual()
            manual = false
            assertFalse("old action was accepted after handback", Phase2Session.guardAgentRequest(modelTap) {
                Phase2Session.tap(context, x, y)
            }.optBoolean("ok"))
            val observed = Phase2Session.guardAgentRequest(JSONObject().put("action", "phase2_observe")) {
                Phase2Session.observe(context)
            }
            assertTrue("fresh background observation failed: $observed", observed.optBoolean("ok"))
            assertTrue("background observation did not include a frame", observed.optString("imageBase64").isNotEmpty())
            val snapshot = observed.optString("snapshotId")
            assertTrue("background observation did not include a snapshot", snapshot.isNotEmpty())
            assertFalse("wrong snapshot was accepted", Phase2Session.guardAgentRequest(modelTap.put("snapshotId", "old")) {
                Phase2Session.tap(context, x, y)
            }.optBoolean("ok"))
            assertTrue("freshly observed action failed", Phase2Session.guardAgentRequest(modelTap.put("snapshotId", snapshot)) {
                Phase2Session.tap(context, x, y)
            }.optBoolean("ok"))
            assertTrue("fresh action was not delivered", awaitCenterColor(id, Color.BLUE))
        } finally {
            if (manual) Phase2Session.endManual()
            Phase2Session.stop(context)
        }
    }

    private fun awaitCenterColor(displayId: Int, expected: Int): Boolean {
        val deadline = SystemClock.uptimeMillis() + 8_000
        while (SystemClock.uptimeMillis() < deadline) {
            val preview = BackgroundPreview.state.value
            val frame = preview?.frame
            if (preview?.displayId == displayId && frame != null) {
                val color = frame.getPixel(frame.width / 2, frame.height / 2)
                if (kotlin.math.abs(Color.red(color) - Color.red(expected)) < 40 &&
                    kotlin.math.abs(Color.green(color) - Color.green(expected)) < 40 &&
                    kotlin.math.abs(Color.blue(color) - Color.blue(expected)) < 40
                ) return true
            }
            Thread.sleep(100)
        }
        return false
    }

    private fun awaitPreviewColor(displayId: Int, red: Boolean): Boolean {
        val deadline = SystemClock.uptimeMillis() + 8_000
        while (SystemClock.uptimeMillis() < deadline) {
            val preview = BackgroundPreview.state.value
            val frame = preview?.frame
            if (preview?.displayId == displayId && frame != null) {
                val color = frame.getPixel(frame.width / 2, frame.height / 2)
                if (red && Color.red(color) > 200 && Color.green(color) < 80) return true
                if (!red && Color.green(color) > 200 && Color.red(color) < 80) return true
            }
            Thread.sleep(100)
        }
        return false
    }

    private fun previewDescription(): String {
        val state = BackgroundPreview.state.value ?: return "no preview state"
        val frame = state.frame ?: return "display=${state.displayId}, no frame"
        val color = frame.getPixel(frame.width / 2, frame.height / 2)
        return "display=${state.displayId}, frame=${frame.width}x${frame.height}, center=${Integer.toHexString(color)}"
    }

    private fun activityDump(): String {
        val descriptor: ParcelFileDescriptor = InstrumentationRegistry.getInstrumentation()
            .uiAutomation.executeShellCommand("dumpsys activity activities")
        return descriptor.use { FileInputStream(it.fileDescriptor).bufferedReader().readText() }
    }

    private fun displaySection(dump: String, displayId: Int): String {
        val start = Regex("(?m)^\\s*Display #$displayId \\(activities from top to bottom\\):")
            .find(dump) ?: return ""
        val next = Regex("(?m)^\\s*Display #[0-9]+ \\(activities from top to bottom\\):")
            .find(dump, start.range.last + 1)
        return dump.substring(start.range.first, next?.range?.first ?: dump.length)
    }

    private fun topResumed(section: String): String? =
        Regex("(?m)^\\s*topResumedActivity=ActivityRecord\\{[^}]*? ([^ ]+/[^ ]+) ")
            .find(section)?.groupValues?.get(1)
}
