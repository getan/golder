package com.getan.golder.mobile.androiduse

import android.content.Context
import android.provider.Settings
import org.json.JSONObject
import java.io.File

object AndroidUseBridge {
    const val SERVICE_CLASS = "com.getan.golder.mobile.androiduse.GolderAccessibilityService"
    private const val SHORT_SERVICE = ".androiduse.GolderAccessibilityService"
    private const val CONTROL_KEY = "control-allowed"
    private const val PAUSED_KEY = "paused"
    private const val ALLOWED_PACKAGES_KEY = "allowed-packages"
    private const val OVERLAY_KEY = "overlay-enabled"
    const val PREFS_NAME = "android-use"

    fun endpointPath(context: Context): String =
        File(context.filesDir, "android-use.json").absolutePath

    fun endpointReady(context: Context): Boolean =
        File(endpointPath(context)).isFile

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

    fun overlayEnabled(context: Context): Boolean =
        prefs(context).getBoolean(OVERLAY_KEY, true)

    fun setOverlayEnabled(context: Context, enabled: Boolean) {
        prefs(context).edit().putBoolean(OVERLAY_KEY, enabled).apply()
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
