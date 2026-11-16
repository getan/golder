package com.getan.golder.mobile

import android.util.Log
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.getan.golder.mobile.androiduse.AndroidUseBridge
import com.getan.golder.mobile.androiduse.Phase2Controller
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

/**
 * Exercises background control's phone-local wireless-debugging path: the app's own Go ADB
 * client pairs with the device's pairing port from inside the app process,
 * then starts the shell helper. Run while Android's pairing dialog is open.
 */
@RunWith(AndroidJUnit4::class)
class Phase2SelfPairTest {
    @Test(timeout = 90_000)
    fun pairsAndStartsHelperLocally() {
        val args = InstrumentationRegistry.getArguments()
        val pairAddress = args.getString("pairAddress").orEmpty()
        val pairCode = args.getString("pairCode").orEmpty()
        val connectAddress = args.getString("connectAddress").orEmpty()
        assertTrue("pairAddress is required", pairAddress.isNotBlank())
        assertTrue("pairCode is required", pairCode.isNotBlank())
        assertTrue("connectAddress is required", connectAddress.isNotBlank())

        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val wasAdvanced = AndroidUseBridge.advancedEnabled(context)
        AndroidUseBridge.setAdvancedEnabled(context, true)
        try {
            val pair = Phase2Controller.pair(context, pairAddress, pairCode)
            assertTrue("pair failed: ${pair.exceptionOrNull()?.message}", pair.isSuccess)
            assertTrue("paired state was not persisted", Phase2Controller.isPaired(context))
            Log.i("Phase2SelfPairTest", "phone-local ADB pairing succeeded")

            val start = Phase2Controller.start(context, connectAddress)
            assertTrue("helper start failed: ${start.exceptionOrNull()?.message}", start.isSuccess)
            assertNotNull("helper Binder was not registered", Phase2Controller.shell())
            Log.i("Phase2SelfPairTest", "shell helper handoff succeeded")
        } finally {
            Phase2Controller.stop(context)
            if (!wasAdvanced) AndroidUseBridge.setAdvancedEnabled(context, false)
        }
    }

    @Test(timeout = 60_000)
    fun startsAlreadyPairedHelper() {
        val args = InstrumentationRegistry.getArguments()
        val connectAddress = args.getString("connectAddress").orEmpty()
        assertTrue("connectAddress is required", connectAddress.isNotBlank())

        val context = InstrumentationRegistry.getInstrumentation().targetContext
        assertTrue("wireless debugging is not paired", Phase2Controller.isPaired(context))
        val wasAdvanced = AndroidUseBridge.advancedEnabled(context)
        AndroidUseBridge.setAdvancedEnabled(context, true)
        try {
            val start = Phase2Controller.start(context, connectAddress)
            assertTrue("helper start failed: ${start.exceptionOrNull()?.message}", start.isSuccess)
            assertNotNull("helper Binder was not registered", Phase2Controller.shell())
            Log.i("Phase2SelfPairTest", "already-paired helper handoff succeeded")
        } finally {
            Phase2Controller.stop(context)
            if (!wasAdvanced) AndroidUseBridge.setAdvancedEnabled(context, false)
        }
    }
}
