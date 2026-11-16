package com.getan.golder.mobile.androiduse

import android.content.Context
import android.os.SystemClock
import com.getan.golder.mobile.GolderNative
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.launch

/** Host-only handoff. The model cannot grant itself or end this lease. */
internal object ManualTakeover {
    enum class Stage { IDLE, WAITING, ACTIVE, RETURNING }
    data class State(val stage: Stage = Stage.IDLE, val displayId: Int = -1, val error: String = "")

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.IO)
    private val mutableState = MutableStateFlow(State())
    val state: StateFlow<State> = mutableState.asStateFlow()
    private var pausedHandle = 0
    @Volatile private var cancelled = false

    @Synchronized
    fun request(context: Context, handle: Int, displayId: Int) {
        if (mutableState.value.stage != Stage.IDLE) return
        cancelled = false
        pausedHandle = handle
        mutableState.value = State(Stage.WAITING, displayId)
        val app = context.applicationContext
        scope.launch {
            var paused = false
            var acquired = false
            try {
                if (handle != 0) {
                    check(GolderNative.pauseForTakeover(handle) == 0) { "Could not pause the agent" }
                    paused = true
                    val deadline = SystemClock.elapsedRealtime() + 60_000
                    while (true) {
                        check(!cancelled) { "Takeover cancelled" }
                        val ready = GolderNative.takeoverReady(handle)
                        check(ready >= 0) { "The agent session is no longer available" }
                        if (ready == 1) break
                        check(SystemClock.elapsedRealtime() < deadline) { "Timed out waiting for the current action to finish" }
                        delay(100)
                    }
                }
                check(!cancelled) { "Takeover cancelled" }
                acquired = Phase2Session.beginManual(app, displayId)
                check(acquired) { "The background display is no longer available" }
                synchronized(this@ManualTakeover) {
                    check(!cancelled) { "Takeover cancelled" }
                    pausedHandle = if (paused) handle else 0
                    mutableState.value = State(Stage.ACTIVE, displayId)
                }
            } catch (e: Exception) {
                if (acquired) Phase2Session.endManual()
                if (paused) runCatching { GolderNative.resumeAfterTakeover(handle) }
                synchronized(this@ManualTakeover) {
                    pausedHandle = 0
                    mutableState.value = State(error = e.message.orEmpty())
                }
            }
        }
    }

    @Synchronized
    fun cancelWaiting() {
        if (mutableState.value.stage == Stage.WAITING) cancelled = true
    }

    @Synchronized
    fun retainedHandle(): Int = if (mutableState.value.stage == Stage.IDLE) 0 else pausedHandle

    @Synchronized
    fun retainsHandle(handle: Int): Boolean = handle != 0 && retainedHandle() == handle

    @Synchronized
    fun handBack() {
        val current = mutableState.value
        if (current.stage != Stage.ACTIVE) return
        mutableState.value = current.copy(stage = Stage.RETURNING, error = "")
        val handle = pausedHandle
        scope.launch {
            // Waits for an in-flight manual gesture before releasing the lease.
            Phase2Session.endManual()
            val resumed = handle == 0 || runCatching { GolderNative.resumeAfterTakeover(handle) == 0 }.getOrDefault(false)
            synchronized(this@ManualTakeover) {
                pausedHandle = 0
                mutableState.value = State(error = if (resumed) "" else "The previous agent session is no longer available")
            }
        }
    }

    @Synchronized
    fun inputFailed() {
        val current = mutableState.value
        if (current.stage == Stage.ACTIVE) {
            mutableState.value = current.copy(error = "Input failed; check the display before trying again")
        }
    }
}
