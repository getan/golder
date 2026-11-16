package com.getan.golder.mobile.androiduse

import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import android.os.ParcelFileDescriptor
import java.io.FileInputStream
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
                assertTrue("tap injection failed", shell.injectTap(display.displayId, 100, 100))
                assertTrue("virtual display produced no frame", display.waitForFrame(5_000))
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
