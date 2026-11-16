package com.getan.golder.mobile.androiduse

import android.accessibilityservice.AccessibilityService
import android.content.Intent
import android.content.SharedPreferences
import android.view.accessibility.AccessibilityEvent
import android.view.accessibility.AccessibilityNodeInfo

class GolderAccessibilityService : AccessibilityService() {
    companion object {
        @Volatile
        var instance: GolderAccessibilityService? = null
            private set

        private const val MAX_PAIRING_NODES = 400
    }

    @Volatile
    private var server: AndroidUseServer? = null
    private var overlay: AndroidUseOverlay? = null
    private var prefsRegistered = false

    @Volatile
    var pairingWindowTexts: List<List<String>> = emptyList()
        private set

    private val prefsListener = SharedPreferences.OnSharedPreferenceChangeListener { _, _ ->
        syncOverlay()
    }

    override fun onServiceConnected() {
        super.onServiceConnected()
        instance = this
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

    override fun onAccessibilityEvent(event: AccessibilityEvent?) {
        if (!Phase2PairingCoordinator.isActive) return
        val packageName = event?.packageName?.toString().orEmpty()
        if (packageName != "com.android.settings" && packageName != "com.android.systemui") return
        pairingWindowTexts = captureSettingsWindows()
    }

    override fun onInterrupt() = Unit

    override fun onUnbind(intent: Intent?): Boolean {
        closeServer()
        if (instance === this) instance = null
        pairingWindowTexts = emptyList()
        return super.onUnbind(intent)
    }

    override fun onDestroy() {
        closeServer()
        if (instance === this) instance = null
        pairingWindowTexts = emptyList()
        super.onDestroy()
    }

    private fun captureSettingsWindows(): List<List<String>> {
        val snapshots = mutableListOf<List<String>>()
        for (window in windows) {
            val root = runCatching { window.root }.getOrNull() ?: continue
            val packageName = root.packageName?.toString().orEmpty()
            if (packageName != "com.android.settings" && packageName != "com.android.systemui") continue
            val texts = mutableListOf<String>()
            val queue = ArrayDeque<AccessibilityNodeInfo>()
            queue.add(root)
            var seen = 0
            while (queue.isNotEmpty() && seen++ < MAX_PAIRING_NODES) {
                val node = queue.removeFirst()
                node.text?.toString()?.takeIf { it.isNotBlank() }?.let(texts::add)
                node.contentDescription?.toString()?.takeIf { it.isNotBlank() }?.let(texts::add)
                for (index in 0 until node.childCount) {
                    runCatching { node.getChild(index) }.getOrNull()?.let(queue::add)
                }
            }
            if (texts.isNotEmpty()) snapshots += texts
        }
        return snapshots
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
