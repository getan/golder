package com.getan.golder.mobile.androiduse

import android.content.Context
import android.graphics.Bitmap
import android.util.Base64
import android.view.KeyEvent
import java.io.ByteArrayOutputStream
import java.util.UUID
import org.json.JSONObject

/**
 * Narrow executor for the Go-owned background-control policy. The Go tool decides when a
 * session may start; this adapter only holds the Surface/ImageReader and calls
 * the restricted shell helper.
 */
internal object Phase2Session {
    private var display: Phase2Display? = null
    private var packageName: String = ""
    private var manualDisplayId: Int = -1
    private var observationRequired = false
    private var handbackSnapshot = ""

    /** Serializes model requests against the local, person-owned handoff. */
    @Synchronized
    fun guardAgentRequest(request: JSONObject, block: () -> JSONObject): JSONObject {
        val action = request.optString("action")
        if (manualDisplayId >= 0 && action != "status" && action != "phase2_status") {
            return error("the person has taken over the background display; wait for handback")
        }
        val needsSnapshot = observationRequired && action in setOf("phase2_open_app", "phase2_tap", "phase2_release", "phase2_stop")
        if (needsSnapshot && (handbackSnapshot.isEmpty() || request.optString("snapshotId") != handbackSnapshot)) {
            return error("the person changed the background display; call background_observe and use its snapshotId before acting")
        }
        val result = block()
        if (needsSnapshot && result.optBoolean("ok")) {
            observationRequired = false
            handbackSnapshot = ""
            result.put("observationRequired", false)
        }
        return result
    }

    @Synchronized
    fun beginManual(context: Context, expectedDisplayId: Int): Boolean {
        val current = display ?: return false
        if (manualDisplayId >= 0 || current.displayId != expectedDisplayId ||
            !AndroidUseBridge.advancedEnabled(context) || Phase2Controller.shell() == null
        ) return false
        manualDisplayId = expectedDisplayId
        GolderAccessibilityService.instance?.invalidateObservation()
        return true
    }

    @Synchronized
    fun endManual() {
        GolderAccessibilityService.instance?.invalidateObservation()
        observationRequired = display != null
        handbackSnapshot = ""
        manualDisplayId = -1
    }

    @Synchronized
    fun manualStroke(context: Context, displayId: Int, xs: IntArray, ys: IntArray, times: LongArray): Boolean {
        if (manualDisplayId != displayId || display?.displayId != displayId ||
            !AndroidUseBridge.advancedEnabled(context)
        ) return false
        return runCatching { Phase2Controller.shell()?.injectStroke(displayId, xs, ys, times) == true }
            .getOrDefault(false)
    }

    @Synchronized
    fun manualBack(context: Context, displayId: Int): Boolean {
        if (manualDisplayId != displayId || display?.displayId != displayId ||
            !AndroidUseBridge.advancedEnabled(context)
        ) return false
        return runCatching { Phase2Controller.shell()?.injectKey(displayId, KeyEvent.KEYCODE_BACK) == true }
            .getOrDefault(false)
    }

    @Synchronized
    fun status(context: Context): JSONObject = status(context, "phase2_status")

    @Synchronized
    fun observe(context: Context): JSONObject {
        val current = display ?: return error("no background display is open")
        val preview = BackgroundPreview.state.value
        val frame = preview?.takeIf { it.displayId == current.displayId }?.frame
            ?: return error("background frame is not available yet")
        val bytes = ByteArrayOutputStream()
        if (!frame.compress(Bitmap.CompressFormat.PNG, 100, bytes)) return error("background frame encoding failed")
        val image = bytes.toByteArray()
        if (image.isEmpty() || image.size > 1_200_000) return error("background frame is too large")
        handbackSnapshot = UUID.randomUUID().toString()
        return status(context, "phase2_observe")
            .put("width", current.width)
            .put("height", current.height)
            .put("snapshotId", handbackSnapshot)
            .put("imageBase64", Base64.encodeToString(image, Base64.NO_WRAP))
    }

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
                return error(
                    "$pkg already has a task on another display. Background control will not move or close an app the " +
                        "user already has open. Ask the user to close it from recents, or offer to operate the visible " +
                        "screen instead.",
                )
            }
            return error("app launch on the virtual display failed")
        }
        packageName = pkg
        val previewDisplay = current
        BackgroundPreview.begin(previewDisplay, previewDisplay.displayId, pkg, previewDisplay.width, previewDisplay.height)
        previewDisplay.startPreview { frame -> BackgroundPreview.publish(previewDisplay, frame) }
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
        disposeDisplay()
        return status(context, "phase2_release")
    }

    @Synchronized
    fun stop(context: Context): JSONObject {
        disposeDisplay()
        Phase2Controller.stop(context)
        return status(context, "phase2_stop")
    }

    @Synchronized
    fun disposeDisplay() {
        val previous = display
        display = null
        packageName = ""
        observationRequired = false
        handbackSnapshot = ""
        previous?.close()
    }

    private fun status(context: Context, action: String): JSONObject = JSONObject()
        .put("ok", true)
        .put("action", action)
        .put("advanced", AndroidUseBridge.advancedEnabled(context))
        .put("paired", Phase2Controller.isPaired(context))
        .put("helperConnected", Phase2Controller.shell() != null)
        .put("displayId", display?.displayId ?: -1)
        .put("package", packageName)
        .put("observationRequired", observationRequired)

    private fun error(message: String): JSONObject =
        JSONObject().put("ok", false).put("error", message)

    private val packagePattern = Regex("^[A-Za-z0-9_]+(\\.[A-Za-z0-9_]+)+$")
}
