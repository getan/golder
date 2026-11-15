package com.getan.golder.mobile

import android.os.Bundle
import android.util.Log
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Box
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
import androidx.compose.material3.AlertDialog
import androidx.compose.material3.Button
import androidx.compose.material3.Card
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.DropdownMenu
import androidx.compose.material3.DropdownMenuItem
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

data class ProviderOption(
    val name: String,
    val defaultModel: String,
    val models: List<String>,
    val reasoning: List<String>,
)

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
    val workspaceDir = remember {
        File(context.filesDir, "workspace").apply { mkdirs() }
    }

    var version by remember { mutableStateOf("loading") }
    val providers = remember { mutableStateListOf<ProviderOption>() }
    var provider by remember { mutableStateOf("openai") }
    var model by remember { mutableStateOf("gpt-4o-mini") }
    var thinking by remember { mutableStateOf("") }
    var baseUrl by remember { mutableStateOf("") }
    var apiKey by remember { mutableStateOf("") }
    var gitToken by remember { mutableStateOf("") }
    var tokenLoaded by remember { mutableStateOf(false) }
    var prompt by remember { mutableStateOf("") }
    var handle by remember { mutableIntStateOf(0) }
    var opening by remember { mutableStateOf(false) }
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
        apiKey = withContext(Dispatchers.IO) { SecureStore.getAPIKey(context) }
        tokenLoaded = true

        val saved = SettingsStore.load(context)
        if (saved.provider.isNotBlank()) provider = saved.provider
        if (saved.model.isNotBlank()) model = saved.model
        thinking = saved.thinking
        baseUrl = saved.baseUrl

        val catalogRaw = runCatching {
            withContext(Dispatchers.IO) { GolderNative.catalog() }
        }.getOrNull()
        if (!catalogRaw.isNullOrBlank()) {
            runCatching {
                val root = JSONObject(catalogRaw)
                val array = root.optJSONArray("providers") ?: JSONArray()
                val parsed = mutableListOf<ProviderOption>()
                for (i in 0 until array.length()) {
                    val obj = array.getJSONObject(i)
                    val name = obj.optString("name").trim()
                    if (name.isEmpty()) continue
                    val defaultModel = obj.optString("defaultModel").trim()
                    val models = mutableListOf<String>()
                    obj.optJSONArray("models")?.let { list ->
                        for (j in 0 until list.length()) {
                            val value = list.optString(j).trim()
                            if (value.isNotEmpty()) models.add(value)
                        }
                    }
                    if (models.isEmpty() && defaultModel.isNotEmpty()) models.add(defaultModel)
                    val reasoning = mutableListOf<String>()
                    obj.optJSONArray("reasoning")?.let { list ->
                        for (j in 0 until list.length()) {
                            val value = list.optString(j).trim()
                            if (value.isNotEmpty()) reasoning.add(value)
                        }
                    }
                    parsed.add(ProviderOption(name, defaultModel, models, reasoning))
                }
                if (parsed.isNotEmpty()) {
                    providers.clear()
                    providers.addAll(parsed)
                    val savedProvider = saved.provider
                    val chosen = parsed.firstOrNull { it.name == savedProvider }
                        ?: parsed.firstOrNull { it.name == "openai" }
                        ?: parsed.first()
                    if (savedProvider.isBlank() || parsed.none { it.name == savedProvider }) {
                        provider = chosen.name
                        model = chosen.defaultModel
                        thinking = chosen.reasoning.firstOrNull() ?: ""
                    } else if (model.isBlank()) {
                        model = chosen.defaultModel
                    }
                }
            }.onFailure { Log.e("GolderJNI", "catalog parse failed", it) }
        }

        SettingsStore.save(context, provider, model, thinking, baseUrl)

        version = runCatching {
            withContext(Dispatchers.IO) { GolderNative.version() }
        }.getOrElse {
            Log.e("GolderJNI", "version failed", it)
            "unavailable"
        }
        Log.i(
            "GolderJNI",
            "version=$version providers=${providers.size} provider=$provider model=$model " +
                "apiKeySet=${apiKey.isNotBlank()} tokenSet=${gitToken.isNotBlank()}",
        )
    }

    LaunchedEffect(handle) {
        val pollHandle = handle
        if (pollHandle == 0) return@LaunchedEffect
        while (isActive && handle == pollHandle) {
            val raw = runCatching {
                withContext(Dispatchers.IO) { GolderNative.poll(pollHandle, 200) }
            }.getOrNull()
            // A reopen closes the old handle while this coroutine may still be
            // finishing a call. Never let that stale result overwrite the new
            // session's status.
            if (handle != pollHandle) break
            if (raw == null) {
                val err = runCatching { GolderNative.lastError() }.getOrDefault("")
                if (err.isNotBlank()) status = err
                Log.w("GolderJNI", "poll handle=$pollHandle failed: $err")
                break
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
            if (handle != pollHandle) break
            delay(60)
        }
    }

    LaunchedEffect(entries.size) {
        if (entries.isNotEmpty()) listState.animateScrollToItem(entries.lastIndex)
    }

    DisposableEffect(handle) {
        // Capture the key owned by this effect. Reading the mutable handle in
        // onDispose would close the *new* session when 0 -> 1 recomposes.
        val ownedHandle = handle
        onDispose {
            if (ownedHandle != 0) {
                runCatching { GolderNative.close(ownedHandle) }
            }
        }
    }

    val pickerProviders = if (providers.isEmpty()) {
        listOf(
            ProviderOption(
                "openai", "gpt-4o-mini", listOf("gpt-4o-mini"),
                listOf("low", "medium", "high"),
            ),
        )
    } else {
        providers
    }

    Column(modifier = Modifier.fillMaxSize().padding(12.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("Golder", style = MaterialTheme.typography.titleLarge)
            Spacer(Modifier.width(8.dp))
            Text("JNI $version", style = MaterialTheme.typography.labelSmall)
        }
        Spacer(Modifier.height(2.dp))
        Text(
            "Workspace: ${workspaceDir.absolutePath}",
            style = MaterialTheme.typography.labelSmall,
            maxLines = 1,
            overflow = TextOverflow.Ellipsis,
        )
        Spacer(Modifier.height(6.dp))
        ModelPicker(
            providers = pickerProviders,
            provider = provider,
            model = model,
            thinking = thinking,
            onPick = { p, m, t ->
                provider = p
                model = m
                thinking = t
                SettingsStore.save(context, p, m, t, baseUrl)
            },
        )
        Spacer(Modifier.height(6.dp))
        ConnectionPicker(baseUrl = baseUrl, apiKey = apiKey) { url, key ->
            baseUrl = url
            apiKey = key
            SettingsStore.save(context, provider, model, thinking, url)
            scope.launch(Dispatchers.IO) { SecureStore.saveAPIKey(context, key) }
        }
        Spacer(Modifier.height(6.dp))
        OutlinedTextField(
            value = gitToken,
            onValueChange = { gitToken = it },
            label = { Text("GitHub token (fine-grained PAT)") },
            singleLine = true,
            visualTransformation = PasswordVisualTransformation(),
            enabled = tokenLoaded,
            modifier = Modifier.fillMaxWidth(),
        )
        Text(
            "Encrypted by Android Keystore; never uploaded or logged. " +
                "Contents R/W; revoke on GitHub.",
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
        Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
            Button(
                enabled = !running && !opening,
                onClick = {
                    if (!opening) {
                        // Set this synchronously so a double tap cannot start two
                        // open operations and race their handle registration.
                        opening = true
                        scope.launch {
                            try {
                                if (!workspaceDir.isDirectory && !workspaceDir.mkdirs()) {
                                    status = "Cannot create workspace: ${workspaceDir.absolutePath}"
                                    return@launch
                                }
                                status = "Opening…"
                                if (handle != 0) {
                                    withContext(Dispatchers.IO) { GolderNative.close(handle) }
                                    handle = 0
                                }
                                val stateDir = File(context.filesDir, "state").apply { mkdirs() }
                                withContext(Dispatchers.IO) {
                                    SecureStore.saveToken(context, gitToken)
                                    SecureStore.saveAPIKey(context, apiKey)
                                }
                                SettingsStore.save(context, provider, model, thinking, baseUrl)
                                val config = JSONObject()
                                    .put("workspace", workspaceDir.absolutePath)
                                    .put("provider", provider)
                                    .put("model", model.trim().ifBlank { "gpt-4o-mini" })
                                    .put("thinkingLevel", thinking)
                                    .put("apiKey", apiKey)
                                    .put("stateDir", stateDir.absolutePath)
                                    .put("sessionId", "mobile-" + Integer.toHexString(workspaceDir.absolutePath.hashCode()))
                                    .put("allowNetworkGit", true)
                                    .put("gitToken", gitToken)
                                if (baseUrl.isNotBlank()) config.put("baseUrl", baseUrl.trim())
                                val opened = withContext(Dispatchers.IO) { GolderNative.open(config.toString()) }
                                val openErr = withContext(Dispatchers.IO) { GolderNative.lastError() }
                                Log.i(
                                    "GolderJNI",
                                    "open handle=$opened provider=$provider model=$model " +
                                        "error=${openErr.take(300)}",
                                )
                                if (opened == 0) {
                                    status = openErr.ifBlank { "Open failed." }
                                } else {
                                    val snapshot = withContext(Dispatchers.IO) { GolderNative.snapshot(opened) }
                                    if (snapshot == null) {
                                        val snapshotErr = withContext(Dispatchers.IO) { GolderNative.lastError() }
                                        status = snapshotErr.ifBlank { "Session snapshot failed." }
                                        Log.w("GolderJNI", "snapshot failed for handle=$opened: $snapshotErr")
                                        withContext(Dispatchers.IO) { GolderNative.close(opened) }
                                    } else {
                                        entries.clear()
                                        handle = opened
                                        loadHistory(snapshot)
                                        status = "Workspace open; saved session loaded."
                                    }
                                }
                            } finally {
                                opening = false
                            }
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

@Composable
private fun ModelPicker(
    providers: List<ProviderOption>,
    provider: String,
    model: String,
    thinking: String,
    onPick: (String, String, String) -> Unit,
) {
    var open by remember { mutableStateOf(false) }
    Card(modifier = Modifier.fillMaxWidth().clickable { open = true }) {
        Column(modifier = Modifier.padding(12.dp)) {
            Text("Provider · model · reasoning", style = MaterialTheme.typography.labelSmall)
            Text(
                "$provider · $model · ${thinking.ifBlank { "default" }}",
                style = MaterialTheme.typography.bodyLarge,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
        }
    }
    if (!open) return

    val fallback = ProviderOption(
        provider.ifBlank { "openai" },
        model.ifBlank { "gpt-4o-mini" },
        listOf(model.ifBlank { "gpt-4o-mini" }),
        listOf("low", "medium", "high"),
    )
    val choices = providers.ifEmpty { listOf(fallback) }
    var draftProvider by remember(open) { mutableStateOf(provider) }
    var draftModel by remember(open) { mutableStateOf(model) }
    var draftThinking by remember(open) { mutableStateOf(thinking) }
    val current = choices.firstOrNull { it.name == draftProvider } ?: choices.first()
    var custom by remember(open) { mutableStateOf(draftModel !in current.models) }

    AlertDialog(
        onDismissRequest = { open = false },
        title = { Text("Provider & model") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text("Provider", style = MaterialTheme.typography.labelSmall)
                DropdownField(current.name, choices.map { it.name }) { name ->
                    val next = choices.firstOrNull { it.name == name } ?: current
                    draftProvider = next.name
                    draftModel = next.defaultModel
                    draftThinking = next.reasoning.firstOrNull() ?: ""
                    custom = false
                }
                Text("Model", style = MaterialTheme.typography.labelSmall)
                DropdownField(draftModel, current.models.distinct() + "Custom model…") { value ->
                    if (value == "Custom model…") {
                        custom = true
                    } else {
                        draftModel = value
                        custom = false
                    }
                }
                if (custom) {
                    OutlinedTextField(
                        value = draftModel,
                        onValueChange = { draftModel = it },
                        label = { Text("Model id") },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth(),
                    )
                }
                Text("Reasoning", style = MaterialTheme.typography.labelSmall)
                DropdownField(
                    if (draftThinking.isBlank()) "default" else draftThinking,
                    listOf("default") + current.reasoning.distinct(),
                ) { value ->
                    draftThinking = if (value == "default") "" else value
                }
            }
        },
        confirmButton = {
            TextButton(
                enabled = draftProvider.isNotBlank() && draftModel.isNotBlank(),
                onClick = {
                    onPick(draftProvider, draftModel.trim(), draftThinking)
                    open = false
                },
            ) { Text("Use") }
        },
        dismissButton = {
            TextButton(onClick = { open = false }) { Text("Cancel") }
        },
    )
}

@Composable
private fun ConnectionPicker(
    baseUrl: String,
    apiKey: String,
    onPick: (String, String) -> Unit,
) {
    var open by remember { mutableStateOf(false) }
    val endpoint = baseUrl.ifBlank { "default endpoint" }
    val key = if (apiKey.isBlank()) "API key not set" else "API key ••••••"
    Card(modifier = Modifier.fillMaxWidth().clickable { open = true }) {
        Column(modifier = Modifier.padding(12.dp)) {
            Text("Connection", style = MaterialTheme.typography.labelSmall)
            Text(
                "$endpoint · $key",
                style = MaterialTheme.typography.bodyLarge,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
        }
    }
    if (!open) return

    var draftUrl by remember(open) { mutableStateOf(baseUrl) }
    var draftKey by remember(open) { mutableStateOf(apiKey) }
    AlertDialog(
        onDismissRequest = { open = false },
        title = { Text("Connection") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedTextField(
                    value = draftUrl,
                    onValueChange = { draftUrl = it },
                    label = { Text("Base URL (optional)") },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                OutlinedTextField(
                    value = draftKey,
                    onValueChange = { draftKey = it },
                    label = { Text("API key") },
                    singleLine = true,
                    visualTransformation = PasswordVisualTransformation(),
                    modifier = Modifier.fillMaxWidth(),
                )
                Text(
                    "Encrypted with Android Keystore and restored on next launch.",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        },
        confirmButton = {
            TextButton(
                onClick = {
                    onPick(draftUrl.trim(), draftKey)
                    open = false
                },
            ) { Text("Use") }
        },
        dismissButton = {
            TextButton(onClick = { open = false }) { Text("Cancel") }
        },
    )
}

@Composable
private fun DropdownField(
    value: String,
    options: List<String>,
    onSelect: (String) -> Unit,
) {
    var expanded by remember { mutableStateOf(false) }
    Box(modifier = Modifier.fillMaxWidth()) {
        OutlinedButton(
            onClick = { expanded = true },
            modifier = Modifier.fillMaxWidth(),
        ) {
            Text(value, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        DropdownMenu(expanded = expanded, onDismissRequest = { expanded = false }) {
            options.distinct().forEach { option ->
                DropdownMenuItem(
                    text = { Text(option, maxLines = 1, overflow = TextOverflow.Ellipsis) },
                    onClick = {
                        expanded = false
                        onSelect(option)
                    },
                )
            }
        }
    }
}
