package com.getan.golder.mobile.androiduse

import android.content.ContentProvider
import android.content.ContentValues
import android.database.Cursor
import android.net.Uri
import android.os.Binder
import android.os.Bundle
import android.os.Process
import android.os.SystemClock
import android.content.pm.ApplicationInfo
import java.io.File

/**
 * Shell -> app binder handoff. The provider is exported because adbd runs the
 * helper as UID 2000; the handoff is authenticated by the shell UID, the app
 * APK path, and a fresh stdin-only nonce.
 */
class GolderShellProvider : ContentProvider() {
    override fun onCreate(): Boolean = true

    override fun call(method: String, arg: String?, extras: Bundle?): Bundle {
        if (method == "status") {
            if (Binder.getCallingUid() != Process.SHELL_UID) return failure("caller is not shell")
            return Bundle().apply {
                putBoolean("ok", true)
                putBoolean("connected", GolderShellRegistry.shell() != null)
            }
        }
        if (method == "probe") {
            val ctx = context ?: return failure("provider unavailable")
            if (ctx.applicationInfo.flags and ApplicationInfo.FLAG_DEBUGGABLE == 0) {
                return failure("probe disabled in release")
            }
            if (Binder.getCallingUid() != Process.SHELL_UID) return failure("caller is not shell")
            val shell = GolderShellRegistry.shell() ?: return failure("helper not connected")
            return runCatching {
                Phase2Display(shell, 320, 240, 160).use { display ->
                    val started = shell.startApp(display.displayId, "com.android.settings")
                    val tapped = shell.injectTap(display.displayId, 160, 120)
                    SystemClock.sleep(800)
                    val frame = display.waitForFrame(3000)
                    Bundle().apply {
                        putBoolean("ok", true)
                        putInt("displayId", display.displayId)
                        putInt("started", started)
                        putBoolean("tapped", tapped)
                        putBoolean("frame", frame)
                    }
                }
            }.getOrElse { failure(it.message ?: "probe failed") }
        }
        if (method != "register") return failure("unsupported method")
        if (Binder.getCallingUid() != Process.SHELL_UID) {
            return failure("caller is not shell")
        }
        val ctx = context ?: return failure("provider unavailable")
        if (!Phase2Controller.consumeNonce(ctx, extras?.getString("nonce"))) {
            return failure("helper nonce mismatch")
        }
        val binder = extras?.getBinder("binder")
            ?: return failure("helper binder missing")
        val remoteUid = extras.getInt("uid", -1)
        val remotePid = extras.getInt("pid", -1)
        if (remoteUid != Binder.getCallingUid() || remotePid <= 0) {
            return failure("helper identity mismatch")
        }
        val installedApk = runCatching {
            ctx.packageManager.getPackageInfo(ctx.packageName, 0).applicationInfo.sourceDir
        }.getOrNull()
        val helperApk = extras.getString("apk")
        if (installedApk == null || helperApk == null || File(installedApk).canonicalPath != File(helperApk).canonicalPath) {
            return failure("helper apk mismatch")
        }
        GolderShellRegistry.install(IGolderShell.Stub.asInterface(binder))
        return Bundle().apply {
            putBoolean("ok", true)
            putLong("ts", System.currentTimeMillis())
        }
    }

    private fun failure(message: String): Bundle =
        Bundle().apply { putString("error", message) }

    override fun query(
        uri: Uri,
        projection: Array<out String>?,
        selection: String?,
        selectionArgs: Array<out String>?,
        sortOrder: String?,
    ): Cursor? = null

    override fun getType(uri: Uri): String? = null
    override fun insert(uri: Uri, values: ContentValues?): Uri? = null
    override fun delete(uri: Uri, selection: String?, selectionArgs: Array<out String>?): Int = 0
    override fun update(
        uri: Uri,
        values: ContentValues?,
        selection: String?,
        selectionArgs: Array<out String>?,
    ): Int = 0
}
