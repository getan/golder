package com.getan.golder.mobile.androiduse

import android.graphics.PixelFormat
import android.media.ImageReader
import android.os.SystemClock

/**
 * Narrow Kotlin-side display adapter. The real helper will provide
 * IGolderShell; until then this class is deliberately not started by the UI.
 */
class Phase2Display(
    private val shell: IGolderShell,
    private val width: Int,
    private val height: Int,
    private val dpi: Int,
) : AutoCloseable {
    private val reader = ImageReader.newInstance(width, height, PixelFormat.RGBA_8888, 2)

    val displayId: Int = shell.createDisplay(reader.surface, width, height, dpi)

    fun waitForFrame(timeoutMs: Long): Boolean {
        val deadline = SystemClock.uptimeMillis() + timeoutMs
        while (SystemClock.uptimeMillis() < deadline) {
            val image = reader.acquireLatestImage()
            if (image != null) {
                image.close()
                return true
            }
            Thread.sleep(50)
        }
        return false
    }

    override fun close() {
        if (displayId >= 0) shell.releaseDisplay(displayId)
        reader.close()
    }
}
