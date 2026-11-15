package com.getan.golder.mobile.androiduse

import android.content.Context
import android.content.ContextWrapper
import android.content.Intent
import android.net.Uri
import android.os.Bundle
import android.os.Looper
import android.os.Process
import android.util.Log
import java.io.BufferedReader
import java.io.InputStreamReader

@Suppress("PrivateApi")
object HelperMain {
    private const val TAG = "GolderHelper"

    @JvmStatic
    fun main(args: Array<String>) {
        Log.i(TAG, "start args=${args.joinToString(",")} uid=${Process.myUid()}")
        if (args.size < 2 || Process.myUid() != Process.SHELL_UID) {
            Log.e(TAG, "rejected args/uid")
            return
        }
        val packageName = args[0]
        val nonce = BufferedReader(InputStreamReader(System.`in`)).readLine() ?: return
        if (nonce.isBlank()) return
        Log.i(TAG, "nonce read, preparing looper")
        Looper.prepareMainLooper()
        val context = systemContext()
        if (context == null) {
            Log.e(TAG, "system context unavailable")
            return
        }
        val registrationContext = runCatching {
            context.createPackageContext("com.android.shell", 0)
        }.onFailure { Log.e(TAG, "shell context unavailable", it) }.getOrElse { context }
        val shell = HelperShell(ShellContext(registrationContext))
        val apk = runCatching {
            context.packageManager.getPackageInfo(packageName, 0).applicationInfo.sourceDir
        }.onFailure { Log.e(TAG, "package lookup failed", it) }.getOrNull() ?: return
        val handoff = Intent()
            .setClassName(packageName, "$packageName.androiduse.Phase2HandoffReceiver")
            .addFlags(Intent.FLAG_RECEIVER_FOREGROUND)
            .putExtras(Bundle().apply { putBinder("binder", shell.asBinder()) })
            .putExtra("nonce", nonce)
            .putExtra("apk", apk)
        runCatching { registrationContext.sendBroadcast(handoff) }
            .onFailure { Log.e(TAG, "handoff broadcast failed", it) }
        Log.i(TAG, "handoff broadcast sent, keeping binder alive")
        Looper.loop()
    }

    private fun systemContext(): Context? = runCatching {
        val threadClass = Class.forName("android.app.ActivityThread")
        val thread = threadClass.getMethod("systemMain").invoke(null)
        threadClass.getMethod("getSystemContext").invoke(thread) as Context
    }.onFailure { Log.e(TAG, "systemMain failed", it) }.getOrNull()

    private class ShellContext(base: Context) : ContextWrapper(base) {
        override fun getPackageName(): String = "com.android.shell"
        override fun getOpPackageName(): String = "com.android.shell"
    }
}
