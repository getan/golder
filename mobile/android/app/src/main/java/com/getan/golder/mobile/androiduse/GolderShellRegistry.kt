package com.getan.golder.mobile.androiduse

import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/** Binder handed over by the shell-UID helper; only the app process can install it. */
object GolderShellRegistry {
    @Volatile
    private var current: IGolderShell? = null

    @Volatile
    private var waiter = CountDownLatch(1)

    fun reset() {
        current = null
        waiter = CountDownLatch(1)
    }

    fun install(shell: IGolderShell) {
        current = shell
        waiter.countDown()
    }

    fun shell(): IGolderShell? = current

    fun await(timeoutMs: Long): IGolderShell? {
        waiter.await(timeoutMs, TimeUnit.MILLISECONDS)
        return current
    }
}
