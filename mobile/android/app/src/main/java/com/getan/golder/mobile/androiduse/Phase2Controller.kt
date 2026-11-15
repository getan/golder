package com.getan.golder.mobile.androiduse

import android.content.Context
import java.security.MessageDigest
import java.util.UUID

object Phase2Controller {
    private const val NONCE_KEY = "phase2-helper-nonce"

    fun beginHandoff(context: Context): String {
        val nonce = UUID.randomUUID().toString()
        context.getSharedPreferences(AndroidUseBridge.PREFS_NAME, Context.MODE_PRIVATE)
            .edit().putString(NONCE_KEY, nonce).apply()
        GolderShellRegistry.reset()
        return nonce
    }

    fun expectedNonce(context: Context): String? =
        context.getSharedPreferences(AndroidUseBridge.PREFS_NAME, Context.MODE_PRIVATE)
            .getString(NONCE_KEY, null)

    fun clear(context: Context) {
        context.getSharedPreferences(AndroidUseBridge.PREFS_NAME, Context.MODE_PRIVATE)
            .edit().remove(NONCE_KEY).apply()
        GolderShellRegistry.reset()
    }

    fun acceptHandoff(
        context: Context,
        binder: android.os.IBinder?,
        supplied: String?,
        helperApk: String?,
    ): Boolean {
        if (!consumeNonce(context, supplied) || binder == null) return false
        val installed = runCatching {
            context.packageManager.getPackageInfo(context.packageName, 0).applicationInfo.sourceDir
        }.getOrNull() ?: return false
        val suppliedPath = helperApk ?: return false
        if (java.io.File(installed).canonicalPath != java.io.File(suppliedPath).canonicalPath) return false
        GolderShellRegistry.install(IGolderShell.Stub.asInterface(binder))
        context.getSharedPreferences(AndroidUseBridge.PREFS_NAME, Context.MODE_PRIVATE)
            .edit().remove(NONCE_KEY).apply()
        return true
    }

    fun shell(): IGolderShell? = GolderShellRegistry.shell()

    fun awaitShell(timeoutMs: Long): IGolderShell? = GolderShellRegistry.await(timeoutMs)

    fun consumeNonce(context: Context, supplied: String?): Boolean {
        val expected = expectedNonce(context) ?: return false
        if (supplied.isNullOrEmpty()) return false
        return MessageDigest.isEqual(expected.toByteArray(), supplied.toByteArray())
    }
}
