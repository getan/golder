package com.getan.golder.mobile.androiduse

import android.app.Activity
import android.graphics.Color
import android.os.Bundle
import android.text.InputType
import android.view.ViewGroup
import android.widget.Button
import android.widget.EditText
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import java.util.concurrent.Executors

class Phase2SetupActivity : Activity() {
    private val worker = Executors.newSingleThreadExecutor()
    private lateinit var pairAddress: EditText
    private lateinit var pairCode: EditText
    private lateinit var connectAddress: EditText
    private lateinit var status: TextView
    private lateinit var pairButton: Button
    private lateinit var startButton: Button
    private lateinit var stopButton: Button

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val pad = (16 * resources.displayMetrics.density).toInt()
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(pad, pad, pad, pad)
        }
        root.addView(TextView(this).apply {
            text = "Phase 2 · wireless debugging helper"
            textSize = 20f
        })
        root.addView(TextView(this).apply {
            text = "Enable advanced control in Golder settings first. Use the pairing address/code shown by Android; the connect address is the separate wireless-debugging port."
            textSize = 13f
            setPadding(0, pad / 2, 0, pad)
        })
        pairAddress = field(root, "Pairing address (host:pairing-port)", Phase2Controller.pairAddress(this))
        pairCode = field(root, "Pairing code", "").apply {
            inputType = InputType.TYPE_CLASS_NUMBER
        }
        connectAddress = field(root, "Connect address (host:connect-port)", Phase2Controller.connectAddress(this))
        status = TextView(this).apply {
            text = if (Phase2Controller.shell() != null) "helper connected" else "helper not connected"
            setPadding(0, pad, 0, pad)
        }
        root.addView(status)
        pairButton = button(root, "Pair") { busy { pair() } }
        startButton = button(root, "Start helper") { busy { start() } }
        stopButton = button(root, "Stop and clear") { stop() }
        setContentView(ScrollView(this).apply { addView(root) })
    }

    private fun pair() {
        val pairAddr = pairAddress.text.toString()
        val code = pairCode.text.toString()
        val connectAddr = connectAddress.text.toString()
        Phase2Controller.rememberAddresses(this, pairAddr, connectAddr)
        val result = Phase2Controller.pair(this, pairAddr, code)
        show(result.fold({ "Paired. Now start the helper." }, { "Pair failed: ${it.message}" }))
    }

    private fun start() {
        val connectAddr = connectAddress.text.toString()
        Phase2Controller.rememberAddresses(this, pairAddress.text.toString(), connectAddr)
        val result = Phase2Controller.start(this, connectAddr)
        show(result.fold({ "Helper connected. Display is ready for Go policy." }, { "Start failed: ${it.message}" }))
        updateButtons()
    }

    private fun stop() {
        Phase2Controller.stop(this)
        show("Stopped; helper, display and nonce cleared.")
        updateButtons()
    }

    private fun busy(block: () -> Unit) {
        setButtons(false)
        worker.execute {
            runCatching(block).onFailure { show("Error: ${it.message}") }
            runOnUiThread { setButtons(true); updateButtons() }
        }
    }

    private fun show(message: String) {
        runOnUiThread { status.text = message }
    }

    private fun setButtons(enabled: Boolean) {
        runOnUiThread {
            pairButton.isEnabled = enabled
            startButton.isEnabled = enabled
            stopButton.isEnabled = enabled
        }
    }

    private fun updateButtons() {
        startButton.isEnabled = Phase2Controller.shell() != null
        stopButton.isEnabled = true
    }

    private fun field(root: LinearLayout, hint: String, value: String): EditText =
        EditText(this).apply {
            this.hint = hint
            setText(value)
            inputType = InputType.TYPE_CLASS_TEXT
            layoutParams = LinearLayout.LayoutParams(
                ViewGroup.LayoutParams.MATCH_PARENT,
                ViewGroup.LayoutParams.WRAP_CONTENT,
            )
            root.addView(this)
        }

    private fun button(root: LinearLayout, label: String, action: () -> Unit): Button =
        Button(this).apply {
            text = label
            setOnClickListener { action() }
            root.addView(this)
        }

    override fun onDestroy() {
        worker.shutdownNow()
        super.onDestroy()
    }
}
