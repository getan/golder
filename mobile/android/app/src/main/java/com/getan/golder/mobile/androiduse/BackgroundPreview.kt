package com.getan.golder.mobile.androiduse

import android.graphics.Bitmap
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow

/** Latest in-memory frame only. The model receives a frame only on an explicit observe call. */
internal object BackgroundPreview {
    data class State(
        val displayId: Int,
        val packageName: String,
        val width: Int,
        val height: Int,
        val frame: Bitmap?,
    )

    private var owner: Any? = null
    private val mutableState = MutableStateFlow<State?>(null)
    val state: StateFlow<State?> = mutableState.asStateFlow()

    @Synchronized
    fun begin(display: Any, displayId: Int, packageName: String, width: Int, height: Int) {
        owner = display
        mutableState.value = State(displayId, packageName, width, height, null)
    }

    @Synchronized
    fun publish(display: Any, frame: Bitmap) {
        val current = mutableState.value
        if (owner === display && current != null) {
            mutableState.value = current.copy(frame = frame)
        }
    }

    @Synchronized
    fun end(display: Any) {
        if (owner === display) {
            owner = null
            mutableState.value = null
        }
    }
}
