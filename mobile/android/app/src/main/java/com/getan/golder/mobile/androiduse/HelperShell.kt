package com.getan.golder.mobile.androiduse

import android.content.ComponentName
import android.content.Context
import android.hardware.display.DisplayManager
import android.hardware.display.VirtualDisplay
import android.os.Build
import android.os.SystemClock
import android.util.Log
import android.view.Display
import android.view.Surface
import java.util.concurrent.TimeUnit

@Suppress("PrivateApi")
internal class HelperShell(private val context: Context) : IGolderShell.Stub() {
    private val tag = "GolderHelperShell"
    private val displays = mutableMapOf<Int, VirtualDisplay>()
    private val launchLock = Any()

    private companion object {
        const val SUPPORTS_TOUCH = 1 shl 6
        const val ROTATES_WITH_CONTENT = 1 shl 7
        const val DESTROY_CONTENT_ON_REMOVAL = 1 shl 8
        const val TRUSTED = 1 shl 10
        const val OWN_DISPLAY_GROUP = 1 shl 11
        const val ALWAYS_UNLOCKED = 1 shl 12
        const val TOUCH_FEEDBACK_DISABLED = 1 shl 13
        const val OWN_FOCUS = 1 shl 14
        const val STEAL_TOP_FOCUS_DISABLED = 1 shl 16
    }

    private data class TaskLocation(val displayId: Int, val packages: Set<String>)

    private fun taskLocations(): List<TaskLocation> {
        val taskManager = Class.forName("android.app.ActivityTaskManager")
            .getMethod("getService").invoke(null)
        val methods = taskManager.javaClass.methods.filter { it.name == "getTasks" }
        val withDisplay = methods.firstOrNull { it.parameterTypes.size == 4 }
        val result = if (withDisplay != null) {
            withDisplay.invoke(taskManager, 512, false, false, Display.INVALID_DISPLAY)
        } else {
            methods.first { it.parameterTypes.size == 3 }.invoke(taskManager, 512, false, false)
        } as List<*>
        check(result.size < 512) { "task list was truncated" }
        val taskInfo = Class.forName("android.app.TaskInfo")
        val displayField = taskInfo.getField("displayId")
        val baseField = taskInfo.getField("baseActivity")
        val topField = taskInfo.getField("topActivity")
        return result.filterNotNull().map { task ->
            val packages = listOf(baseField.get(task), topField.get(task))
                .filterIsInstance<ComponentName>().map { it.packageName }.toSet()
            check(packages.isNotEmpty()) { "task has no identifiable activity" }
            TaskLocation(
                displayField.getInt(task),
                packages,
            )
        }
    }

    override fun createDisplay(surface: Surface?, width: Int, height: Int, dpi: Int): Int = synchronized(launchLock) {
        if (surface == null || width <= 0 || height <= 0 || dpi <= 0) return -1
        return runCatching {
            check(Build.VERSION.SDK_INT >= Build.VERSION_CODES.UPSIDE_DOWN_CAKE) {
                "background display isolation requires Android 14 or later"
            }
            val manager = context.getSystemService(DisplayManager::class.java)
            val flags = DisplayManager.VIRTUAL_DISPLAY_FLAG_PUBLIC or
                DisplayManager.VIRTUAL_DISPLAY_FLAG_OWN_CONTENT_ONLY or
                SUPPORTS_TOUCH or ROTATES_WITH_CONTENT or DESTROY_CONTENT_ON_REMOVAL or
                TRUSTED or OWN_DISPLAY_GROUP or ALWAYS_UNLOCKED or
                TOUCH_FEEDBACK_DISABLED or OWN_FOCUS or STEAL_TOP_FOCUS_DISABLED
            // Independent focus protects the physical screen; destroy-on-removal
            // prevents virtual-display tasks from being moved to that screen.
            val display = manager.createVirtualDisplay("golder-background", width, height, dpi, surface, flags, null, null)
            val id = display.display.displayId
            displays[id] = display
            id
        }.onFailure { Log.e(tag, "createDisplay failed", it) }.getOrDefault(-1)
    }

    override fun releaseDisplay(displayId: Int) {
        synchronized(launchLock) { displays.remove(displayId)?.release() }
    }

    override fun startApp(displayId: Int, packageName: String): Int = synchronized(launchLock) {
        if (!displays.containsKey(displayId) || packageName.isBlank()) return -1
        val before = try {
            taskLocations()
        } catch (e: Exception) {
            Log.e(tag, "cannot verify existing tasks", e)
            return -1
        }
        // Never move or close an existing task belonging to the user.
        if (before.any { it.displayId != displayId && packageName in it.packages }) {
            return -2
        }
        // Launcher activities do not necessarily declare CATEGORY_DEFAULT, so
        // an implicit `am start` can fail even when the launcher finds the app.
        val component = context.packageManager.getLaunchIntentForPackage(packageName)?.component
        if (component == null || component.packageName != packageName) {
            Log.e(tag, "no launcher activity for $packageName")
            return -1
        }
        val started = runCommand(
            "am", "start", "--display", displayId.toString(),
            "-n", component.flattenToString(),
            "-a", "android.intent.action.MAIN", "-c", "android.intent.category.LAUNCHER",
        )
        if (!started) return -1
        val deadline = SystemClock.uptimeMillis() + 2_000
        do {
            val tasks = try {
                taskLocations()
            } catch (e: Exception) {
                Log.e(tag, "cannot verify launched task", e)
                return -1
            }
            if (tasks.any { packageName in it.packages && it.displayId != displayId }) return -1
            if (tasks.any { packageName in it.packages && it.displayId == displayId }) return displayId
            SystemClock.sleep(100)
        } while (SystemClock.uptimeMillis() < deadline)
        return -1
    }

    override fun injectTap(displayId: Int, x: Int, y: Int): Boolean = synchronized(launchLock) {
        if (!displays.containsKey(displayId)) return false
        return runCommand("input", "-d", displayId.toString(), "tap", x.toString(), y.toString())
    }

    override fun injectKey(displayId: Int, keyCode: Int): Boolean = synchronized(launchLock) {
        if (!displays.containsKey(displayId)) return false
        return runCommand("input", "-d", displayId.toString(), "keyevent", keyCode.toString())
    }

    private fun runCommand(vararg args: String): Boolean = runCatching {
        val process = ProcessBuilder(*args).redirectErrorStream(true).start()
        val output = process.inputStream.bufferedReader().readText()
        val finished = process.waitFor(3, TimeUnit.SECONDS)
        val ok = finished && process.exitValue() == 0 && !output.contains("Error")
        if (!ok) Log.e(tag, "command failed: ${args.joinToString(" ")} output=$output")
        ok
    }.onFailure {
        Log.e(tag, "command failed: ${args.joinToString(" ")}", it)
    }.getOrDefault(false)

    override fun releaseAll() {
        synchronized(launchLock) {
            displays.values.forEach { runCatching { it.release() } }
            displays.clear()
        }
    }

    override fun destroy() {
        releaseAll()
        android.os.Process.killProcess(android.os.Process.myPid())
    }
}
