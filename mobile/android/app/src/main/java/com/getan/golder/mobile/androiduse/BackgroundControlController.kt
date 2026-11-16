package com.getan.golder.mobile.androiduse

import android.content.Context
import com.getan.golder.mobile.GolderNative
import org.json.JSONObject
import java.security.MessageDigest
import java.io.File
import java.util.UUID

/** Lifecycle and pairing state for background phone control. */
object Phase2Controller {
    private const val NONCE_KEY = "phase2-helper-nonce"
    private const val PAIRED_KEY = "phase2-paired"
    private const val PAIR_ADDRESS_KEY = "phase2-pair-address"
    private const val CONNECT_ADDRESS_KEY = "phase2-connect-address"

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

    fun isPaired(context: Context): Boolean =
        prefs(context).getBoolean(PAIRED_KEY, false)

    fun pairAddress(context: Context): String = prefs(context).getString(PAIR_ADDRESS_KEY, "").orEmpty()
    fun connectAddress(context: Context): String = prefs(context).getString(CONNECT_ADDRESS_KEY, "").orEmpty()

    fun rememberAddresses(context: Context, pairAddress: String, connectAddress: String) {
        prefs(context).edit()
            .putString(PAIR_ADDRESS_KEY, pairAddress.trim())
            .putString(CONNECT_ADDRESS_KEY, connectAddress.trim())
            .apply()
    }

    fun pair(context: Context, address: String, code: String): Result<Unit> {
        if (!AndroidUseBridge.advancedEnabled(context)) {
            return Result.failure(IllegalStateException("Enable advanced control first"))
        }
        val dir = identityDir(context)
        val parsed = parse(GolderNative.phase2Pair(address.trim(), code.trim(), dir))
        if (parsed.isSuccess) {
            prefs(context).edit().putBoolean(PAIRED_KEY, true).apply()
        }
        return parsed
    }

    /** Discover the current local port on a worker thread; never use a remote host. */
    fun startAutomatically(context: Context): Result<Unit> {
        if (!AndroidUseBridge.advancedEnabled(context)) {
            return Result.failure(IllegalStateException("Enable background control first"))
        }
        if (!isPaired(context)) {
            return Result.failure(IllegalStateException("Pair wireless debugging first"))
        }
        if (shell()?.asBinder()?.isBinderAlive == true) return Result.success(Unit)
        val address = LocalAdbDiscovery.findConnectAddress(context)
            ?: return Result.failure(IllegalStateException("Could not discover this phone's wireless debugging port; check that wireless debugging is enabled"))
        val result = start(context, address)
        if (result.isSuccess) rememberAddresses(context, pairAddress(context), address)
        return result
    }

    fun start(context: Context, address: String): Result<Unit> {
        if (!AndroidUseBridge.advancedEnabled(context)) {
            return Result.failure(IllegalStateException("Enable advanced control first"))
        }
        if (!isPaired(context)) {
            return Result.failure(IllegalStateException("Pair wireless debugging first"))
        }
        val nonce = beginHandoff(context)
        val parsed = parse(
            GolderNative.phase2Start(
                address.trim(),
                context.applicationInfo.sourceDir,
                context.packageName,
                "com.getan.golder.mobile.androiduse.HelperMain",
                "",
                context.applicationInfo.uid,
                nonce,
                identityDir(context),
            ),
        )
        if (parsed.isFailure) {
            clear(context)
            return parsed
        }
        if (awaitShell(5_000) == null) {
            clear(context)
            return Result.failure(IllegalStateException("Helper did not connect"))
        }
        return Result.success(Unit)
    }

    fun stop(context: Context) {
        Phase2Session.disposeDisplay()
        runCatching { shell()?.destroy() }
        clear(context)
    }

    private fun identityDir(context: Context): String =
        File(context.filesDir, "phase2").also { it.mkdirs() }.absolutePath

    private fun prefs(context: Context) =
        context.getSharedPreferences(AndroidUseBridge.PREFS_NAME, Context.MODE_PRIVATE)

    private fun parse(raw: String?): Result<Unit> {
        if (raw == null) return Result.failure(IllegalStateException("native background control bridge unavailable"))
        val obj = runCatching { JSONObject(raw) }.getOrElse {
            return Result.failure(IllegalStateException("invalid background control bridge response"))
        }
        if (!obj.optBoolean("ok", false)) {
            return Result.failure(IllegalStateException(obj.optString("error", "background control failed")))
        }
        return Result.success(Unit)
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
