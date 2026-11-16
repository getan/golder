package com.getan.golder.mobile.androiduse

import android.accessibilityservice.AccessibilityService
import android.accessibilityservice.GestureDescription
import android.graphics.Bitmap
import android.graphics.Path
import android.graphics.Rect
import android.os.Build
import android.os.Bundle
import android.view.Display
import android.view.accessibility.AccessibilityNodeInfo
import androidx.core.content.ContextCompat
import org.json.JSONArray
import org.json.JSONObject
import java.io.BufferedReader
import java.io.BufferedWriter
import java.io.File
import java.io.InputStreamReader
import java.io.OutputStreamWriter
import java.net.InetAddress
import java.net.ServerSocket
import java.net.SocketTimeoutException
import java.util.concurrent.CountDownLatch
import java.util.concurrent.Executors
import java.util.concurrent.TimeUnit
import java.util.concurrent.atomic.AtomicInteger
import java.util.UUID

internal class AndroidUseServer(private val service: GolderAccessibilityService) {
    private val snapshotSequence = AtomicInteger()
    private var lastSnapshotId = ""
    private var lastNodes = mutableMapOf<String, AccessibilityNodeInfo>()

    @Volatile
    private var socket: ServerSocket? = null

    @Volatile
    private var thread: Thread? = null
    private val heartbeat = Executors.newSingleThreadScheduledExecutor { r ->
        Thread(r, "golder-android-use-heartbeat").apply { isDaemon = true }
    }

    fun start() {
        if (thread?.isAlive == true) return
        thread = Thread({ serve() }, "golder-android-use").apply {
            isDaemon = true
            start()
        }
        heartbeat.scheduleAtFixedRate({ writeHeartbeat() }, 0, 2, TimeUnit.SECONDS)
    }

    fun close() {
        runCatching { socket?.close() }
        socket = null
        thread?.interrupt()
        thread = null
        heartbeat.shutdownNow()
        lastNodes.clear()
        lastSnapshotId = ""
        runCatching { File(AndroidUseBridge.endpointPath(service)).delete() }
        runCatching { File(service.filesDir, AndroidUseBridge.HEARTBEAT_FILE).delete() }
    }

    private fun serve() {
        val path = AndroidUseBridge.endpointPath(service)
        val auth = UUID.randomUUID().toString()
        try {
            // Both the Go client and the health probe connect to IPv4 loopback.
            // getLoopbackAddress() can select ::1 on Android, making an otherwise
            // healthy service unreachable at 127.0.0.1.
            val local = ServerSocket(0, 20, InetAddress.getByName("127.0.0.1"))
            local.soTimeout = 1000
            socket = local
            val endpoint = File("$path.tmp")
            endpoint.writeText(
                JSONObject()
                    .put("port", local.localPort)
                    .put("token", auth)
                    .toString(),
            )
            endpoint.renameTo(File(path))
            while (!Thread.currentThread().isInterrupted) {
                val client = try {
                    local.accept()
                } catch (_: SocketTimeoutException) {
                    continue
                }
                client.use {
                    val reader = BufferedReader(InputStreamReader(client.inputStream))
                    val writer = BufferedWriter(OutputStreamWriter(client.outputStream))
                    val line = reader.readLine() ?: return@use
                    val request = runCatching { JSONObject(line) }.getOrNull()
                    val response = when {
                        request == null -> error("invalid request")
                        request.optString("token") != auth -> error("unauthorized")
                        else -> runCatching { handle(request) }.getOrElse { error(it.message) }
                    }
                    writer.write(response.toString())
                    writer.newLine()
                    writer.flush()
                }
            }
        } catch (_: Exception) {
            // Closing the service closes the listener; no user-facing log here.
        } finally {
            runCatching { socket?.close() }
            socket = null
            runCatching { File(path).delete() }
            runCatching { File(service.filesDir, AndroidUseBridge.HEARTBEAT_FILE).delete() }
        }
    }

    private fun writeHeartbeat() {
        if (socket == null) return
        runCatching {
            val heartbeat = File(service.filesDir, AndroidUseBridge.HEARTBEAT_FILE)
            val tmp = File("${heartbeat.path}.tmp")
            tmp.writeText(
                JSONObject()
                    .put("ts", System.currentTimeMillis())
                    .toString(),
            )
            if (!tmp.renameTo(heartbeat)) {
                heartbeat.delete()
                check(tmp.renameTo(heartbeat))
            }
        }
    }

