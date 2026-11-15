package com.getan.golder.mobile.androiduse

import android.content.Context
import android.provider.Settings
import java.io.File

object AndroidUseBridge {
    const val SERVICE_CLASS = "com.getan.golder.mobile.androiduse.GolderAccessibilityService"
    private const val SHORT_SERVICE = ".androiduse.GolderAccessibilityService"

    fun endpointPath(context: Context): String =
        File(context.filesDir, "android-use.json").absolutePath

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
