package com.getan.golder.mobile

import android.content.Context

/** Non-secret picker state that should survive an app restart. */
data class SavedSettings(
    val provider: String,
    val model: String,
    val thinking: String,
    val baseUrl: String,
)

object SettingsStore {
    private const val PREFS = "golder-settings"
    private const val PROVIDER = "provider"
    private const val MODEL = "model"
    private const val THINKING = "thinking"
    private const val BASE_URL = "base-url"

    fun load(context: Context): SavedSettings {
        val prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        return SavedSettings(
            provider = prefs.getString(PROVIDER, "").orEmpty(),
            model = prefs.getString(MODEL, "").orEmpty(),
            thinking = prefs.getString(THINKING, "").orEmpty(),
            baseUrl = prefs.getString(BASE_URL, "").orEmpty(),
        )
    }

    fun save(
        context: Context,
        provider: String,
        model: String,
        thinking: String,
        baseUrl: String,
    ) {
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            .edit()
            .putString(PROVIDER, provider)
            .putString(MODEL, model)
            .putString(THINKING, thinking)
            .putString(BASE_URL, baseUrl)
            .apply()
    }
}
