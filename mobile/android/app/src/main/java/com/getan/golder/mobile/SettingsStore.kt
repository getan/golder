package com.getan.golder.mobile

import android.content.Context

/** Non-secret picker state that should survive an app restart. */
data class SavedSettings(
    val provider: String,
    val model: String,
    val thinking: String,
    val baseUrl: String,
    val permissionMode: String,
    val language: String,
)

object SettingsStore {
    private const val PREFS = "golder-settings"
    private const val PROVIDER = "provider"
    private const val MODEL = "model"
    private const val THINKING = "thinking"
    private const val BASE_URL = "base-url"
    private const val PERMISSION_MODE = "permission-mode"
    private const val LANGUAGE = "language"

    fun load(context: Context): SavedSettings {
        val prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        return SavedSettings(
            provider = prefs.getString(PROVIDER, "").orEmpty(),
            model = prefs.getString(MODEL, "").orEmpty(),
            thinking = prefs.getString(THINKING, "").orEmpty(),
            baseUrl = prefs.getString(BASE_URL, "").orEmpty(),
            permissionMode = prefs.getString(PERMISSION_MODE, "auto").orEmpty().ifBlank { "auto" },
            language = prefs.getString(LANGUAGE, "en").orEmpty().ifBlank { "en" },
        )
    }

    fun save(
        context: Context,
        provider: String,
        model: String,
        thinking: String,
        baseUrl: String,
        permissionMode: String,
        language: String,
    ) {
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            .edit()
            .putString(PROVIDER, provider)
            .putString(MODEL, model)
            .putString(THINKING, thinking)
            .putString(BASE_URL, baseUrl)
            .putString(PERMISSION_MODE, permissionMode)
            .putString(LANGUAGE, language)
            .apply()
    }
}
