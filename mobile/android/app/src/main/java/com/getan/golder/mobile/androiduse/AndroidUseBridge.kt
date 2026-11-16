package com.getan.golder.mobile.androiduse

import android.content.Context
import android.provider.Settings
import org.json.JSONObject
import java.io.File
import java.net.Socket

object AndroidUseBridge {
    const val SERVICE_CLASS = "com.getan.golder.mobile.androiduse.GolderAccessibilityService"
    private const val SHORT_SERVICE = ".androiduse.GolderAccessibilityService"
    const val HEARTBEAT_FILE = "android-use-heartbeat.json"
    private const val AUDIT_MAX_LINES = 200
    private const val CONTROL_KEY = "control-allowed"
    private const val PAUSED_KEY = "paused"
    private const val ALLOWED_PACKAGES_KEY = "allowed-packages"
    private const val ADVANCED_KEY = "advanced-enabled"
    const val PREFS_NAME = "android-use"

    fun endpointPath(context: Context): String =
        File(context.filesDir, "android-use.json").absolutePath

    /**
     * A stale endpoint file outlives a dead service, so file existence alone
     * cannot mean "ready": the service rewrites a heartbeat every two seconds.
     */
    fun endpointReady(context: Context): Boolean =
        File(endpointPath(context)).isFile && heartbeatFresh(context)

    fun heartbeatFresh(context: Context, maxAgeMs: Long = 8_000): Boolean = runCatching {
        val file = File(context.filesDir, HEARTBEAT_FILE)
        if (!file.isFile) return@runCatching false
        val ts = JSONObject(file.readText()).optLong("ts", 0)
        val age = System.currentTimeMillis() - ts
        age in 0..maxAgeMs
    }.getOrDefault(false)

    /**
     * Probe the loopback bridge instead of trusting a stale endpoint file.
     * An accessibility service can be enabled in system settings yet unbound
     * after an APK update; only a successful status round-trip proves it works.
     */
    fun endpointLive(context: Context, timeoutMs: Int = 700): Boolean = runCatching {
        val endpoint = JSONObject(File(endpointPath(context)).readText())
        val port = endpoint.optInt("port", -1)
        val token = endpoint.optString("token")
        if (port <= 0 || token.isBlank()) return@runCatching false
        Socket("127.0.0.1", port).use { socket ->
            socket.soTimeout = timeoutMs
            val out = socket.getOutputStream()
            out.write(
                JSONObject()
                    .put("action", "status")
                    .put("token", token)
                    .toString()
                    .plus("\n")
                    .toByteArray(Charsets.UTF_8),
            )
            out.flush()
            val line = socket.getInputStream().bufferedReader().readLine() ?: return@use false
            JSONObject(line).optBoolean("ok", false)
        }
    }.getOrDefault(false)

    private fun auditPath(context: Context): File =
        File(context.filesDir, "android-use-audit.jsonl")

    private fun statusPath(context: Context): File =
        File(context.filesDir, "android-use-status.json")

    private fun prefs(context: Context) =
        context.getSharedPreferences(PREFS_NAME, Context.MODE_PRIVATE)

    fun controlAllowed(context: Context): Boolean =
        prefs(context).getBoolean(CONTROL_KEY, isEnabled(context))

    fun setControlAllowed(context: Context, allowed: Boolean) {
        prefs(context).edit().putBoolean(CONTROL_KEY, allowed).apply()
        if (!allowed) setPaused(context, true)
    }

    fun isPaused(context: Context): Boolean = prefs(context).getBoolean(PAUSED_KEY, false)

    fun setPaused(context: Context, paused: Boolean) {
        prefs(context).edit().putBoolean(PAUSED_KEY, paused).apply()
    }

    fun allowedPackages(context: Context): Set<String> =
        prefs(context).getString(ALLOWED_PACKAGES_KEY, "")
            .orEmpty()
            .split(',')
            .map { it.trim() }
            .filter { it.isNotEmpty() }
            .toSet()

    fun setAllowedPackages(context: Context, value: String) {
        prefs(context).edit().putString(ALLOWED_PACKAGES_KEY, value).apply()
    }

    fun advancedEnabled(context: Context): Boolean =
        prefs(context).getBoolean(ADVANCED_KEY, false)

    fun setAdvancedEnabled(context: Context, enabled: Boolean) {
        prefs(context).edit().putBoolean(ADVANCED_KEY, enabled).apply()
        if (!enabled) setPaused(context, true)
    }

    @Synchronized
    fun recordAudit(
        context: Context,
        action: String,
        ok: Boolean,
        packageName: String,
        error: String,
    ) {
        val entry = JSONObject()
            .put("ts", System.currentTimeMillis())
            .put("action", action)
            .put("ok", ok)
        if (packageName.isNotBlank()) entry.put("package", packageName)
        if (error.isNotBlank()) entry.put("error", error.take(240))
        runCatching { auditPath(context).appendText(entry.toString() + "\n") }
        runCatching { trimAudit(auditPath(context)) }
        runCatching {
            statusPath(context).writeText(
                JSONObject(entry.toString())
                    .put("paused", isPaused(context))
                    .put("controlAllowed", controlAllowed(context))
                    .toString(),
            )
        }
    }

    fun recentAudit(context: Context, limit: Int = 8): String = runCatching {
        auditPath(context).readLines().takeLast(limit).joinToString("\n")
    }.getOrDefault("")

    /** Keep the private audit file bounded; Settings only shows the newest entries. */
    private fun trimAudit(path: File) {
        val lines = path.readLines()
        if (lines.size <= AUDIT_MAX_LINES) return
        val tmp = File(path.parentFile, "${path.name}.tmp")
        tmp.writeText(
            lines.takeLast(AUDIT_MAX_LINES).joinToString("\n", postfix = "\n"),
        )
        if (!tmp.renameTo(path)) {
            path.delete()
            check(tmp.renameTo(path))
        }
    }

    fun status(context: Context): String = runCatching {
        statusPath(context).readText()
    }.getOrDefault("")

    fun isEnabled(context: Context): Boolean {
        val enabled = runCatching {
            Settings.Secure.getInt(
                context.contentResolver,
                Settings.Secure.ACCESSIBILITY_ENABLED,
            )
        }.getOrDefault(0) == 1
        if (!enabled) return false
        val services = Settings.Secure.getString(
            context.contentResolver,
            Settings.Secure.ENABLED_ACCESSIBILITY_SERVICES,
        ) ?: return false
        val full = "${context.packageName}/$SERVICE_CLASS"
        val short = "${context.packageName}/$SHORT_SERVICE"
        return services.split(':').any {
            it.equals(full, ignoreCase = true) || it.equals(short, ignoreCase = true)
        }
    }
}
