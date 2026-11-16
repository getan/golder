package com.getan.golder.mobile.androiduse

import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import java.net.InetAddress
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assume.assumeTrue
import org.junit.Test
import org.junit.runner.RunWith

@RunWith(AndroidJUnit4::class)
class LocalAdbDiscoveryTest {
    @Test
    fun onlyAcceptsLocalAddresses() {
        assertTrue(LocalAdbDiscovery.isLocalAddress(InetAddress.getByName("127.0.0.1")))
        assertTrue(LocalAdbDiscovery.isLocalAddress(InetAddress.getByName("::1")))
        assertFalse(LocalAdbDiscovery.isLocalAddress(null))
        assertFalse(LocalAdbDiscovery.isLocalAddress(InetAddress.getByName("0.0.0.0")))
        assertFalse(LocalAdbDiscovery.isLocalAddress(InetAddress.getByName("192.0.2.1")))
    }

    @Test
    fun discoversCurrentWirelessPortWhenProvided() {
        val instrumentation = InstrumentationRegistry.getInstrumentation()
        val port = InstrumentationRegistry.getArguments().getString("expectedPort").orEmpty()
        assumeTrue("expectedPort is required for the device-specific discovery check", port.isNotBlank())
        assertEquals("127.0.0.1:$port", LocalAdbDiscovery.findConnectAddress(instrumentation.targetContext))
    }
}
