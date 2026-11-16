package com.getan.golder.mobile.androiduse

import android.content.Context
import android.util.Log
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.Job
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/**
 * Coordinates phone-local wireless-debugging pairing while Android's Settings
 * pairing dialog is in the foreground. The accessibility service supplies the
 * dialog text; this coordinator calls the Go ADB client over loopback and then
 * starts the shell helper.
 */
object Phase2PairingCoordinator {
    private const val TAG = "Phase2Pairing"
    private const val WINDOW_TIMEOUT_MS = 5 * 60_000L
    private const val POLL_MS = 250L

    private val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)
    private var job: Job? = null

    @Volatile
    private var active = false

    @Volatile
    var status: String = ""
        private set

    val isActive: Boolean get() = active

    fun start(context: Context) {
        stop()
        val app = context.applicationContext
        active = true
        status = "Waiting for Android's pairing dialog..."
        val deadline = System.currentTimeMillis() + WINDOW_TIMEOUT_MS
        job = scope.launch {
            val tried = mutableSetOf<String>()
            while (active && System.currentTimeMillis() < deadline) {
                val service = GolderAccessibilityService.instance
                if (service == null) {
                    status = "Enable Golder's accessibility service first."
                    delay(1_000)
                    continue
                }
                val reading = PairingScreenParser.parse(service.pairingWindowTexts)
                if (reading != null && reading.code !in tried) {
                    tried += reading.code
                    val pairAddress = "127.0.0.1:${reading.pairingPort}"
                    Log.i(TAG, "pairing window found; pairing over localhost")
                    status = "Pairing over localhost..."
                    val paired = Phase2Controller.pair(app, pairAddress, reading.code)
                    if (paired.isFailure) {
                        status = "Pair failed: ${paired.exceptionOrNull()?.message}"
                        delay(1_000)
                        continue
                    }
                    status = "Paired; starting helper..."
                    // The pairing screen also shows the connect port; remember it
                    // instead of re-discovering a port the system may not expose.
                    reading.connectPort?.let { port ->
                        Phase2Controller.rememberAddresses(app, pairAddress, "127.0.0.1:$port")
                    }
                    val started = Phase2Controller.startAutomatically(app)
                    status = if (started.isSuccess) {
                        "Helper connected."
                    } else {
                        "Paired, but helper start failed: ${started.exceptionOrNull()?.message}"
                    }
                    active = false
                    return@launch
                }
                delay(POLL_MS)
            }
            if (active) status = "Timed out waiting for the pairing dialog."
            active = false
        }
    }

    fun stop() {
        active = false
        job?.cancel()
        job = null
    }
}

private data class PairingReading(
    val code: String,
    val pairingPort: Int,
    val connectPort: Int?,
)

private object PairingScreenParser {
    private val codePattern = Regex("""\d{6}""")
    private val ipv4PortPattern = Regex(
        """(?<![\w.:])(\d{1,3})\.(\d{1,3})\.(\d{1,3})\.(\d{1,3}):(\d{1,5})(?![\w.:])""",
    )

    fun parse(windows: List<List<String>>): PairingReading? {
        var code: String? = null
        var pairingPort: Int? = null
        val allPorts = mutableSetOf<Int>()
        for (texts in windows) {
            val codes = texts.mapNotNull { text ->
                text.filterNot { it.isWhitespace() }.takeIf { codePattern.matches(it) }
            }.toSet()
            val ports = texts.flatMap(::ports).toSet()
            allPorts += ports
            if (codes.size == 1 && ports.size == 1) {
                code = codes.first()
                pairingPort = ports.first()
            }
        }
        val foundCode = code ?: return null
        val foundPairPort = pairingPort ?: return null
        val foundConnectPort = allPorts.firstOrNull { it != foundPairPort }
        return PairingReading(foundCode, foundPairPort, foundConnectPort)
    }

    private fun ports(text: String): List<Int> =
        ipv4PortPattern.findAll(text).mapNotNull { match ->
            val octets = (1..4).map { match.groupValues[it].toIntOrNull() }
            if (octets.any { it == null || it !in 0..255 }) return@mapNotNull null
            match.groupValues[5].toIntOrNull()?.takeIf { it in 1..65535 }
        }.toList()
}
