package com.getan.golder.mobile.androiduse

import android.app.Activity
import android.content.Intent
import android.os.Bundle
import android.provider.Settings
import android.widget.Button
import android.widget.LinearLayout
import android.widget.ScrollView
import android.widget.TextView
import com.getan.golder.mobile.SettingsStore
import java.util.concurrent.Executors

/** Setup screen for background phone control. */
class Phase2SetupActivity : Activity() {
    private val worker = Executors.newSingleThreadExecutor()
    private lateinit var status: TextView
    private lateinit var pairWithSystemButton: Button
    private lateinit var startButton: Button
    private lateinit var stopButton: Button
    private val chinese get() = SettingsStore.load(this).language == "zh"

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val pad = (16 * resources.displayMetrics.density).toInt()
        val root = LinearLayout(this).apply {
            orientation = LinearLayout.VERTICAL
            setPadding(pad, pad, pad, pad)
        }
        button(root, if (chinese) "返回" else "Back") { finish() }
        root.addView(TextView(this).apply {
            text = if (chinese) "后台手机控制" else "Background phone control"
            textSize = 20f
        })
        root.addView(TextView(this).apply {
            text = if (chinese) {
                "首次使用时，在系统开发者选项中打开无线调试并选择“使用配对码配对设备”。Golder 会通过无障碍服务读取配对信息并自动连接。"
            } else {
                "For first-time setup, open Wireless debugging in Developer options and choose Pair device with pairing code. Golder reads the pairing details through accessibility and connects automatically."
            }
            textSize = 13f
            setPadding(0, pad / 2, 0, pad)
        })
        status = TextView(this).apply {
            text = if (Phase2Controller.shell() != null) {
                if (chinese) "后台控制已连接" else "Background control connected"
            } else {
                if (chinese) "后台控制未连接" else "Background control not connected"
            }
            setPadding(0, pad, 0, pad)
        }
        root.addView(status)
        pairWithSystemButton = button(root, if (chinese) "打开系统配对" else "Open system pairing") { pairWithSystem() }
        startButton = button(root, if (chinese) "连接后台控制" else "Connect background control") { busy { start() } }
        stopButton = button(root, if (chinese) "停止后台控制" else "Stop background control") { stop() }
        setContentView(ScrollView(this).apply { addView(root) })
    }

    private fun pairWithSystem() {
        Phase2PairingCoordinator.start(this)
        show(if (chinese) "请打开系统配对码弹窗，保持弹窗开启。" else "Keep the system pairing dialog open.")
        runCatching {
            startActivity(Intent(Settings.ACTION_APPLICATION_DEVELOPMENT_SETTINGS))
        }.onFailure {
            show("Open Developer options > Wireless debugging manually.")
        }
    }

    private fun start() {
        val result = Phase2Controller.startAutomatically(this)
        show(result.fold({ if (chinese) "后台控制已连接" else "Background control connected" }, { "Connection failed: ${it.message}" }))
    }

    private fun stop() {
        Phase2PairingCoordinator.stop()
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
            pairWithSystemButton.isEnabled = enabled
            startButton.isEnabled = enabled
            stopButton.isEnabled = enabled
        }
    }

    private fun updateButtons() {
        startButton.isEnabled = Phase2Controller.isPaired(this)
        stopButton.isEnabled = true
    }

    override fun onResume() {
        super.onResume()
        if (Phase2PairingCoordinator.status.isNotEmpty()) show(Phase2PairingCoordinator.status)
        updateButtons()
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
