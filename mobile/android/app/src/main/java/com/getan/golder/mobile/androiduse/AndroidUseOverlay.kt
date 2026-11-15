package com.getan.golder.mobile.androiduse

import android.content.Intent
import android.graphics.Bitmap
import android.graphics.BitmapFactory
import android.graphics.Color
import android.graphics.PixelFormat
import android.graphics.drawable.GradientDrawable
import android.os.Handler
import android.os.Looper
import android.view.Gravity
import android.view.View
import android.view.WindowManager
import android.widget.Button
import android.widget.ImageView
import android.widget.LinearLayout
import android.widget.TextView
import com.getan.golder.mobile.MainActivity
import java.io.File
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit

/**
 * Accessibility overlay for live AndroidUse status, a screenshot preview, and
 * exact approval of sensitive actions. It is intentionally non-focusable so
 * the user can keep using the app underneath.
 */
class AndroidUseOverlay(private val service: GolderAccessibilityService) {
    private val main = Handler(Looper.getMainLooper())
    private val windowManager = service.getSystemService(WindowManager::class.java)
    private var root: LinearLayout? = null
    private var status: TextView? = null
    private var preview: ImageView? = null
    private var previewBitmap: Bitmap? = null
    private var toggle: Button? = null
    private var allow: Button? = null
    private var deny: Button? = null
    private var pending: CountDownLatch? = null
    @Volatile private var confirmationResult = false

    fun show() {
        if (root != null || windowManager == null) return
        val panel = LinearLayout(service).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(dp(12), dp(10), dp(12), dp(10))
            background = GradientDrawable().apply {
                setColor(Color.argb(235, 28, 28, 28))
                cornerRadius = dp(12).toFloat()
            }
        }
        val title = TextView(service).apply {
            text = "AndroidUse"
            setTextColor(Color.WHITE)
            textSize = 13f
            setTypeface(typeface, android.graphics.Typeface.BOLD)
        }
        status = TextView(service).apply {
            setTextColor(Color.LTGRAY)
            textSize = 11f
        }
        preview = ImageView(service).apply {
            layoutParams = LinearLayout.LayoutParams(dp(220), dp(280))
            scaleType = ImageView.ScaleType.FIT_CENTER
            visibility = View.GONE
        }
        val row = LinearLayout(service).apply {
            orientation = LinearLayout.HORIZONTAL
            gravity = Gravity.END
        }
        toggle = Button(service).apply {
            text = "Pause"
            textSize = 10f
            setOnClickListener {
                val paused = !AndroidUseBridge.isPaused(service)
                AndroidUseBridge.setPaused(service, paused)
                update("toggle", "", paused)
            }
            row.addView(this)
        }
        Button(service).apply {
            text = "Open"
            textSize = 10f
            setOnClickListener {
                service.startActivity(
                    Intent(service, MainActivity::class.java)
                        .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
                )
            }
            row.addView(this)
        }
        Button(service).apply {
            text = "Hide"
            textSize = 10f
            setOnClickListener {
                AndroidUseBridge.setOverlayEnabled(service, false)
            }
            row.addView(this)
        }
        deny = Button(service).apply {
            text = "Deny"
            textSize = 10f
            visibility = View.GONE
            setOnClickListener { answer(false) }
            row.addView(this)
        }
        allow = Button(service).apply {
            text = "Allow once"
            textSize = 10f
            visibility = View.GONE
            setOnClickListener { answer(true) }
            row.addView(this)
        }
        panel.addView(title)
        panel.addView(status)
        panel.addView(preview)
        panel.addView(row)
        root = panel
        val params = WindowManager.LayoutParams(
            WindowManager.LayoutParams.WRAP_CONTENT,
            WindowManager.LayoutParams.WRAP_CONTENT,
            WindowManager.LayoutParams.TYPE_ACCESSIBILITY_OVERLAY,
            WindowManager.LayoutParams.FLAG_NOT_FOCUSABLE or
                WindowManager.LayoutParams.FLAG_LAYOUT_NO_LIMITS,
            PixelFormat.TRANSLUCENT,
        ).apply {
            gravity = Gravity.TOP or Gravity.END
            x = dp(12)
            y = dp(80)
        }
        runCatching { windowManager.addView(panel, params) }.onFailure {
            root = null
            status = null
            preview = null
            toggle = null
            allow = null
            deny = null
        }
    }

    fun update(action: String, packageName: String, paused: Boolean, imagePath: String? = null) {
        main.post {
            if (root == null) return@post
            if (pending != null) return@post
            status?.text = buildString {
                append(if (paused) "PAUSED · " else "ACTIVE · ")
                append(action)
                if (packageName.isNotBlank()) append(" · ").append(packageName.take(36))
            }
            toggle?.text = if (paused) "Resume" else "Pause"
            if (imagePath != null) showPreview(imagePath)
        }
    }

    /**
     * Block the current action until the person presses Allow once or Deny.
     * Called from the server thread; the overlay buttons run on the main thread.
     */
    fun requestConfirmation(action: String, detail: String): Boolean {
        val latch = CountDownLatch(1)
        pending = latch
        confirmationResult = false
        main.post {
            if (root == null) {
                latch.countDown()
                return@post
            }
            status?.text = "Confirm $action: ${detail.take(80)}"
            toggle?.visibility = View.GONE
            allow?.visibility = View.VISIBLE
            deny?.visibility = View.VISIBLE
        }
        val confirmed = try {
            latch.await(15, TimeUnit.SECONDS) && confirmationResult
        } catch (_: InterruptedException) {
            false
        }
        pending = null
        main.post {
            allow?.visibility = View.GONE
            deny?.visibility = View.GONE
            toggle?.visibility = View.VISIBLE
            status?.text = if (confirmed) "Approved once" else "Denied"
        }
        return confirmed
    }

    fun close() {
        pending?.countDown()
        pending = null
        main.post {
            root?.let { runCatching { windowManager?.removeView(it) } }
            root = null
            previewBitmap?.recycle()
            previewBitmap = null
            status = null
            preview = null
            toggle = null
            allow = null
            deny = null
        }
    }

    private fun answer(value: Boolean) {
        confirmationResult = value
        pending?.countDown()
    }

    private fun showPreview(path: String) {
        val options = BitmapFactory.Options().apply { inJustDecodeBounds = true }
        BitmapFactory.decodeFile(path, options)
        var sample = 1
        while (options.outWidth / sample > 440 || options.outHeight / sample > 560) {
            sample *= 2
        }
        val bitmap = BitmapFactory.decodeFile(
            path,
            BitmapFactory.Options().apply { inSampleSize = sample },
        ) ?: return
        previewBitmap?.recycle()
        previewBitmap = bitmap
        preview?.setImageBitmap(bitmap)
        preview?.visibility = View.VISIBLE
    }

    private fun dp(value: Int): Int =
        (value * service.resources.displayMetrics.density).toInt()
}
