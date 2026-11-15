package com.getan.golder.mobile.androiduse

import android.content.Context
import android.hardware.display.DisplayManager
import android.hardware.display.VirtualDisplay
import android.util.Log
import android.view.Surface
import java.util.concurrent.TimeUnit

@Suppress("PrivateApi")
internal class HelperShell(private val context: Context) : IGolderShell.Stub() {
    private val tag = "GolderHelperShell"
    private val displays = mutableMapOf<Int, VirtualDisplay>()

    override fun createDisplay(surface: Surface?, width: Int, height: Int, dpi: Int): Int {
        if (surface == null || width <= 0 || height <= 0 || dpi <= 0) return -1
        return runCatching {
            val manager = context.getSystemService(DisplayManager::class.java)
            val flags = DisplayManager.VIRTUAL_DISPLAY_FLAG_PUBLIC or
                DisplayManager.VIRTUAL_DISPLAY_FLAG_OWN_CONTENT_ONLY
            val display = manager.createVirtualDisplay("golder-phase2", width, height, dpi, surface, flags, null, null)
            val id = display.display.displayId
            displays[id] = display
            id
        }.onFailure { Log.e(tag, "createDisplay failed", it) }.getOrDefault(-1)
    }

    override fun releaseDisplay(displayId: Int) {
        displays.remove(displayId)?.release()
    }

    override fun startApp(displayId: Int, packageName: String): Int {
        if (!displays.containsKey(displayId) || packageName.isBlank()) return -1
        val started = runCommand(
            "am", "start", "--display", displayId.toString(),
            "-a", "android.intent.action.MAIN",
            "-c", "android.intent.category.LAUNCHER",
            "-p", packageName,
        )
        return if (started) displayId else -1
    }

    override fun injectTap(displayId: Int, x: Int, y: Int): Boolean {
        if (!displays.containsKey(displayId)) return false
        return runCommand("input", "-d", displayId.toString(), "tap", x.toString(), y.toString())
    }

    override fun injectKey(displayId: Int, keyCode: Int): Boolean {
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
        displays.values.forEach { runCatching { it.release() } }
        displays.clear()
    }

    override fun destroy() {
        releaseAll()
        android.os.Process.killProcess(android.os.Process.myPid())
    }
}