    internal fun invalidateObservation() {
        lastNodes.clear()
        lastSnapshotId = ""
    }

    private fun handle(request: JSONObject): JSONObject =
        Phase2Session.guardAgentRequest(request) { handleAllowed(request) }

    private fun handleAllowed(request: JSONObject): JSONObject {
        val action = request.optString("action")
        if (action == "status") {
            return status()
        }
        if (action == "resume") {
            AndroidUseBridge.setPaused(service, false)
            return recorded(action, ok(action).put("paused", false))
        }
        if (action == "take_over") {
            lastNodes.clear()
            lastSnapshotId = ""
            AndroidUseBridge.setPaused(service, true)
            return recorded(action, ok(action).put("paused", true))
        }
        if (action in CONTROL_ACTIONS) {
            if (!AndroidUseBridge.controlAllowed(service)) {
                return recorded(action, error("AndroidUse control is disabled in Golder settings"))
            }
            if (AndroidUseBridge.isPaused(service)) {
                return recorded(action, error("AndroidUse is paused; resume it in Golder settings"))
            }
        }
        val response = when (action) {
            "observe" -> observe()
            "screenshot" -> screenshot()
            "tap" -> tap(request)
            "type" -> type(request)
            "swipe" -> swipe(request)
            "open_app" -> openApp(request)
            "back" -> globalAction(AccessibilityService.GLOBAL_ACTION_BACK, "back")
            "home" -> globalAction(AccessibilityService.GLOBAL_ACTION_HOME, "home")
            "wait" -> waitFor(request)
            "phase2_status" -> Phase2Session.status(service)
            "phase2_observe" -> Phase2Session.observe(service)
            "phase2_start" -> Phase2Session.start(service)
            "phase2_stop" -> Phase2Session.stop(service)
            "phase2_open_app" -> Phase2Session.openApp(service, request.optString("packageName"))
            "phase2_tap" -> Phase2Session.tap(
                service,
                request.optInt("x", -1),
                request.optInt("y", -1),
            )
            "phase2_release" -> Phase2Session.release(service)
            else -> error("unsupported action")
        }
        return recorded(action, response)
    }

    private fun observe(): JSONObject {
        val root = service.rootInActiveWindow ?: return error("no active window")
        val rootPackage = root.packageName?.toString().orEmpty()
        if (!packageAllowed(rootPackage)) {
            return error("package $rootPackage is not in the AndroidUse allowlist")
        }
        val nodes = JSONArray()
        val refs = linkedMapOf<String, AccessibilityNodeInfo>()
        var count = 0

        fun addNode(node: AccessibilityNodeInfo) {
            if (count >= MAX_NODES) return
            count += 1
            val ref = "n-$count"
            val bounds = Rect()
            node.getBoundsInScreen(bounds)
            val text = if (node.isPassword) "" else node.text?.toString().orEmpty()
            val description = if (node.isPassword) "" else node.contentDescription?.toString().orEmpty()
            val item = JSONObject()
                .put("ref", ref)
                .put("role", node.className?.toString()?.substringAfterLast('.') ?: "")
                .put("text", text.take(MAX_TEXT))
                .put("contentDescription", description.take(MAX_TEXT))
                .put("viewId", node.viewIdResourceName ?: "")
                .put("bounds", JSONArray(listOf(bounds.left, bounds.top, bounds.right, bounds.bottom)))
                .put("clickable", node.isClickable)
                .put("editable", node.isEditable)
                .put("focused", node.isFocused)
                .put("enabled", node.isEnabled)
                .put("scrollable", node.isScrollable)
                .put("password", node.isPassword)
            nodes.put(item)
            refs[ref] = node
        }

        fun walk(node: AccessibilityNodeInfo?, depth: Int) {
            if (node == null || depth > MAX_DEPTH || count >= MAX_NODES) return
            val relevant = node.isVisibleToUser ||
                node.isClickable ||
                node.isEditable ||
                !node.text.isNullOrBlank() ||
                !node.contentDescription.isNullOrBlank()
            if (relevant) addNode(node)
            for (index in 0 until node.childCount) {
                walk(node.getChild(index), depth + 1)
            }
        }

        walk(root, 0)
        val snapshot = "s-${snapshotSequence.incrementAndGet()}"
        lastSnapshotId = snapshot
        lastNodes = refs
        val metrics = service.resources.displayMetrics
        return ok("observe")
            .put("snapshotId", snapshot)
            .put("package", root.packageName?.toString().orEmpty())
            .put("paused", AndroidUseBridge.isPaused(service))
            .put("controlAllowed", AndroidUseBridge.controlAllowed(service))
            .put("display", JSONObject()
                .put("width", metrics.widthPixels)
                .put("height", metrics.heightPixels)
                .put("density", metrics.densityDpi))
            .put("nodes", nodes)
    }

