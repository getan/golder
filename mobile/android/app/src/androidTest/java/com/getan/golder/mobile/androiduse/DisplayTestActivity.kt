package com.getan.golder.mobile.androiduse

import android.app.Activity
import android.graphics.Color
import android.os.Bundle
import android.os.Handler
import android.os.Looper
import android.view.MotionEvent
import android.view.View

/** Isolated activity owned by the test APK, never a user's existing app task. */
class DisplayTestActivity : Activity() {
    private val handler = Handler(Looper.getMainLooper())
    private lateinit var content: View
    private var red = true
    private var startY = 0f
    private var moved = false
    private val animate = object : Runnable {
        override fun run() {
            red = !red
            content.setBackgroundColor(if (red) Color.RED else Color.GREEN)
            handler.postDelayed(this, 1_200)
        }
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        content = View(this).apply {
            setBackgroundColor(Color.RED)
            setOnTouchListener { view, event ->
                when (event.actionMasked) {
                    MotionEvent.ACTION_DOWN -> {
                        startY = event.y
                        moved = false
                        true
                    }
                    MotionEvent.ACTION_MOVE -> {
                        if (kotlin.math.abs(event.y - startY) > 20) moved = true
                        true
                    }
                    MotionEvent.ACTION_UP -> {
                        handler.removeCallbacks(animate)
                        view.setBackgroundColor(if (moved) Color.YELLOW else Color.BLUE)
                        true
                    }
                    else -> true
                }
            }
        }
        setContentView(content)
        handler.postDelayed(animate, 1_200)
    }

    override fun onDestroy() {
        handler.removeCallbacks(animate)
        super.onDestroy()
    }
}
