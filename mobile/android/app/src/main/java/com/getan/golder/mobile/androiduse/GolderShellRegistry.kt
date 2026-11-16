package com.getan.golder.mobile.androiduse

import android.os.IBinder
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/** Binder handed over by the shell-UID helper; only the app process can install it. */
object GolderShellRegistry {
    @Volatile
    private var current: IGolderShell? = null

    @Volatile
    private var waiter = CountDownLatch(1)
    private var deathRecipient: IBinder.DeathRecipient? = null

    @Synchronized
    fun reset() {
        deathRecipient?.let { recipient ->
            runCatching { current?.asBinder()?.unlinkToDeath(recipient, 0) }
        }
        deathRecipient = null
        current = null
        waiter = CountDownLatch(1)
    }

    @Synchronized
    fun install(shell: IGolderShell) {
        deathRecipient?.let { recipient ->
            runCatching { current?.asBinder()?.unlinkToDeath(recipient, 0) }
        }
        val binder = shell.asBinder()
        val recipient = IBinder.DeathRecipient {
            val removed = synchronized(this) {
                if (current?.asBinder() !== binder) false else {
                    current = null
                    deathRecipient = null
                    waiter = CountDownLatch(1)
                    true
                }
            }
            if (removed) Phase2Session.disposeDisplay()
        }
        current = shell
        deathRecipient = recipient
        try {
            binder.linkToDeath(recipient, 0)
        } catch (_: android.os.RemoteException) {
            current = null
            deathRecipient = null
        }
        waiter.countDown()
    }

    fun shell(): IGolderShell? = current

    fun await(timeoutMs: Long): IGolderShell? {
        waiter.await(timeoutMs, TimeUnit.MILLISECONDS)
        return current
    }
}
