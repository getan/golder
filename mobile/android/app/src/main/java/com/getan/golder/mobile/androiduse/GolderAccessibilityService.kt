package com.getan.golder.mobile.androiduse

import android.accessibilityservice.AccessibilityService
import android.content.Intent
import android.view.accessibility.AccessibilityEvent

class GolderAccessibilityService : AccessibilityService() {
    @Volatile
    private var server: AndroidUseServer? = null

    override fun onServiceConnected() {
        super.onServiceConnected()
        if (server == null) {
            server = AndroidUseServer(this).also { it.start() }
        }
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
    }
}
