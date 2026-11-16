package com.getan.golder.mobile

import android.util.Log
import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import com.getan.golder.mobile.androiduse.AndroidUseBridge
import com.getan.golder.mobile.androiduse.Phase2Controller
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Assert.fail
import org.junit.Test
import org.junit.runner.RunWith
import java.io.File

/**
 * Background-control device E2E with the real configured provider. This uses the same
 * [GolderNative.open] session boundary as MainActivity, but reads the app's
 * saved provider/model and Keystore API key instead of starting a fake server.
 */
@RunWith(AndroidJUnit4::class)
class Phase2AgentE2ETest {
    private val context get() = InstrumentationRegistry.getInstrumentation().targetContext

    @Test(timeout = 120_000)
    fun realProviderResponds() {
        val settings = SettingsStore.load(context)
        requireConfigured(settings)
        val root = newRoot("smoke")
        val debug = File(root, "debug.log")
        var passed = false
        try {
            val outcome = runPrompt(
                baseConfig(settings, root, debug),
                "Reply with exactly OK and nothing else. Do not call any tools.",
                90_000,
            )
            Log.i(TAG, "smoke ended=${outcome.ended} error=${outcome.error} text=${outcome.text.take(200)}")
            if (!outcome.ended) fail("provider run did not finish: ${outcome.error}; debug=${outcome.debugTail}")
            if (outcome.error.isNotBlank()) fail("provider run error: ${outcome.error}")
            assertTrue("provider did not return OK: ${outcome.text.take(300)}", outcome.text.contains("OK"))
            passed = true
        } finally {
            if (passed) {
                root.deleteRecursively()
            } else {
                Log.e(TAG, "smoke failed; debug log retained at ${debug.absolutePath}")
            }
        }
    }

    @Test(timeout = 300_000)
    fun realProviderRunsFullPhase2Sequence() {
        val settings = SettingsStore.load(context)
        requireConfigured(settings)
        val endpoint = File(AndroidUseBridge.endpointPath(context))
        assertTrue("advanced phone control is disabled", AndroidUseBridge.advancedEnabled(context))
        assertTrue("Phase 2 is not paired; run Phase2SelfPairTest first", Phase2Controller.isPaired(context))
        assertTrue("accessibility endpoint file is missing", endpoint.isFile)
        assertTrue(
            "accessibility endpoint is not live; rebind the accessibility service",
            awaitEndpointLive(30_000),
        )

        val originalAllowed = AndroidUseBridge.allowedPackages(context)
        val changedAllowed = originalAllowed.isNotEmpty() && "com.android.settings" !in originalAllowed
        if (changedAllowed) {
            AndroidUseBridge.setAllowedPackages(
                context,
                (originalAllowed + "com.android.settings").joinToString(","),
            )
        }
        val root = newRoot("full")
        val debug = File(root, "debug.log")
        var passed = false
        try {
            val config = baseConfig(settings, root, debug)
                .put("androidUseEndpoint", endpoint.absolutePath)
                .put("androidUsePhase2", true)
            val outcome = runPrompt(config, PHASE2_PROMPT, 240_000)
            Log.i(TAG, "phase2 ended=${outcome.ended} error=${outcome.error} events=${outcome.events.size}")
            if (!outcome.ended) {
                fail("agent run did not finish: ${outcome.error}; debug=${outcome.debugTail}")
            }
            if (outcome.error.isNotBlank()) fail("agent run error: ${outcome.error}")
            assertPhase2Sequence(outcome.events)
            assertTrue("helper is still connected after phase2_stop", Phase2Controller.shell() == null)
            passed = true
        } finally {
            runCatching { Phase2Controller.stop(context) }
            if (changedAllowed) {
                AndroidUseBridge.setAllowedPackages(context, originalAllowed.joinToString(","))
            }
            if (!passed) {
                Log.e(TAG, "E2E failed; debug log retained at ${debug.absolutePath}")
            } else {
                root.deleteRecursively()
            }
        }
    }

    private fun requireConfigured(settings: SavedSettings) {
        assertTrue("No provider configured in Golder settings", settings.provider.isNotBlank())
        assertTrue("No model configured in Golder settings", settings.model.isNotBlank())
        Log.i(
            TAG,
            "provider=${settings.provider} model=${settings.model} " +
                "baseUrlSet=${settings.baseUrl.isNotBlank()} " +
                "apiKeySet=${SecureStore.getAPIKey(context).isNotBlank()} thinking=${settings.thinking}",
        )
    }

    private fun newRoot(label: String): File =
        File(context.filesDir, "phase2-e2e-$label-${System.nanoTime()}").apply { mkdirs() }

    private fun awaitEndpointLive(timeoutMs: Long): Boolean {
        val deadline = System.currentTimeMillis() + timeoutMs
        while (System.currentTimeMillis() < deadline) {
            if (AndroidUseBridge.endpointLive(context)) return true
            Thread.sleep(500)
        }
        return false
    }

    private fun baseConfig(settings: SavedSettings, root: File, debug: File): JSONObject =
        JSONObject()
            .put("workspace", root.absolutePath)
            .put("provider", settings.provider)
            .put("model", settings.model)
            .put("thinkingLevel", settings.thinking)
            .put("permissionMode", "auto")
            .put("apiKey", SecureStore.getAPIKey(context))
            .put("debugLog", debug.absolutePath)
            .apply {
                if (settings.baseUrl.isNotBlank()) put("baseUrl", settings.baseUrl.trim())
            }

