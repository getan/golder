package com.getan.golder.mobile.androiduse

import android.graphics.Bitmap
import android.graphics.Canvas
import android.graphics.Paint
import android.graphics.PixelFormat
import android.graphics.Rect
import android.media.ImageReader
import android.os.Handler
import android.os.HandlerThread
import android.os.SystemClock
import android.util.Log
import java.nio.ByteBuffer

/**
 * Kotlin-side virtual display adapter backed by the restricted shell helper.
 */
class Phase2Display(
    private val shell: IGolderShell,
    val width: Int,
    val height: Int,
    private val dpi: Int,
) : AutoCloseable {
    private val reader = ImageReader.newInstance(width, height, PixelFormat.RGBA_8888, 2)
    @Volatile private var previewThread: HandlerThread? = null
    @Volatile private var closed = false
    @Volatile private var previewFrames = 0L
    private var rawFrame: Bitmap? = null
    private var paddedBuffer: ByteBuffer? = null
    private val previewPaint = Paint(Paint.FILTER_BITMAP_FLAG)

    val displayId: Int = shell.createDisplay(reader.surface, width, height, dpi)

    /** Starts a bounded in-memory preview after the target app has launched. */
    @Synchronized
    fun startPreview(onFrame: (Bitmap) -> Unit) {
        if (closed || previewThread != null || displayId < 0) return
        val worker = HandlerThread("golder-background-preview").apply { start() }
        previewThread = worker
        val handler = Handler(worker.looper)
        var pending = false
        var lastFrameAt = 0L
        fun scheduleFrame() {
            if (!closed && !pending) {
                pending = true
                val delay = (lastFrameAt + 200 - SystemClock.uptimeMillis()).coerceAtLeast(0)
                handler.postDelayed({
                    pending = false
                    synchronized(this) {
                        if (!closed) {
                            val image = runCatching { reader.acquireLatestImage() }.getOrNull()
                            if (image != null) {
                                try {
                                    val frame = previewBitmap(image.planes[0])
                                    lastFrameAt = SystemClock.uptimeMillis()
                                    previewFrames++
                                    onFrame(frame)
                                } catch (e: Exception) {
                                    Log.w("GolderPreview", "Unable to render background frame", e)
                                } finally {
                                    image.close()
                                }
                            }
                        }
                    }
                }, delay)
            }
        }
        reader.setOnImageAvailableListener({ scheduleFrame() }, handler)
        // The display may already have queued its only frame before the
        // listener was installed (for example, a static screen).
        handler.post { scheduleFrame() }
    }

    private fun previewBitmap(plane: android.media.Image.Plane): Bitmap {
        require(plane.pixelStride == 4 && plane.rowStride >= width * 4 && plane.rowStride % 4 == 0)
        val paddedWidth = plane.rowStride / plane.pixelStride
        val byteCount = plane.rowStride * height
        val source = plane.buffer.duplicate().apply { rewind() }
        require(source.remaining() >= plane.rowStride * (height - 1) + width * plane.pixelStride)
        val pixels = if (source.remaining() >= byteCount) source else {
            val buffer = paddedBuffer?.takeIf { it.capacity() == byteCount }
                ?: ByteBuffer.allocate(byteCount).also { paddedBuffer = it }
            buffer.clear()
            buffer.put(source)
            buffer.position(byteCount)
            buffer.flip()
            buffer
        }
        val raw = rawFrame?.takeIf { it.width == paddedWidth && it.height == height } ?: run {
            rawFrame?.recycle()
            Bitmap.createBitmap(paddedWidth, height, Bitmap.Config.ARGB_8888).also { rawFrame = it }
        }
        raw.copyPixelsFromBuffer(pixels)
        val scale = minOf(1f, 360f / width, 720f / height)
        val outWidth = (width * scale).toInt().coerceAtLeast(1)
        val outHeight = (height * scale).toInt().coerceAtLeast(1)
        return Bitmap.createBitmap(outWidth, outHeight, Bitmap.Config.ARGB_8888).also { output ->
            Canvas(output).drawBitmap(raw, Rect(0, 0, width, height), Rect(0, 0, outWidth, outHeight), previewPaint)
        }
    }

    fun waitForFrame(timeoutMs: Long): Boolean {
        val deadline = SystemClock.uptimeMillis() + timeoutMs
        while (SystemClock.uptimeMillis() < deadline) {
            val available = synchronized(this) {
                if (closed) return false
                if (previewThread != null) previewFrames > 0 else {
                    reader.acquireLatestImage()?.let { image ->
                        image.close()
                        true
                    } ?: false
                }
            }
            if (available) return true
            Thread.sleep(50)
        }
        return false
    }

    @Synchronized
    override fun close() {
        if (closed) return
        closed = true
        BackgroundPreview.end(this)
        runCatching { reader.setOnImageAvailableListener(null, null) }
        previewThread?.quitSafely()
        previewThread = null
        rawFrame?.recycle()
        rawFrame = null
        paddedBuffer = null
        if (displayId >= 0) runCatching { shell.releaseDisplay(displayId) }
        reader.close()
    }
}