    private fun screenshot(): JSONObject {
        if (Build.VERSION.SDK_INT < Build.VERSION_CODES.R) {
            return error("screenshot requires Android 11 or newer")
        }
        val latch = CountDownLatch(1)
        var file: File? = null
        var failure: String? = null
        val callback = object : AccessibilityService.TakeScreenshotCallback {
            override fun onSuccess(result: AccessibilityService.ScreenshotResult) {
                try {
                    val buffer = result.hardwareBuffer
                    val wrapped = Bitmap.wrapHardwareBuffer(buffer, result.colorSpace)
                    val bitmap = wrapped?.copy(Bitmap.Config.ARGB_8888, false)
                    buffer.close()
                    if (bitmap == null) {
                        failure = "screenshot bitmap is unavailable"
                    } else {
                        val dir = File(service.cacheDir, "android-use").apply { mkdirs() }
                        val target = File(dir, "shot-${System.currentTimeMillis()}.png")
                        target.outputStream().use { bitmap.compress(Bitmap.CompressFormat.PNG, 90, it) }
                        bitmap.recycle()
                        pruneScreenshots(dir)
                        file = target
                    }
                } catch (e: Exception) {
                    failure = e.message ?: "screenshot failed"
                } finally {
                    latch.countDown()
                }
            }

            override fun onFailure(errorCode: Int) {
                failure = "screenshot failed: $errorCode"
                latch.countDown()
            }
        }
        service.takeScreenshot(
            Display.DEFAULT_DISPLAY,
            ContextCompat.getMainExecutor(service),
            callback,
        )
        if (!latch.await(5, TimeUnit.SECONDS)) return error("screenshot timed out")
        failure?.let { return error(it) }
        val saved = file ?: return error("screenshot produced no file")
        return ok("screenshot")
            .put("path", saved.absolutePath)
            .put("size", saved.length())
    }

    private fun tap(request: JSONObject): JSONObject {
        checkSnapshot(request)?.let { return error(it) }
        packageAllowed(service.rootInActiveWindow?.packageName?.toString().orEmpty())
            .let { if (!it) return error("current package is not in the AndroidUse allowlist") }
        val ref = request.optString("ref")
        if (ref.isNotBlank()) {
            val node = lastNodes[ref] ?: return error("unknown node ref")
            sensitiveReason(node)?.let {
                return sensitiveHandoff(it)
            }
            runCatching { node.refresh() }
            var target: AccessibilityNodeInfo? = node
            var depth = 0
            while (target != null && !target.isClickable && depth < 5) {
                target = target.parent
                depth += 1
            }
            if (target != null && target.performAction(AccessibilityNodeInfo.ACTION_CLICK)) {
                return withForeground(ok("tap").put("ref", ref))
            }
            val bounds = Rect()
            node.getBoundsInScreen(bounds)
            if (tapAt(bounds.centerX(), bounds.centerY())) {
                return withForeground(ok("tap").put("ref", ref).put("fallback", true))
            }
            return error("tap failed")
        }
        val x = request.optInt("x", -1)
        val y = request.optInt("y", -1)
        if (x < 0 || y < 0) return error("tap requires ref or x/y")
        sensitiveReason(service.rootInActiveWindow)?.let {
            return sensitiveHandoff(it)
        }
        return if (tapAt(x, y)) withForeground(ok("tap")) else error("tap failed")
    }

    private fun type(request: JSONObject): JSONObject {
        checkSnapshot(request)?.let { return error(it) }
        packageAllowed(service.rootInActiveWindow?.packageName?.toString().orEmpty())
            .let { if (!it) return error("current package is not in the AndroidUse allowlist") }
        val ref = request.optString("ref")
        val node = lastNodes[ref] ?: return error("unknown node ref")
        if (node.isPassword) return error("refusing to type into a password field")
        sensitiveReason(node)?.let {
            return sensitiveHandoff(it)
        }
        runCatching { node.refresh() }
        node.performAction(AccessibilityNodeInfo.ACTION_FOCUS)
        val bundle = Bundle().apply {
            putCharSequence(
                AccessibilityNodeInfo.ACTION_ARGUMENT_SET_TEXT_CHARSEQUENCE,
                request.optString("text"),
            )
        }
        if (!node.performAction(AccessibilityNodeInfo.ACTION_SET_TEXT, bundle)) {
            return error("text field did not accept text")
        }
        return withForeground(ok("type").put("ref", ref))
    }

