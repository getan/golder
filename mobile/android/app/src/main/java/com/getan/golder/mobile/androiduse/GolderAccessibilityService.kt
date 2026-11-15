package com.getan.golder.mobile.androiduse

import android.accessibilityservice.AccessibilityService
import android.content.Intent
import android.content.SharedPreferences
import android.view.accessibility.AccessibilityEvent

class GolderAccessibilityService : AccessibilityService() {
    @Volatile
    private var server: AndroidUseServer? = null
    private var overlay: AndroidUseOverlay? = null
    private var prefsRegistered = false
    private val prefsListener = SharedPreferences.OnSharedPreferenceChangeListener { _, _ ->
        syncOverlay()
    }

    override fun onServiceConnected() {
        super.onServiceConnected()
        AndroidUseNotifier.update(this, "ready", "", AndroidUseBridge.isPaused(this))
        if (!prefsRegistered) {
            getSharedPreferences(AndroidUseBridge.PREFS_NAME, MODE_PRIVATE)
                .registerOnSharedPreferenceChangeListener(prefsListener)
            prefsRegistered = true
        }
        syncOverlay()
        if (server == null) {
            server = AndroidUseServer(this).also { it.start() }
        }
    }

    fun updateOverlay(action: String, packageName: String, paused: Boolean, imagePath: String? = null) {
        if (AndroidUseBridge.overlayEnabled(this)) {
            syncOverlay()
            overlay?.update(action, packageName, paused, imagePath)
        }
    }

    fun requestOverlayConfirmation(action: String, detail: String): Boolean {
        ensureOverlay()
        val confirmed = overlay?.requestConfirmation(action, detail) ?: false
        if (!AndroidUseBridge.overlayEnabled(this)) {
            overlay?.close()
            overlay = null
        }
        return confirmed
    }

    override fun onAccessibilityEvent(event: AccessibilityEvent?) = Unit

    override fun onInterrupt() = Unit

    override fun onUnbind(intent: Intent?): Boolean {
        closeServer()
        return super.onUnbind(intent)
    }

    override fun onDestroy() {
        closeServer()
        super.onDestroy()
    }

    private fun closeServer() {
        server?.close()
        server = null
        overlay?.close()
        overlay = null
        if (prefsRegistered) {
            runCatching {
                getSharedPreferences(AndroidUseBridge.PREFS_NAME, MODE_PRIVATE)
                    .unregisterOnSharedPreferenceChangeListener(prefsListener)
            }
            prefsRegistered = false
        }
        AndroidUseNotifier.cancel(this)
    }

    private fun ensureOverlay() {
        if (overlay == null) {
            overlay = AndroidUseOverlay(this).also { it.show() }
        }
    }

    private fun syncOverlay() {
        if (AndroidUseBridge.overlayEnabled(this)) {
            ensureOverlay()
        } else {
            overlay?.close()
            overlay = null
        }
    }
}
