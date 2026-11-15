package com.getan.golder.mobile.androiduse

import android.accessibilityservice.AccessibilityService
import android.content.Intent
import android.view.accessibility.AccessibilityEvent

class GolderAccessibilityService : AccessibilityService() {
    @Volatile
    private var server: AndroidUseServer? = null
    private var overlay: AndroidUseOverlay? = null

    override fun onServiceConnected() {
        super.onServiceConnected()
        AndroidUseNotifier.update(this, "ready", "", AndroidUseBridge.isPaused(this))
        overlay = AndroidUseOverlay(this).also { it.show() }
        if (server == null) {
            server = AndroidUseServer(this).also { it.start() }
        }
    }

    fun updateOverlay(action: String, packageName: String, paused: Boolean, imagePath: String? = null) {
        overlay?.update(action, packageName, paused, imagePath)
    }

    fun requestOverlayConfirmation(action: String, detail: String): Boolean =
        overlay?.requestConfirmation(action, detail) ?: false

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
        AndroidUseNotifier.cancel(this)
    }
}
