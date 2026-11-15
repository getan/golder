package com.getan.golder.mobile

import androidx.test.ext.junit.runners.AndroidJUnit4
import androidx.test.platform.app.InstrumentationRegistry
import android.util.Log
import java.io.Closeable
import java.io.File
import java.io.InputStream
import java.net.InetAddress
import java.net.ServerSocket
import java.net.URL
import java.util.concurrent.atomic.AtomicBoolean
import kotlin.concurrent.thread
import org.json.JSONArray
import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import org.junit.runner.RunWith

/**
 * Device-side tests for the actual Kotlin -> JNI -> Go path. The model is a
 * loopback OpenAI-compatible SSE server, so no real credential or public
 * network is involved.
 */
@RunWith(AndroidJUnit4::class)
class JniSessionTest {
    @Test
    fun versionIsExposed() {
        assertEquals("0.1", GolderNative.version())
    }

    @Test
    fun githubTokenRoundTripsThroughAndroidKeystore() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val previous = SecureStore.getToken(context)
        try {
            SecureStore.clearToken(context)
            assertTrue(SecureStore.saveToken(context, "ghp_test_fixture"))
            assertEquals("ghp_test_fixture", SecureStore.getToken(context))
            SecureStore.clearToken(context)
            assertEquals("", SecureStore.getToken(context))
        } finally {
            if (previous.isBlank()) SecureStore.clearToken(context) else SecureStore.saveToken(context, previous)
        }
    }

    @Test
    fun fakeServerServesTheExpectedSSE() {
        val server = FakeChatServer()
        try {
            val conn = URL("http://127.0.0.1:${server.port}/v1/chat/completions")
                .openConnection() as java.net.HttpURLConnection
            conn.requestMethod = "POST"
            conn.doOutput = true
            conn.setRequestProperty("Content-Type", "application/json")
            conn.outputStream.use { it.write("{}".toByteArray()) }
            val body = conn.inputStream.bufferedReader().use { it.readText() }
            assertTrue("fake server body = $body", body.contains("hello from fake"))
        } finally {
            server.close()
        }
    }

    @Test
    fun streamsTextThroughJni() {
        val context = InstrumentationRegistry.getInstrumentation().targetContext
        val root = File(context.filesDir, "jni-test-${System.nanoTime()}").apply { mkdirs() }
        val debugFile = File(root, "debug.log")
        val server = FakeChatServer()
        try {
            val config = JSONObject()
                .put("workspace", root.absolutePath)
                .put("model", "fake-model")
                .put("protocol", "openai")
                .put("baseUrl", "http://127.0.0.1:${server.port}/v1")
                .put("apiKey", "test-key")
                .put("debugLog", debugFile.absolutePath)
            val handle = GolderNative.open(config.toString())
            Log.i("GolderJNI", "test open handle=$handle lastError=${GolderNative.lastError()}")
            assertTrue("open failed: ${GolderNative.lastError()}", handle > 0)
            try {
                assertEquals(0, GolderNative.start(handle, "say hello"))
                val text = StringBuilder()
                val seen = StringBuilder()
                var ended = false
                var failure = ""
                val deadline = System.currentTimeMillis() + 20_000
                while (!ended && System.currentTimeMillis() < deadline) {
                    val raw = GolderNative.poll(handle, 100)
                    if (raw == null) {
                        failure = GolderNative.lastError()
                        break
                    }
                    if (seen.length < 4000) seen.append(raw).append('\n')
                    val events = JSONArray(raw)
                    for (i in 0 until events.length()) {
                        val event = events.getJSONObject(i)
                        when (event.optString("type")) {
                            "text" -> text.append(event.optString("text"))
                            "run_end" -> {
                                ended = true
                                if (event.optString("error").isNotBlank()) {
                                    failure = event.optString("error")
                                }
                            }
                        }
                    }
                    if (!ended) Thread.sleep(50)
                }
                assertTrue("run ended with error: $failure", failure.isEmpty())
                val debug = if (debugFile.exists()) debugFile.readText().takeLast(3000) else "(no debug log)"
                assertTrue("run did not end; events=$seen; debug=$debug", ended)
                assertTrue("missing streamed text: $text", text.contains("hello from fake"))
            } finally {
                GolderNative.close(handle)
            }
        } finally {
            server.close()
            root.deleteRecursively()
        }
    }
}

/** Minimal HTTP server replaying one OpenAI Chat Completions SSE response. */
private class FakeChatServer : Closeable {
    private val socket = ServerSocket(0, 16, InetAddress.getByName("127.0.0.1"))
    private val closed = AtomicBoolean(false)
    val port: Int get() = socket.localPort

    init {
        thread(isDaemon = true, name = "fake-chat-server") {
            while (!closed.get()) {
                try {
                    val client = socket.accept()
                    try {
                        val input = client.getInputStream()
                        val requestLine = readAsciiLine(input) ?: continue
                        var length = 0
                        while (true) {
                            val line = readAsciiLine(input) ?: break
                            if (line.isEmpty()) break
                            if (line.startsWith("Content-Length:", ignoreCase = true)) {
                                length = line.substringAfter(':').trim().toIntOrNull() ?: 0
                            }
                        }
                        val requestBody = ByteArray(length)
                        var read = 0
                        while (read < length) {
                            val n = input.read(requestBody, read, length - read)
                            if (n < 0) break
                            read += n
                        }
                        Log.i("GolderJNI", "fake request: $requestLine len=$length")
                        val body = buildString {
                            append("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fake-model\",\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\",\"content\":\"hello from fake\"},\"finish_reason\":null}]}\n\n")
                            append("data: {\"id\":\"chatcmpl-1\",\"object\":\"chat.completion.chunk\",\"created\":1,\"model\":\"fake-model\",\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\n")
                            append("data: [DONE]\n\n")
                        }
                        val bodyBytes = body.toByteArray()
                        val header = "HTTP/1.1 200 OK\r\n" +
                            "Content-Type: text/event-stream\r\n" +
                            "Content-Length: ${bodyBytes.size}\r\n" +
                            "Cache-Control: no-cache\r\n" +
                            "Connection: close\r\n\r\n"
                        client.getOutputStream().apply {
                            write(header.toByteArray())
                            write(bodyBytes)
                            flush()
                        }
                        Log.i("GolderJNI", "fake response: ${bodyBytes.size} bytes")
                    } finally {
                        runCatching { client.close() }
                    }
                } catch (_: Exception) {
                    if (closed.get()) break
                }
            }
        }
    }

    override fun close() {
        closed.set(true)
        runCatching { socket.close() }
    }
}

/** Reads one ASCII header line without decoding the binary request body. */
private fun readAsciiLine(input: InputStream): String? {
    val bytes = java.io.ByteArrayOutputStream()
    while (true) {
        val b = input.read()
        if (b < 0) return if (bytes.size() == 0) null else bytes.toString("US-ASCII")
        if (b == '\n'.code) break
        if (b != '\r'.code) bytes.write(b)
    }
    return bytes.toString("US-ASCII")
}
