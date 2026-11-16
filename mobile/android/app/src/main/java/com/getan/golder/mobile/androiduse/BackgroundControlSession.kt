package com.getan.golder.mobile.androiduse

import android.content.Context
import org.json.JSONObject

/**
 * Narrow executor for the Go-owned background-control policy. The Go tool decides when a
 * session may start; this adapter only holds the Surface/ImageReader and calls
 * the restricted shell helper.
 */
internal object Phase2Session {
    private var display: Phase2Display? = null
    private var packageName: String = ""

    @Synchronized
    fun status(context: Context): JSONObject = status(context, "phase2_status")

    @Synchronized
    fun start(context: Context): JSONObject {
        if (!AndroidUseBridge.advancedEnabled(context)) {
            return error("advanced control is disabled in Golder settings")
        }
        if (!Phase2Controller.isPaired(context)) {
            return error("wireless debugging is not paired")
        }
        if (Phase2Controller.shell() == null) {
            val started = Phase2Controller.startAutomatically(context)
            if (started.isFailure) {
                return error(started.exceptionOrNull()?.message ?: "helper start failed")
            }
        }
        return status(context, "phase2_start")
    }

    @Synchronized
    fun openApp(context: Context, pkg: String): JSONObject {
        if (!AndroidUseBridge.advancedEnabled(context)) {
            return error("advanced control is disabled in Golder settings")
        }
        val shell = Phase2Controller.shell() ?: return error("helper is not connected; call background_start")
        if (!packagePattern.matches(pkg)) {
            return error("packageName is not valid")
        }
        val allowed = AndroidUseBridge.allowedPackages(context)
        if (allowed.isNotEmpty() && pkg !in allowed) {
            return error("$pkg is not in the AndroidUse allowlist")
        }
        var created = false
        var current = display
        if (current == null) {
            val metrics = context.resources.displayMetrics
            current = Phase2Display(shell, metrics.widthPixels, metrics.heightPixels, metrics.densityDpi)
            if (current.displayId < 0) {
                current.close()
                return error("virtual display creation failed")
            }
            display = current
            created = true
        }
        val started = runCatching {
            shell.startApp(current.displayId, pkg)
        }.getOrElse { -1 }
        if (started < 0) {
            if (created) {
                display?.close()
                display = null
            }
            if (started == -2) {
                return error("app already has a task on another display; background control will not move or close it")
            }
            return error("app launch on the virtual display failed")
        }
        packageName = pkg
        return status(context, "phase2_open_app").put("started", true)
    }

    @Synchronized
    fun tap(context: Context, x: Int, y: Int): JSONObject {
        val shell = Phase2Controller.shell() ?: return error("helper is not connected")
        val current = display ?: return error("no virtual display; call background_open_app first")
        if (x < 0 || y < 0) return error("x and y are required")
        val injected = runCatching { shell.injectTap(current.displayId, x, y) }.getOrDefault(false)
        if (!injected) return error("tap injection failed")
        return status(context, "phase2_tap").put("x", x).put("y", y)
    }

    @Synchronized
    fun release(context: Context): JSONObject {
        display?.close()
        display = null
        packageName = ""
        return status(context, "phase2_release")
    }

    @Synchronized
    fun stop(context: Context): JSONObject {
        display?.close()
        display = null
        packageName = ""
        Phase2Controller.stop(context)
        return status(context, "phase2_stop")
    }

    private fun status(context: Context, action: String): JSONObject = JSONObject()
        .put("ok", true)
        .put("action", action)
        .put("advanced", AndroidUseBridge.advancedEnabled(context))
        .put("paired", Phase2Controller.isPaired(context))
        .put("helperConnected", Phase2Controller.shell() != null)
        .put("displayId", display?.displayId ?: -1)
        .put("package", packageName)

    private fun error(message: String): JSONObject =
        JSONObject().put("ok", false).put("error", message)

    private val packagePattern = Regex("^[A-Za-z0-9_]+(\\.[A-Za-z0-9_]+)+$")
}
