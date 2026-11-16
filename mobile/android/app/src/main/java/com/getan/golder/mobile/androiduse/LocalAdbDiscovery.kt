package com.getan.golder.mobile.androiduse

import android.annotation.SuppressLint
import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import java.net.InetAddress
import java.net.NetworkInterface
import java.util.concurrent.CountDownLatch
import java.util.concurrent.LinkedBlockingQueue
import java.util.concurrent.TimeUnit

/** Finds the phone's own wireless-debugging connect port, never a peer device. */
internal object LocalAdbDiscovery {
    private const val CONNECT_SERVICE = "_adb-tls-connect._tcp."

    /** Call on a worker thread. An unavailable discovery service is not an authorization grant. */
    fun findConnectAddress(context: Context): String? {
        systemPort()?.let { return "127.0.0.1:$it" }
        val nsd = context.applicationContext.getSystemService(NsdManager::class.java) ?: return null
        val found = LinkedBlockingQueue<NsdServiceInfo>()
        val listener = object : NsdManager.DiscoveryListener {
            override fun onDiscoveryStarted(serviceType: String) = Unit
            override fun onDiscoveryStopped(serviceType: String) = Unit
            override fun onServiceFound(serviceInfo: NsdServiceInfo) { found.offer(serviceInfo) }
            override fun onServiceLost(serviceInfo: NsdServiceInfo) = Unit
            override fun onStartDiscoveryFailed(serviceType: String, errorCode: Int) = Unit
            override fun onStopDiscoveryFailed(serviceType: String, errorCode: Int) = Unit
        }
        val ports = linkedSetOf<Int>()
        var started = false
        try {
            nsd.discoverServices(CONNECT_SERVICE, NsdManager.PROTOCOL_DNS_SD, listener)
            started = true
            val deadline = System.nanoTime() + TimeUnit.SECONDS.toNanos(4)
            var settleDeadline = deadline
            while (System.nanoTime() < settleDeadline) {
                val remaining = settleDeadline - System.nanoTime()
                val info = found.poll(remaining.coerceAtLeast(1), TimeUnit.NANOSECONDS) ?: break
                val port = resolveLocalPort(nsd, info, deadline)
                if (port != null) {
                    ports += port
                    settleDeadline = minOf(deadline, System.nanoTime() + TimeUnit.MILLISECONDS.toNanos(500))
                }
            }
        } catch (_: Exception) {
            return null
        } finally {
            if (started) runCatching { nsd.stopServiceDiscovery(listener) }
        }
        // Do not guess if advertisements disagree about this phone's port.
        return ports.singleOrNull()?.let { "127.0.0.1:$it" }
    }

    @SuppressLint("PrivateApi")
    private fun systemPort(): Int? = runCatching {
        val value = Class.forName("android.os.SystemProperties")
            .getMethod("get", String::class.java)
            .invoke(null, "service.adb.tls.port") as? String
        value?.toIntOrNull()?.takeIf { it in 1..65535 }
    }.getOrNull()

    @Suppress("DEPRECATION")
    private fun resolveLocalPort(nsd: NsdManager, info: NsdServiceInfo, deadline: Long): Int? {
        val done = CountDownLatch(1)
        var result: Int? = null
        nsd.resolveService(info, object : NsdManager.ResolveListener {
            override fun onResolveFailed(serviceInfo: NsdServiceInfo, errorCode: Int) { done.countDown() }
            override fun onServiceResolved(serviceInfo: NsdServiceInfo) {
                if (isLocalAddress(serviceInfo.host)) {
                    result = serviceInfo.port.takeIf { it in 1..65535 }
                }
                done.countDown()
            }
        })
        val remaining = (deadline - System.nanoTime()).coerceAtLeast(0)
        done.await(minOf(remaining, TimeUnit.SECONDS.toNanos(1)), TimeUnit.NANOSECONDS)
        return result
    }

    internal fun isLocalAddress(address: InetAddress?): Boolean {
        if (address == null || address.isAnyLocalAddress) return false
        if (address.isLoopbackAddress) return true
        return runCatching {
            NetworkInterface.getNetworkInterfaces().asSequence().any { network ->
                network.inetAddresses.asSequence().any { it == address }
            }
        }.getOrDefault(false)
    }
}