    private fun swipe(request: JSONObject): JSONObject {
        checkSnapshot(request)?.let { return error(it) }
        packageAllowed(service.rootInActiveWindow?.packageName?.toString().orEmpty())
            .let { if (!it) return error("current package is not in the AndroidUse allowlist") }
        sensitiveReason(service.rootInActiveWindow)?.let {
            return sensitiveHandoff(it)
        }
        val x = request.optInt("x", -1)
        val y = request.optInt("y", -1)
        val toX = request.optInt("toX", -1)
        val toY = request.optInt("toY", -1)
        if (x < 0 || y < 0 || toX < 0 || toY < 0) {
            return error("swipe requires x/y and toX/toY")
        }
        val duration = request.optInt("durationMs", 300).coerceIn(50, 5000).toLong()
        val path = Path().apply {
            moveTo(x.toFloat(), y.toFloat())
            lineTo(toX.toFloat(), toY.toFloat())
        }
        return if (dispatchGesture(path, duration)) withForeground(ok("swipe")) else error("swipe failed")
    }

    private fun openApp(request: JSONObject): JSONObject {
        val pkg = request.optString("packageName")
        if (!packagePattern.matches(pkg)) return error("invalid package name")
        if (!packageAllowed(pkg)) return error("package $pkg is not in the AndroidUse allowlist")
        if (BLOCKED_PACKAGES.any { pkg == it || pkg.startsWith("$it.") }) {
            return error("opening system permission/installer UI is blocked")
        }
        val pm = service.packageManager
        val intent = pm.getLaunchIntentForPackage(pkg)
        if (intent == null) {
            // Distinguish "really absent" from "hidden by package visibility" so
            // the model does not tell the user to install an app that is there.
            val known = runCatching { pm.getPackageInfo(pkg, 0) }.isSuccess
            return error(
                if (known) "package $pkg has no launcher activity" else "app is not installed or not visible to Golder",
            )
        }
        fun foreground(): Boolean = service.rootInActiveWindow?.packageName?.toString() == pkg
        fun waitForForeground(rounds: Int): Boolean {
            repeat(rounds) {
                if (foreground()) return true
                Thread.sleep(200)
            }
            return foreground()
        }
        if (foreground()) {
            return ok("open_app").put("packageName", pkg).put("verified", true).put("alreadyOpen", true)
        }
        // OEM builds refuse a background activity start from an app, while the
        // connected shell helper is allowed to do it. Try the helper first so the
        // common MIUI case does not pay for a refused attempt.
        if (Phase2Session.launchOnMain(service, pkg) && waitForForeground(6)) {
            return ok("open_app").put("packageName", pkg).put("verified", true).put("viaHelper", true)
        }
        intent.addFlags(android.content.Intent.FLAG_ACTIVITY_NEW_TASK)
        runCatching { service.startActivity(intent) }
        if (waitForForeground(6)) {
            return ok("open_app").put("packageName", pkg).put("verified", true)
        }
        return error(
            "the system blocked bringing $pkg to the foreground; " +
                "open the app manually, then call observe. This OEM may restrict background activity launches.",
        )
    }

    private fun globalAction(action: Int, name: String): JSONObject {
        return if (service.performGlobalAction(action)) withForeground(ok(name)) else error("$name failed")
    }

    private fun waitFor(request: JSONObject): JSONObject {
        val duration = request.optInt("durationMs", 200).coerceIn(50, 5000)
        return runCatching {
            Thread.sleep(duration.toLong())
            ok("wait").put("durationMs", duration)
        }.getOrElse { error("wait interrupted") }
    }

    private fun status(): JSONObject {
        val pkg = service.rootInActiveWindow?.packageName?.toString().orEmpty()
        return ok("status")
            .put("paused", AndroidUseBridge.isPaused(service))
            .put("controlAllowed", AndroidUseBridge.controlAllowed(service))
            .put("package", pkg)
            .put("allowlist", AndroidUseBridge.allowedPackages(service).joinToString(","))
            .put("audit", AndroidUseBridge.recentAudit(service))
    }