    private fun runPrompt(config: JSONObject, prompt: String, timeoutMs: Long): RunOutcome {
        var handle = 0
        val events = mutableListOf<JSONObject>()
        val text = StringBuilder()
        var ended = false
        var error = ""
        var debugTail = ""
        try {
            handle = GolderNative.open(config.toString())
            if (handle <= 0) {
                return RunOutcome(events, text.toString(), false, "open failed: ${GolderNative.lastError()}", "")
            }
            val start = GolderNative.start(handle, prompt)
            if (start != 0) {
                return RunOutcome(events, text.toString(), false, "start failed: ${GolderNative.lastError()}", "")
            }
            val deadline = System.currentTimeMillis() + timeoutMs
            while (!ended && System.currentTimeMillis() < deadline) {
                val raw = GolderNative.poll(handle, 200)
                if (raw == null) {
                    error = "poll failed: ${GolderNative.lastError()}"
                    break
                }
                val batch = JSONArray(raw)
                for (i in 0 until batch.length()) {
                    val event = batch.getJSONObject(i)
                    events.add(event)
                    when (event.optString("type")) {
                        "text" -> text.append(event.optString("text"))
                        "run_end" -> {
                            ended = true
                            error = event.optString("error")
                        }
                    }
                    Log.i(TAG, "event ${eventSummary(event)}")
                }
                if (!ended) Thread.sleep(100)
            }
            if (!ended && error.isBlank()) error = "timed out after ${timeoutMs}ms"
        } catch (t: Throwable) {
            error = t.message ?: t.javaClass.simpleName
        } finally {
            if (handle > 0) runCatching { GolderNative.close(handle) }
            debugTail = runCatching {
                val path = config.optString("debugLog")
                if (path.isBlank()) "" else File(path).takeIf { it.isFile }?.readText()?.takeLast(4000).orEmpty()
            }.getOrDefault("")
        }
        return RunOutcome(events, text.toString(), ended, error, debugTail)
    }

    private fun assertPhase2Sequence(events: List<JSONObject>) {
        val starts = events.filter {
            it.optString("type") == "tool_start" && it.optString("toolName") == "android_use"
        }
        val ends = events.filter {
            it.optString("type") == "tool_end" && it.optString("toolName") == "android_use"
        }.associateBy { it.optString("toolCallId") }
        val actions = starts.map { toolArgs(it).optString("action") }
        Log.i(TAG, "android_use actions=$actions")

        var cursor = 0
        for (action in actions) {
            if (cursor < REQUIRED.size && action == REQUIRED[cursor]) cursor++
        }
        assertEquals("Phase 2 sequence incomplete; got $actions", REQUIRED.size, cursor)

        for (action in REQUIRED) {
            val start = starts.lastOrNull { toolArgs(it).optString("action") == action }
            if (start == null) {
                fail("missing android_use action $action")
                return
            }
            val end = ends[start.optString("toolCallId")]
            if (end == null) {
                fail("missing tool_end for $action")
                return
            }
            assertFalse("$action failed: ${end.optString("text")}", end.optBoolean("isError", false))
            val result = runCatching { JSONObject(end.optString("text")) }.getOrNull()
            if (result == null) {
                fail("$action returned non-JSON: ${end.optString("text").take(500)}")
                return
            }
            assertTrue("$action result is not ok: ${end.optString("text").take(500)}", result.optBoolean("ok"))
            assertEquals("$action result action mismatch", action, result.optString("action"))
            if (action == "background_open_app") {
                assertTrue("background_open_app did not start an app", result.optBoolean("started"))
            }
            if (action == "background_stop") {
                assertFalse("background_stop left helper connected", result.optBoolean("helperConnected", true))
            }
        }
    }

    private fun toolArgs(event: JSONObject): JSONObject {
        event.optJSONObject("args")?.let { return it }
        val raw = event.optString("args")
        return runCatching { JSONObject(raw) }.getOrElse { JSONObject() }
    }

    private fun eventSummary(event: JSONObject): String {
        val type = event.optString("type")
        return when (type) {
            "tool_start" -> "$type ${event.optString("toolName")} ${event.optString("args").take(400)}"
            "tool_end" -> "$type ${event.optString("toolName")} " +
                "error=${event.optBoolean("isError", false)} ${event.optString("text").take(400)}"
            "run_end" -> "$type error=${event.optString("error")}"
            "text" -> "$type ${event.optString("text").take(200)}"
            else -> type
        }
    }

    private data class RunOutcome(
        val events: List<JSONObject>,
        val text: String,
        val ended: Boolean,
        val error: String,
        val debugTail: String,
    )

    companion object {
        private const val TAG = "Phase2AgentE2E"
        private val REQUIRED = listOf(
            "background_status",
            "background_start",
            "background_open_app",
            "background_tap",
            "background_release",
            "background_stop",
        )
        private val PHASE2_PROMPT = """
            Use the android_use tool exactly six times, one action per call, in this exact order:
            1. {"action":"background_status"}
            2. {"action":"background_start"}
            3. {"action":"background_open_app","packageName":"com.android.settings"}
            4. {"action":"background_tap","x":100,"y":100}
            5. {"action":"background_release"}
            6. {"action":"background_stop"}
            Do not call observe or any other tool. Do not ask for confirmation. Continue through all six calls even if one returns an error, then reply with one line per result.
        """.trimIndent()
    }
}
