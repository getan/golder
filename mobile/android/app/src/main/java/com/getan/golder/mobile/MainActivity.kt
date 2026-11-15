package com.getan.golder.mobile

import android.os.Bundle
import android.util.Log
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.Checkbox
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.Surface
import androidx.compose.material3.Text
import androidx.compose.material3.TextButton
import androidx.compose.runtime.Composable
import androidx.compose.runtime.DisposableEffect
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import java.io.File
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject

data class Entry(val id: Long, val kind: String, val text: String)

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContent {
            MaterialTheme {
                Surface(modifier = Modifier.fillMaxSize()) {
                    GolderApp()
                }
            }
        }
    }
}

@Composable
private fun GolderApp() {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val defaultWorkspace = File(context.filesDir, "workspace").absolutePath

    var version by remember { mutableStateOf("loading") }
    var workspace by remember { mutableStateOf(defaultWorkspace) }
    var model by remember { mutableStateOf("gpt-4o-mini") }
    var baseUrl by remember { mutableStateOf("") }
    var apiKey by remember { mutableStateOf("") }
    var gitToken by remember { mutableStateOf("") }
    var tokenLoaded by remember { mutableStateOf(false) }
    var allowGitNetwork by remember { mutableStateOf(false) }
    var prompt by remember { mutableStateOf("") }
    var handle by remember { mutableIntStateOf(0) }
    var running by remember { mutableStateOf(false) }
    var status by remember { mutableStateOf("Open a workspace to begin.") }
    var nextId by remember { mutableStateOf(1L) }
    val entries = remember { mutableStateListOf<Entry>() }
    val listState = rememberLazyListState()

    fun add(kind: String, text: String) {
        if (text.isBlank()) return
        entries.add(Entry(nextId++, kind, text))
    }

    fun appendAssistant(text: String) {
        val last = entries.lastOrNull()
        if (last != null && last.kind == "assistant") {
            entries[entries.lastIndex] = last.copy(text = last.text + text)
        } else {
            add("assistant", text)
        }
    }

    fun loadHistory(raw: String?) {
        val messages = runCatching { JSONObject(raw ?: "{}").optJSONArray("messages") }.getOrNull() ?: return
        for (i in 0 until messages.length()) {
            val msg = messages.getJSONObject(i)
            val role = msg.optString("role")
            val content = msg.optJSONArray("content")
            val text = buildString {
                if (content != null) {
                    for (j in 0 until content.length()) {
                        val block = content.getJSONObject(j)
                        if (block.optString("type") == "text") append(block.optString("text"))
                    }
                }
            }
            when (role) {
                "user" -> if (text.isNotBlank()) add("user", text)
                "assistant" -> if (text.isNotBlank()) add("assistant", text)
                "toolResult" -> if (text.isNotBlank()) add("tool", text.take(1200))
                "compaction" -> add("status", "Context summary restored")
            }
        }
    }

    LaunchedEffect(Unit) {
        gitToken = withContext(Dispatchers.IO) { SecureStore.getToken(context) }
        tokenLoaded = true
        version = runCatching {
            withContext(Dispatchers.IO) { GolderNative.version() }
        }.getOrElse {
            Log.e("GolderJNI", "version failed", it)
            "unavailable"
        }
        Log.i("GolderJNI", "version=$version")
    }

    LaunchedEffect(handle) {
        while (handle != 0 && isActive) {
            val raw = runCatching {
                withContext(Dispatchers.IO) { GolderNative.poll(handle, 200) }
            }.getOrNull()
            if (raw == null) {
                val err = runCatching { GolderNative.lastError() }.getOrDefault("")
                if (err.isNotBlank()) status = err
                delay(250)
                continue
            }
            try {
                val events = JSONArray(raw)
                for (i in 0 until events.length()) {
                    val event = events.getJSONObject(i)
                    when (event.optString("type")) {
                        "run_start" -> {
                            running = true
                            status = "Working…"
                        }
                        "text" -> appendAssistant(event.optString("text"))
                        "thinking" -> add("thinking", event.optString("text"))
                        "tool_start" -> {
                            val name = event.optString("toolName")
                            add("tool", "→ $name")
                        }
                        "tool_end" -> {
                            val name = event.optString("toolName")
                            val mark = if (event.optBoolean("isError")) "✗" else "✓"
                            val output = event.optString("text").take(1200)
                            add("tool", "$mark $name\n$output")
                        }
                        "run_end" -> {
                            running = false
                            val error = event.optString("error")
                            status = when {
                                event.optBoolean("cancelled") -> "Stopped."
                                error.isNotBlank() -> "Error: $error"
                                else -> "Done."
                            }
                            add("status", status)
                        }
                    }
                }
            } catch (e: Exception) {
                status = "Bad event JSON: ${e.message}"
            }
            delay(60)
        }
    }

    LaunchedEffect(entries.size) {
        if (entries.isNotEmpty()) listState.animateScrollToItem(entries.lastIndex)
    }

    DisposableEffect(handle) {
        onDispose {
            if (handle != 0) {
                runCatching { GolderNative.close(handle) }
            }
        }
    }

    Column(modifier = Modifier.fillMaxSize().padding(12.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("Golder", style = MaterialTheme.typography.titleLarge)
            Spacer(Modifier.width(8.dp))
            Text("JNI $version", style = MaterialTheme.typography.labelSmall)
        }
        Spacer(Modifier.height(6.dp))
        OutlinedTextField(
            value = workspace,
            onValueChange = { workspace = it },
            label = { Text("Workspace") },
            singleLine = true,
            modifier = Modifier.fillMaxWidth(),
        )
        OutlinedTextField(
            value = model,
            onValueChange = { model = it },
            label = { Text("Model") },
            singleLine = true,
            modifier = Modifier.fillMaxWidth(),
        )
        OutlinedTextField(
            value = baseUrl,
            onValueChange = { baseUrl = it },
            label = { Text("Base URL (optional)") },
            singleLine = true,
            modifier = Modifier.fillMaxWidth(),
        )
        OutlinedTextField(
            value = apiKey,
            onValueChange = { apiKey = it },
            label = { Text("API key (not saved)") },
            singleLine = true,
            visualTransformation = PasswordVisualTransformation(),
            modifier = Modifier.fillMaxWidth(),
        )
        OutlinedTextField(
            value = gitToken,
            onValueChange = { gitToken = it },
            label = { Text("GitHub token (fine-grained PAT, encrypted)") },
            singleLine = true,
            visualTransformation = PasswordVisualTransformation(),
            enabled = tokenLoaded,
            modifier = Modifier.fillMaxWidth(),
        )
        Text(
            "Keystore-encrypted on this device; never uploaded or logged. " +
                "Use a fine-grained PAT with Contents R/W; revoke it anytime on GitHub.",
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
        )
        TextButton(
            enabled = tokenLoaded,
            onClick = {
                gitToken = ""
                SecureStore.clearToken(context)
                status = "GitHub token cleared."
            },
        ) { Text("Forget GitHub token") }
        Row(verticalAlignment = Alignment.CenterVertically) {
            Checkbox(
                checked = allowGitNetwork,
                onCheckedChange = { allowGitNetwork = it },
                enabled = handle == 0,
            )
            Text("Allow Git network (clone/fetch/push)", style = MaterialTheme.typography.bodySmall)
        }
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Button(
                enabled = !running,
                onClick = {
                    scope.launch {
                        val dir = File(workspace)
                        if (!dir.isDirectory && !dir.mkdirs()) {
                            status = "Cannot create workspace: ${dir.absolutePath}"
                            return@launch
                        }
                        if (handle != 0) {
                            withContext(Dispatchers.IO) { GolderNative.close(handle) }
                            handle = 0
                        }
                        val stateDir = File(context.filesDir, "state").apply { mkdirs() }
                        withContext(Dispatchers.IO) { SecureStore.saveToken(context, gitToken) }
                        val config = JSONObject()
                            .put("workspace", dir.absolutePath)
                            .put("model", model.trim())
                            .put("apiKey", apiKey)
                            .put("stateDir", stateDir.absolutePath)
                            .put("sessionId", "mobile-" + Integer.toHexString(dir.absolutePath.hashCode()))
                            .put("allowNetworkGit", allowGitNetwork)
                            .put("gitToken", gitToken)
                        if (baseUrl.isNotBlank()) config.put("baseUrl", baseUrl.trim())
                        val opened = withContext(Dispatchers.IO) { GolderNative.open(config.toString()) }
                        if (opened == 0) {
                            status = withContext(Dispatchers.IO) { GolderNative.lastError() }
                        } else {
                            val snapshot = withContext(Dispatchers.IO) { GolderNative.snapshot(opened) }
                            entries.clear()
                            handle = opened
                            loadHistory(snapshot)
                            status = "Workspace open; saved session loaded."
                        }
                    }
                },
            ) { Text("Open workspace") }
            OutlinedButton(
                enabled = running,
                onClick = {
                    if (handle != 0) GolderNative.cancel(handle)
                },
            ) { Text("Stop") }
        }
        Spacer(Modifier.height(6.dp))
        Text(status, style = MaterialTheme.typography.bodySmall, maxLines = 3, overflow = TextOverflow.Ellipsis)
        Spacer(Modifier.height(6.dp))
        Card(modifier = Modifier.weight(1f).fillMaxWidth()) {
            LazyColumn(
                state = listState,
                modifier = Modifier.fillMaxSize().padding(8.dp),
                verticalArrangement = Arrangement.spacedBy(6.dp),
            ) {
                items(entries, key = { it.id }) { entry ->
                    when (entry.kind) {
                        "assistant" -> Text(entry.text, style = MaterialTheme.typography.bodyMedium)
                        "user" -> Text("You: ${entry.text}", style = MaterialTheme.typography.bodyMedium)
                        "thinking" -> Text(entry.text, style = MaterialTheme.typography.bodySmall, maxLines = 4, overflow = TextOverflow.Ellipsis)
                        "tool" -> Text(entry.text, style = MaterialTheme.typography.bodySmall)
                        else -> Text(entry.text, style = MaterialTheme.typography.labelSmall)
                    }
                }
                if (running) {
                    item {
                        Row(verticalAlignment = Alignment.CenterVertically) {
                            CircularProgressIndicator(modifier = Modifier.height(16.dp).width(16.dp))
                            Spacer(Modifier.width(8.dp))
                            Text("Working…", style = MaterialTheme.typography.labelSmall)
                        }
                    }
                }
            }
        }
        Spacer(Modifier.height(6.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            OutlinedTextField(
                value = prompt,
                onValueChange = { prompt = it },
                label = { Text("Task") },
                modifier = Modifier.weight(1f),
                maxLines = 4,
            )
            Spacer(Modifier.width(8.dp))
            Button(
                enabled = handle != 0 && !running && prompt.isNotBlank(),
                onClick = {
                    val text = prompt.trim()
                    scope.launch {
                        add("user", text)
                        prompt = ""
                        val code = withContext(Dispatchers.IO) { GolderNative.start(handle, text) }
                        if (code != 0) {
                            status = withContext(Dispatchers.IO) { GolderNative.lastError() }
                        } else {
                            running = true
                        }
                    }
                },
            ) { Text("Send") }
        }
    }
}