    private fun recorded(action: String, response: JSONObject): JSONObject {
        AndroidUseBridge.recordAudit(
            service,
            action,
            response.optBoolean("ok"),
            response.optString("package"),
            response.optString("error"),
        )
        AndroidUseNotifier.update(
            service,
            action,
            response.optString("package"),
            AndroidUseBridge.isPaused(service),
        )
        return response
    }

    private fun withForeground(response: JSONObject): JSONObject {
        Thread.sleep(180)
        val pkg = service.rootInActiveWindow?.packageName?.toString().orEmpty()
        return response.put("package", pkg).put("verified", pkg.isNotBlank())
    }

    private fun sensitiveReason(node: AccessibilityNodeInfo?): String? {
        var current = node
        var depth = 0
        while (current != null && depth < 4) {
            val text = (
                current.text?.toString().orEmpty() + " " +
                    current.contentDescription?.toString().orEmpty()
                ).lowercase()
            SENSITIVE_TERMS.firstOrNull { text.contains(it) }?.let { return it }
            current = current.parent
            depth++
        }
        return null
    }

    private fun nodeDetail(node: AccessibilityNodeInfo): String {
        val text = node.text?.toString().orEmpty()
        val description = node.contentDescription?.toString().orEmpty()
        return (text + " " + description).trim().take(80).ifBlank { "current control" }
    }

    private fun pruneScreenshots(dir: File) {
        val files = dir.listFiles()?.filter { it.isFile && it.name.endsWith(".png") }
            ?.sortedByDescending { it.lastModified() }
            ?: return
        files.drop(8).forEach { runCatching { it.delete() } }
    }

    private fun checkSnapshot(request: JSONObject): String? {
        val snapshot = request.optString("snapshotId")
        if (snapshot.isBlank() || snapshot != lastSnapshotId) {
            return "snapshot is stale; observe again"
        }
        return null
    }

    private fun packageAllowed(pkg: String): Boolean {
        val allowed = AndroidUseBridge.allowedPackages(service)
        return allowed.isEmpty() || pkg in allowed
    }

    private fun tapAt(x: Int, y: Int): Boolean {
        val path = Path().apply { moveTo(x.toFloat(), y.toFloat()) }
        return dispatchGesture(path, 60)
    }

    private fun dispatchGesture(path: Path, duration: Long): Boolean {
        val latch = CountDownLatch(1)
        var completed = false
        val gesture = GestureDescription.Builder()
            .addStroke(GestureDescription.StrokeDescription(path, 0, duration))
            .build()
        val callback = object : AccessibilityService.GestureResultCallback() {
            override fun onCompleted(gestureDescription: GestureDescription?) {
                completed = true
                latch.countDown()
            }

            override fun onCancelled(gestureDescription: GestureDescription?) {
                latch.countDown()
            }
        }
        if (!service.dispatchGesture(gesture, callback, null)) return false
        return latch.await(3, TimeUnit.SECONDS) && completed
    }

    private fun ok(action: String): JSONObject =
        JSONObject().put("ok", true).put("action", action)

    private fun error(message: String?): JSONObject =
        JSONObject().put("ok", false).put("error", message ?: "AndroidUse failed")

    /**
     * A sensitive control action is the person's decision, not the model's. Pause
     * control and ask for a handoff instead of guessing at consent.
     */
    private fun sensitiveHandoff(reason: String): JSONObject {
        AndroidUseBridge.setPaused(service, true)
        return error(
            "sensitive action '$reason' needs the user. Ask them to take over the background display " +
                "from the live preview, then continue after they hand it back.",
        )
    }

    private companion object {
        const val MAX_NODES = 300
        const val MAX_DEPTH = 30
        const val MAX_TEXT = 240
        val packagePattern = Regex("^[A-Za-z0-9_]+(\\.[A-Za-z0-9_]+)+$")
        val CONTROL_ACTIONS = setOf("tap", "type", "swipe", "open_app", "back", "home")
        val BLOCKED_PACKAGES = listOf(
            "com.android.permissioncontroller",
            "com.google.android.permissioncontroller",
            "com.android.packageinstaller",
            "com.google.android.packageinstaller",
        )
        val SENSITIVE_TERMS = listOf(
            "支付", "付款", "购买", "转账", "删除", "卸载", "安装", "允许", "确认",
            "发送", "提交", "登录", "验证码",
            "pay", "purchase", "transfer", "delete", "uninstall", "install",
            "allow", "confirm", "send", "submit", "login", "verification",
        )
    }
}
