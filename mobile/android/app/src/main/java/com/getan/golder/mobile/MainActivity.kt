@file:OptIn(
    androidx.compose.material3.ExperimentalMaterial3Api::class,
    androidx.compose.foundation.ExperimentalFoundationApi::class,
)

package com.getan.golder.mobile

import android.os.Bundle
import android.content.Intent
import android.util.Log
import androidx.core.content.FileProvider
import androidx.activity.ComponentActivity
import androidx.activity.compose.BackHandler
import androidx.activity.compose.setContent
import androidx.activity.enableEdgeToEdge
import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.basicMarquee
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectHorizontalDragGestures
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.systemGestureExclusion
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import java.io.File
import kotlin.math.roundToInt
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject

data class Entry(
    val id: Long,
    val kind: String,
    val text: String,
    val toolName: String = "",
    val toolCommand: String = "",
    val toolOutput: String = "",
    val toolError: Boolean = false,
    val toolDone: Boolean = false,
)

data class TodoItem(val content: String, val status: String)

data class PermissionModeOption(
    val id: String,
    val label: String,
    val description: String,
)

private val permissionModeOptions = listOf(
    PermissionModeOption("read-only", "Read Only", ""),
    PermissionModeOption("ask", "Ask", ""),
    PermissionModeOption("auto", "Auto", ""),
    PermissionModeOption("full-access", "Full Access", ""),
)

private fun permissionModeLabel(id: String): String =
    permissionModeOptions.firstOrNull { it.id == id }?.label ?: id

private fun defaultReasoning(levels: List<String>): String =
    if ("medium" in levels) "medium" else levels.firstOrNull() ?: ""

data class ProviderOption(
    val name: String,
    val defaultModel: String,
    val models: List<String>,
    val reasoning: List<String>,
)

class MainActivity : ComponentActivity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        enableEdgeToEdge()
        val initialTask = intent?.getStringExtra(Intent.EXTRA_TEXT).orEmpty()
        setContent {
            MaterialTheme {
                Surface(modifier = Modifier.fillMaxSize()) {
                    GolderApp(initialTask)
                }
            }
        }
    }
}

@Composable
private fun GolderApp(initialTask: String = "") {
    val context = LocalContext.current
    val scope = rememberCoroutineScope()
    val modelCacheDir = remember {
        File(context.filesDir, "model-cache").apply { mkdirs() }.absolutePath
    }

    val projects = remember { mutableStateListOf<Project>() }
    var activeProjectId by remember { mutableStateOf("") }
    var settingsLoaded by remember { mutableStateOf(false) }
    var settingsOpen by remember { mutableStateOf(false) }
    var newProjectOpen by remember { mutableStateOf(false) }
    val drawerState = rememberDrawerState(DrawerValue.Closed)
    val activeProject = projects.firstOrNull { it.id == activeProjectId }

    var version by remember { mutableStateOf("loading") }
    val providers = remember { mutableStateListOf<ProviderOption>() }
    var provider by remember { mutableStateOf("openai") }
    var model by remember { mutableStateOf("gpt-4o-mini") }
    var thinking by remember { mutableStateOf("") }
    var permissionMode by remember { mutableStateOf("auto") }
    var language by remember { mutableStateOf("en") }
    var baseUrl by remember { mutableStateOf("") }
    var apiKey by remember { mutableStateOf("") }
    var gitToken by remember { mutableStateOf("") }
    var tokenLoaded by remember { mutableStateOf(false) }
    var prompt by remember { mutableStateOf(initialTask) }
    var handle by remember { mutableIntStateOf(0) }
    var opening by remember { mutableStateOf(false) }
    var running by remember { mutableStateOf(false) }
    var contextPercent by remember { mutableStateOf<Int?>(null) }
    var gitBranch by remember { mutableStateOf("no repo") }
    var status by remember { mutableStateOf("Starting…") }
    var nextId by remember { mutableStateOf(1L) }
    val entries = remember { mutableStateListOf<Entry>() }
    val listState = rememberLazyListState()
    val openMutex = remember { Mutex() }
    val modelLoading = remember { mutableStateMapOf<String, Boolean>() }

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
        val toolCalls = mutableMapOf<String, Pair<String, String>>()
        for (i in 0 until messages.length()) {
            val msg = messages.getJSONObject(i)
            val role = msg.optString("role")
            val content = msg.optJSONArray("content")
            val text = buildString {
                if (content != null) {
                    for (j in 0 until content.length()) {
                        val block = content.getJSONObject(j)
                        when (block.optString("type")) {
                            "text" -> append(block.optString("text"))
                            "toolCall" -> {
                                val id = block.optString("id")
                                val name = block.optString("name").ifBlank { "tool" }
                                val args = block.optJSONObject("arguments")
                                    ?: runCatching {
                                        JSONObject(block.optString("arguments"))
                                    }.getOrNull()
                                if (id.isNotBlank()) {
                                    toolCalls[id] = name to toolCommand(name, args)
                                }
                            }
                        }
                    }
                }
            }
            when (role) {
                "user" -> if (text.isNotBlank()) add("user", text)
                "assistant" -> if (text.isNotBlank()) add("assistant", text)
                "toolResult" -> if (text.isNotBlank()) {
                    val toolCallId = msg.optString("toolCallId")
                    val known = toolCalls[toolCallId]
                    val name = msg.optString("toolName").ifBlank {
                        known?.first ?: "tool"
                    }
                    entries.add(
                        Entry(
                            nextId++, "tool", "",
                            toolName = name,
                            toolCommand = known?.second ?: name,
                            toolOutput = text.take(1200),
                            toolDone = true,
                        ),
                    )
                }
                "compaction" -> add("status", "Context summary restored")
            }
        }
    }

    suspend fun loadModels(providerName: String) {
        if (modelLoading[providerName] == true) return
        modelLoading[providerName] = true
        try {
            val base = if (providerName == provider) baseUrl.trim() else ""
            val key = if (providerName == provider) apiKey else ""
            var ids = emptyList<String>()
            var fresh = false

            fun parse(raw: String?) {
                if (raw.isNullOrBlank()) return
                val obj = runCatching { JSONObject(raw) }.getOrNull() ?: return
                val array = obj.optJSONArray("ids") ?: return
                ids = buildList {
                    for (i in 0 until array.length()) {
                        val id = array.optString(i).trim()
                        if (id.isNotEmpty()) add(id)
                    }
                }
                fresh = obj.optBoolean("fresh")
                if (ids.isNotEmpty()) {
                    val index = providers.indexOfFirst { it.name == providerName }
                    if (index >= 0) {
                        providers[index] = providers[index].copy(models = ids)
                    }
                }
            }

            parse(
                withContext(Dispatchers.IO) {
                    GolderNative.models(providerName, base, key, modelCacheDir, false)
                },
            )
            if (!fresh || ids.isEmpty()) {
                parse(
                    withContext(Dispatchers.IO) {
                        GolderNative.models(providerName, base, key, modelCacheDir, true)
                    },
                )
            }
        } catch (e: Exception) {
            Log.w("GolderJNI", "model discovery failed provider=$providerName: ${e.message}")
        } finally {
            modelLoading[providerName] = false
        }
    }

    suspend fun openWorkspace(project: Project) {
        openMutex.withLock {
            opening = true
            try {
            val dir = File(project.path)
            if (!dir.isDirectory && !dir.mkdirs()) {
                status = "Cannot create project: ${project.path}"
                return
            }
            contextPercent = null
            gitBranch = withContext(Dispatchers.IO) { readGitBranch(dir) }
            status = "Opening ${project.name}…"
            if (handle != 0) {
                withContext(Dispatchers.IO) { GolderNative.close(handle) }
                handle = 0
            }
            entries.clear()
            val stateDir = File(context.filesDir, "state").apply { mkdirs() }
            withContext(Dispatchers.IO) {
                SecureStore.saveToken(context, gitToken)
                SecureStore.saveAPIKey(context, apiKey)
            }
            SettingsStore.save(context, provider, model, thinking, baseUrl, permissionMode, language)
            val config = JSONObject()
                .put("workspace", dir.absolutePath)
                .put("provider", provider)
                .put("model", model.trim().ifBlank { "gpt-4o-mini" })
                .put("thinkingLevel", thinking)
                .put("permissionMode", permissionMode)
                .put("apiKey", apiKey)
                .put("stateDir", stateDir.absolutePath)
                .put("sessionId", "mobile-" + Integer.toHexString(dir.absolutePath.hashCode()))
                .put("allowNetworkGit", true)
                .put("gitToken", gitToken)
            if (baseUrl.isNotBlank()) config.put("baseUrl", baseUrl.trim())

            val opened = withContext(Dispatchers.IO) { GolderNative.open(config.toString()) }
            val openErr = withContext(Dispatchers.IO) { GolderNative.lastError() }
            Log.i("GolderJNI", "open project=${project.id} handle=$opened error=${openErr.take(300)}")
            if (opened == 0) {
                status = openErr.ifBlank { "Open failed." }
                return
            }
            if (activeProjectId != project.id) {
                withContext(Dispatchers.IO) { GolderNative.close(opened) }
                return
            }
            val snapshot = withContext(Dispatchers.IO) { GolderNative.snapshot(opened) }
            if (snapshot == null) {
                val snapshotErr = withContext(Dispatchers.IO) { GolderNative.lastError() }
                status = snapshotErr.ifBlank { "Session snapshot failed." }
                withContext(Dispatchers.IO) { GolderNative.close(opened) }
                return
            }
            entries.clear()
            handle = opened
            loadHistory(snapshot)
            status = "Ready"
            } finally {
                opening = false
            }
        }
    }

    fun selectProject(project: Project) {
        scope.launch { drawerState.close() }
        if (project.id == activeProjectId) return
        scope.launch {
            if (handle != 0) {
                withContext(Dispatchers.IO) { GolderNative.close(handle) }
                handle = 0
            }
            running = false
            entries.clear()
            status = "Opening ${project.name}…"
            activeProjectId = project.id
            ProjectStore.save(context, projects, project.id)
        }
    }

    LaunchedEffect(Unit) {
        val projectState = ProjectStore.load(context)
        projects.clear()
        projects.addAll(projectState.projects)
        activeProjectId = projectState.activeId
        ProjectStore.save(context, projects, activeProjectId)

        gitToken = withContext(Dispatchers.IO) { SecureStore.getToken(context) }
        apiKey = withContext(Dispatchers.IO) { SecureStore.getAPIKey(context) }
        tokenLoaded = true

        val saved = SettingsStore.load(context)
        if (saved.provider.isNotBlank()) provider = saved.provider
        if (saved.model.isNotBlank()) model = saved.model
        thinking = saved.thinking
        permissionMode = saved.permissionMode
        language = saved.language
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
                        thinking = defaultReasoning(chosen.reasoning)
                    } else if (model.isBlank()) {
                        model = chosen.defaultModel
                    }
                }
            }.onFailure { Log.e("GolderJNI", "catalog parse failed", it) }
        }
        SettingsStore.save(context, provider, model, thinking, baseUrl, permissionMode, language)

        version = runCatching {
            withContext(Dispatchers.IO) { GolderNative.version() }
        }.getOrElse {
            Log.e("GolderJNI", "version failed", it)
            "unavailable"
        }
        Log.i(
            "GolderJNI",
            "version=$version providers=${providers.size} projects=${projects.size} " +
                "provider=$provider model=$model apiKeySet=${apiKey.isNotBlank()} " +
                "tokenSet=${gitToken.isNotBlank()}",
        )
        settingsLoaded = true
    }

    LaunchedEffect(settingsLoaded, activeProjectId) {
        if (!settingsLoaded) return@LaunchedEffect
        val project = projects.firstOrNull { it.id == activeProjectId } ?: return@LaunchedEffect
        openWorkspace(project)
    }

    LaunchedEffect(settingsLoaded) {
        if (settingsLoaded) loadModels(provider)
    }

    LaunchedEffect(handle) {
        val pollHandle = handle
        if (pollHandle == 0) return@LaunchedEffect
        while (isActive && handle == pollHandle) {
            val raw = runCatching {
                withContext(Dispatchers.IO) { GolderNative.poll(pollHandle, 200) }
            }.getOrNull()
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
                            val name = event.optString("toolName").ifBlank { "tool" }
                            val args = event.optJSONObject("args")
                            entries.add(
                                Entry(
                                    nextId++, "tool", "",
                                    toolName = name,
                                    toolCommand = toolCommand(name, args),
                                ),
                            )
                        }
                        "tool_end" -> {
                            val name = event.optString("toolName").ifBlank { "tool" }
                            val output = event.optString("text").take(1200)
                            val isError = event.optBoolean("isError")
                            val index = entries.indexOfLast {
                                it.kind == "tool" && it.toolName == name && !it.toolDone
                            }
                            if (index >= 0) {
                                entries[index] = entries[index].copy(
                                    toolOutput = output,
                                    toolError = isError,
                                    toolDone = true,
                                )
                            } else {
                                entries.add(
                                    Entry(
                                        nextId++, "tool", "",
                                        toolName = name,
                                        toolOutput = output,
                                        toolError = isError,
                                        toolDone = true,
                                    ),
                                )
                            }
                            if (name == "git") {
                                val path = activeProject?.path
                                if (path != null) {
                                    scope.launch(Dispatchers.IO) {
                                        gitBranch = readGitBranch(File(path))
                                    }
                                }
                            }
                            if (name == "android_build" && !isError) {
                                val apkPath = Regex("APK_PATH=(\\S+)").find(output)?.groupValues?.get(1)
                                if (apkPath != null) {
                                    status = if (installApk(context, apkPath)) {
                                        "APK downloaded; confirm installation."
                                    } else {
                                        "APK downloaded, but the installer could not be opened."
                                    }
                                }
                            }
                        }
                        "context_usage" -> {
                            val window = event.optInt("contextWindow")
                            val tokens = event.optInt("contextTokens")
                            contextPercent = if (window > 0) {
                                ((tokens.toDouble() / window) * 100)
                                    .roundToInt()
                                    .coerceIn(0, 100)
                            } else {
                                null
                            }
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
        // Capture this effect's own key; reading the mutable state in onDispose
        // would close the *new* session on a 0 -> 1 recomposition.
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

    BackHandler(enabled = settingsOpen) { settingsOpen = false }

    if (settingsOpen) {
        CompositionLocalProvider(LocalStrings provides stringsFor(language)) {
            SettingsScreen(
                version = version,
                providers = pickerProviders,
                provider = provider,
                model = model,
                thinking = thinking,
                permissionMode = permissionMode,
                language = language,
                baseUrl = baseUrl,
                apiKey = apiKey,
                gitToken = gitToken,
                tokenLoaded = tokenLoaded,
                projectName = activeProject?.name ?: "Project",
                onProviderPick = { p, m, t ->
                    provider = p
                    model = m
                    thinking = t
                    SettingsStore.save(context, p, m, t, baseUrl, permissionMode, language)
                },
                onRequestModels = { name -> scope.launch { loadModels(name) } },
                onPermissionPick = { mode ->
                    permissionMode = mode
                    SettingsStore.save(context, provider, model, thinking, baseUrl, mode, language)
                },
                onLanguagePick = { lang ->
                    language = lang
                    SettingsStore.save(context, provider, model, thinking, baseUrl, permissionMode, lang)
                },
                onConnectionPick = { url, key ->
                    baseUrl = url
                    apiKey = key
                    SettingsStore.save(context, provider, model, thinking, url, permissionMode, language)
                    scope.launch(Dispatchers.IO) { SecureStore.saveAPIKey(context, key) }
                },
                onTokenChange = { gitToken = it },
                onForgetToken = {
                    gitToken = ""
                    SecureStore.clearToken(context)
                    status = "GitHub token cleared."
                },
                onClose = { settingsOpen = false },
                onReconnect = {
                    settingsOpen = false
                    scope.launch { activeProject?.let { openWorkspace(it) } }
                },
            )
        }
        return
    }

    CompositionLocalProvider(LocalStrings provides stringsFor(language)) {
    ModalNavigationDrawer(
        drawerState = drawerState,
        drawerContent = {
            ProjectDrawer(
                projects = projects,
                activeId = activeProjectId,
                onSelect = { selectProject(it) },
                onNew = { newProjectOpen = true },
            )
        },
    ) {
        Scaffold(
            topBar = {
                Column {
                    MainTopBar(
                        projectName = activeProject?.name ?: "Golder",
                        provider = provider,
                        model = model,
                        thinking = thinking,
                        connected = handle != 0,
                        running = running,
                        onMenu = { scope.launch { drawerState.open() } },
                        onSettings = { settingsOpen = true },
                        onOpen = { scope.launch { activeProject?.let { openWorkspace(it) } } },
                        onStop = { if (handle != 0) GolderNative.cancel(handle) },
                    )
                    StatusStrip(
                        mode = permissionMode,
                        contextPercent = contextPercent,
                        branch = gitBranch,
                    )
                }
            },
            bottomBar = {
                Composer(
                    prompt = prompt,
                    enabled = handle != 0 && !running && prompt.isNotBlank(),
                    running = running,
                    onPrompt = { prompt = it },
                    onSend = {
                        val text = prompt.trim()
                        if (text.isNotBlank() && handle != 0 && !running) {
                            prompt = ""
                            scope.launch {
                                add("user", text)
                                val code = withContext(Dispatchers.IO) { GolderNative.start(handle, text) }
                                if (code != 0) {
                                    status = withContext(Dispatchers.IO) { GolderNative.lastError() }
                                } else {
                                    running = true
                                }
                            }
                        }
                    },
                )
            },
        ) { inner ->
            Box(modifier = Modifier.fillMaxSize().padding(inner)) {
                Column(modifier = Modifier.fillMaxSize()) {
                    if (status.isNotBlank()) {
                        Text(
                            status,
                            style = MaterialTheme.typography.labelSmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                            maxLines = 2,
                            overflow = TextOverflow.Ellipsis,
                            modifier = Modifier.padding(horizontal = 16.dp, vertical = 4.dp),
                        )
                    }
                    Conversation(
                        entries = entries,
                        running = running,
                        listState = listState,
                        modifier = Modifier.weight(1f),
                    )
                }
                // Android's back gesture owns the left edge. Exclude a narrow
                // strip and turn its horizontal drag into the project drawer.
                Box(
                    modifier = Modifier
                        .fillMaxHeight()
                        .width(28.dp)
                        .align(Alignment.CenterStart)
                        .systemGestureExclusion()
                        .pointerInput(drawerState) {
                            var total = 0f
                            detectHorizontalDragGestures(
                                onDragStart = { total = 0f },
                                onHorizontalDrag = { change, amount ->
                                    total += amount
                                    change.consume()
                                    if (total > 28f && drawerState.isClosed) {
                                        total = -1000f
                                        scope.launch { drawerState.open() }
                                    }
                                },
                            )
                        },
                )
            }
        }
    }

    if (newProjectOpen) {
        NewProjectDialog(
            onDismiss = { newProjectOpen = false },
            onCreate = { name ->
                val project = ProjectStore.create(context, name)
                projects.add(project)
                ProjectStore.save(context, projects, activeProjectId)
                newProjectOpen = false
                selectProject(project)
            },
        )
    }
    }
}

@Composable
private fun MainTopBar(
    projectName: String,
    provider: String,
    model: String,
    thinking: String,
    connected: Boolean,
    running: Boolean,
    onMenu: () -> Unit,
    onSettings: () -> Unit,
    onOpen: () -> Unit,
    onStop: () -> Unit,
) {
    val s = LocalStrings.current
    TopAppBar(
        navigationIcon = {
            TextButton(onClick = onMenu) { Text("☰", fontSize = 22.sp) }
        },
        title = {
            Column {
                Text(
                    projectName,
                    style = MaterialTheme.typography.titleMedium,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
                Text(
                    "$provider · $model · ${thinking.ifBlank { "default" }}",
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                    maxLines = 1,
                    overflow = TextOverflow.Ellipsis,
                )
            }
        },
        actions = {
            if (running) {
                TextButton(onClick = onStop) { Text(s.stop) }
            } else if (!connected) {
                TextButton(onClick = onOpen) { Text(s.open) }
            }
            TextButton(onClick = onSettings) { Text("⚙", fontSize = 20.sp) }
        },
        colors = TopAppBarDefaults.topAppBarColors(
            containerColor = MaterialTheme.colorScheme.surface,
        ),
    )
}

@Composable
private fun StatusStrip(
    mode: String,
    contextPercent: Int?,
    branch: String,
) {
    val s = LocalStrings.current
    Surface(color = MaterialTheme.colorScheme.surfaceVariant.copy(alpha = 0.45f)) {
        Row(
            modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 4.dp),
            horizontalArrangement = Arrangement.spacedBy(12.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(
                "${permissionModeLabel(mode)} · ${s.ctx} ${contextPercent?.let { "$it%" } ?: "--"}",
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                maxLines = 1,
            )
            Text(
                "⑂ $branch",
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
                modifier = Modifier.weight(1f),
            )
        }
    }
}

@Composable
private fun Conversation(
    entries: List<Entry>,
    running: Boolean,
    listState: androidx.compose.foundation.lazy.LazyListState,
    modifier: Modifier = Modifier,
) {
    val s = LocalStrings.current
    val latestTodoId = entries.lastOrNull {
        it.kind == "tool" && isTodoTool(it.toolName)
    }?.id
    val visible = entries.filterNot {
        it.kind == "tool" && isTodoTool(it.toolName) && it.id != latestTodoId
    }
    LazyColumn(
        state = listState,
        modifier = modifier.fillMaxSize().padding(horizontal = 16.dp),
        verticalArrangement = Arrangement.spacedBy(8.dp),
    ) {
        if (visible.isEmpty()) {
            item {
                Text(
                    s.startTask,
                    style = MaterialTheme.typography.bodyMedium,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
        }
        if (running) {
            // Keep each command visible while the run is in progress.
            items(visible, key = { it.id }) { entry ->
                ConversationEntry(entry)
            }
        } else {
            // Once the run is complete, merge consecutive completed commands.
            var index = 0
            while (index < visible.size) {
                val entry = visible[index]
                if (entry.kind == "tool" && !isTodoTool(entry.toolName) && entry.toolDone) {
                    val group = mutableListOf(entry)
                    var next = index + 1
                    while (next < visible.size) {
                        val candidate = visible[next]
                        if (candidate.kind == "tool" &&
                            !isTodoTool(candidate.toolName) &&
                            candidate.toolDone
                        ) {
                            group.add(candidate)
                            next++
                        } else {
                            break
                        }
                    }
                    if (group.size > 1) {
                        item(key = "group-${group.first().id}-${group.last().id}") {
                            ToolGroupCard(group)
                        }
                        index = next
                        continue
                    }
                }
                item(key = "entry-${entry.id}") {
                    ConversationEntry(entry)
                }
                index++
            }
        }
        if (running) {
            item {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    CircularProgressIndicator(modifier = Modifier.height(16.dp).width(16.dp))
                    Spacer(Modifier.width(8.dp))
                    Text(s.working, style = MaterialTheme.typography.labelSmall)
                }
            }
        }
    }
}

@Composable
private fun ConversationEntry(entry: Entry) {
    when {
        entry.kind == "user" -> ChatBubble(entry.text, user = true)
        entry.kind == "assistant" -> ChatBubble(entry.text, user = false)
        entry.kind == "tool" && isTodoTool(entry.toolName) -> TodoCard(entry)
        entry.kind == "tool" -> ToolCard(entry)
        entry.kind == "thinking" -> Text(
            entry.text,
            style = MaterialTheme.typography.bodySmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            maxLines = 3,
            overflow = TextOverflow.Ellipsis,
        )
        else -> Text(
            entry.text,
            style = MaterialTheme.typography.labelSmall,
            color = MaterialTheme.colorScheme.onSurfaceVariant,
            textAlign = TextAlign.Center,
            modifier = Modifier.fillMaxWidth(),
        )
    }
}

@Composable
private fun ChatBubble(text: String, user: Boolean) {
    Row(
        modifier = Modifier.fillMaxWidth(),
        horizontalArrangement = if (user) Arrangement.End else Arrangement.Start,
    ) {
        Surface(
            shape = RoundedCornerShape(16.dp),
            color = if (user) {
                MaterialTheme.colorScheme.primaryContainer
            } else {
                MaterialTheme.colorScheme.surfaceVariant
            },
            modifier = Modifier.widthIn(max = 320.dp),
        ) {
            Text(
                text,
                style = MaterialTheme.typography.bodyMedium,
                color = if (user) {
                    MaterialTheme.colorScheme.onPrimaryContainer
                } else {
                    MaterialTheme.colorScheme.onSurfaceVariant
                },
                modifier = Modifier.padding(horizontal = 12.dp, vertical = 9.dp),
            )
        }
    }
}

@Composable
private fun ToolGroupCard(entries: List<Entry>) {
    var expanded by remember(entries.first().id, entries.last().id) {
        mutableStateOf(false)
    }
    val errors = entries.count { it.toolError }
    val names = entries.map { it.toolName }.distinct().joinToString(" · ")
    Card(
        modifier = Modifier.fillMaxWidth(),
        colors = CardDefaults.cardColors(
            containerColor = MaterialTheme.colorScheme.surfaceVariant,
        ),
    ) {
        Column(modifier = Modifier.padding(10.dp)) {
            Row(
                modifier = Modifier
                    .fillMaxWidth()
                    .clickable { expanded = !expanded },
                verticalAlignment = Alignment.CenterVertically,
            ) {
                Text(
                    if (errors > 0) "✗" else "✓",
                    color = if (errors > 0) {
                        MaterialTheme.colorScheme.error
                    } else {
                        MaterialTheme.colorScheme.primary
                    },
                    style = MaterialTheme.typography.bodySmall,
                )
                Spacer(Modifier.width(6.dp))
                Text(
                    "${entries.size} commands",
                    style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.weight(1f),
                )
                Text(
                    if (expanded) "Hide" else "Show",
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            Text(
                names,
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
                maxLines = 1,
                overflow = TextOverflow.Ellipsis,
            )
            if (expanded) {
                Spacer(Modifier.height(6.dp))
                Column(verticalArrangement = Arrangement.spacedBy(6.dp)) {
                    entries.forEach { ToolCard(it) }
                }
            }
        }
    }
}

@Composable
private fun ToolCard(entry: Entry) {
    var expanded by remember(entry.id) { mutableStateOf(false) }
    val mark = when {
        !entry.toolDone -> "…"
        entry.toolError -> "✗"
        else -> "✓"
    }
    val markColor = when {
        !entry.toolDone -> MaterialTheme.colorScheme.onSurfaceVariant
        entry.toolError -> MaterialTheme.colorScheme.error
        else -> MaterialTheme.colorScheme.primary
    }
    val label = entry.toolCommand.ifBlank {
        entry.toolName.ifBlank { "tool" }
    }
    val snippet = entry.toolOutput.lineSequence()
        .firstOrNull { it.isNotBlank() }
        ?.trim()
        ?.take(100)
        .orEmpty()
    Card(
        modifier = Modifier.fillMaxWidth().clickable { expanded = !expanded },
        colors = CardDefaults.cardColors(
            containerColor = MaterialTheme.colorScheme.surfaceVariant,
        ),
    ) {
        Column(modifier = Modifier.padding(10.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(mark, color = markColor, style = MaterialTheme.typography.bodySmall)
                Spacer(Modifier.width(6.dp))
                Column(modifier = Modifier.weight(1f)) {
                    Text(
                        label,
                        style = MaterialTheme.typography.labelMedium,
                        maxLines = 1,
                        overflow = TextOverflow.Ellipsis,
                    )
                    if (!expanded && snippet.isNotBlank()) {
                        Text(
                            snippet,
                            style = MaterialTheme.typography.labelSmall,
                            color = MaterialTheme.colorScheme.onSurfaceVariant,
                            maxLines = 1,
                            overflow = TextOverflow.Ellipsis,
                        )
                    }
                }
                Text(
                    if (expanded) "Hide" else "Show",
                    style = MaterialTheme.typography.labelSmall,
                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                )
            }
            if (expanded) {
                Text(
                    entry.toolOutput.ifBlank {
                        if (entry.toolDone) "No output." else "Running…"
                    },
                    style = MaterialTheme.typography.bodySmall,
                    fontFamily = FontFamily.Monospace,
                    modifier = Modifier.padding(top = 6.dp),
                )
            }
        }
    }
}

@Composable
private fun TodoCard(entry: Entry) {
    val items = parseTodoItems(entry.toolOutput)
    val done = items.count { it.status == "completed" }
    Card(
        modifier = Modifier.fillMaxWidth(),
        colors = CardDefaults.cardColors(
            containerColor = MaterialTheme.colorScheme.tertiaryContainer,
        ),
        border = BorderStroke(1.dp, MaterialTheme.colorScheme.primary),
    ) {
        Column(
            modifier = Modifier.padding(12.dp),
            verticalArrangement = Arrangement.spacedBy(4.dp),
        ) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(
                    "TODO",
                    style = MaterialTheme.typography.labelMedium,
                    color = MaterialTheme.colorScheme.primary,
                    fontWeight = FontWeight.Bold,
                )
                Spacer(Modifier.weight(1f))
                if (items.isNotEmpty()) {
                    Text(
                        "$done/${items.size}",
                        style = MaterialTheme.typography.labelSmall,
                        color = MaterialTheme.colorScheme.onTertiaryContainer,
                    )
                }
            }
            when {
                !entry.toolDone && entry.toolOutput.isBlank() -> Text(
                    "Updating tasks…",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onTertiaryContainer,
                )
                items.isEmpty() -> Text(
                    if (entry.toolOutput.contains("no tasks", ignoreCase = true)) {
                        "No tasks."
                    } else {
                        entry.toolOutput.ifBlank { "No tasks." }
                    },
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.onTertiaryContainer,
                )
                else -> {
                    items.forEach { item ->
                        val color = when (item.status) {
                            "in_progress" -> MaterialTheme.colorScheme.primary
                            "completed" -> MaterialTheme.colorScheme.onSurfaceVariant
                            else -> MaterialTheme.colorScheme.onTertiaryContainer
                        }
                        Row(verticalAlignment = Alignment.Top) {
                            Text(
                                todoMark(item.status),
                                modifier = Modifier.width(22.dp),
                                style = MaterialTheme.typography.bodySmall,
                                color = color,
                            )
                            Text(
                                item.content,
                                style = MaterialTheme.typography.bodySmall,
                                color = color,
                                fontWeight = if (item.status == "in_progress") {
                                    FontWeight.SemiBold
                                } else {
                                    FontWeight.Normal
                                },
                                textDecoration = if (item.status == "completed") {
                                    TextDecoration.LineThrough
                                } else {
                                    TextDecoration.None
                                },
                            )
                        }
                    }
                }
            }
            if (entry.toolError) {
                Text(
                    "Todo update failed.",
                    style = MaterialTheme.typography.bodySmall,
                    color = MaterialTheme.colorScheme.error,
                )
            }
        }
    }
}

private fun isTodoTool(name: String): Boolean =
    name.equals("todo", ignoreCase = true) || name.equals("todowrite", ignoreCase = true)

private fun toolCommand(name: String, args: JSONObject?): String {
    if (args == null) return name
    return when (name) {
        "git" -> {
            val action = args.optString("action").trim()
            val detail = args.optString("url").ifBlank {
                args.optString("path").ifBlank { args.optString("repo") }
            }.trim()
            listOf("git", action, detail).filter { it.isNotBlank() }.joinToString(" ")
        }
        "read", "ls", "grep", "find" -> {
            val detail = args.optString("path").ifBlank {
                args.optString("pattern").ifBlank { args.optString("query") }
            }.trim()
            if (detail.isBlank()) name else "$name $detail"
        }
        "todo" -> {
            val count = args.optJSONArray("todos")?.length() ?: 0
            if (count > 0) "todo ($count tasks)" else "todo"
        }
        else -> name
    }
}

private fun readGitBranch(root: File): String = runCatching {
    val head = File(root, ".git/HEAD")
    if (!head.isFile) return@runCatching "no repo"
    val value = head.readText().trim()
    when {
        value.startsWith("ref: refs/heads/") -> value.removePrefix("ref: refs/heads/")
        value.length >= 7 -> value.take(7)
        else -> "detached"
    }
}.getOrDefault("no repo")

private fun installApk(context: android.content.Context, path: String): Boolean = runCatching {
    val file = File(path)
    if (!file.isFile) return@runCatching false
    val uri = FileProvider.getUriForFile(
        context,
        "${context.packageName}.fileprovider",
        file,
    )
    val intent = Intent(Intent.ACTION_VIEW).apply {
        setDataAndType(uri, "application/vnd.android.package-archive")
        addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION or Intent.FLAG_ACTIVITY_NEW_TASK)
    }
    context.startActivity(intent)
    true
}.getOrDefault(false)

private fun parseTodoItems(raw: String): List<TodoItem> {
    val items = mutableListOf<TodoItem>()
    for (line in raw.lineSequence()) {
        val text = line.trim()
        val status = when {
            text.startsWith("[x]", ignoreCase = true) -> "completed"
            text.startsWith("[~]") -> "in_progress"
            text.startsWith("[ ]") -> "pending"
            else -> continue
        }
        val content = text.substring(3).trim()
        if (content.isNotEmpty()) items.add(TodoItem(content, status))
    }
    return items
}

private fun todoMark(status: String): String = when (status) {
    "completed" -> "✓"
    "in_progress" -> "●"
    else -> "○"
}

@Composable
private fun Composer(
    prompt: String,
    enabled: Boolean,
    running: Boolean,
    onPrompt: (String) -> Unit,
    onSend: () -> Unit,
) {
    val s = LocalStrings.current
    Surface(
        tonalElevation = 2.dp,
        modifier = Modifier.fillMaxWidth().imePadding().navigationBarsPadding(),
    ) {
        Row(
            modifier = Modifier.padding(12.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            OutlinedTextField(
                value = prompt,
                onValueChange = onPrompt,
                label = { Text(s.task) },
                modifier = Modifier.weight(1f),
                maxLines = 4,
                enabled = !running,
            )
            Spacer(Modifier.width(8.dp))
            Button(enabled = enabled, onClick = onSend) { Text(s.send) }
        }
    }
}

@Composable
private fun SettingsScreen(
    version: String,
    providers: List<ProviderOption>,
    provider: String,
    model: String,
    thinking: String,
    permissionMode: String,
    language: String,
    baseUrl: String,
    apiKey: String,
    gitToken: String,
    tokenLoaded: Boolean,
    projectName: String,
    onProviderPick: (String, String, String) -> Unit,
    onRequestModels: (String) -> Unit,
    onPermissionPick: (String) -> Unit,
    onLanguagePick: (String) -> Unit,
    onConnectionPick: (String, String) -> Unit,
    onTokenChange: (String) -> Unit,
    onForgetToken: () -> Unit,
    onClose: () -> Unit,
    onReconnect: () -> Unit,
) {
    val s = LocalStrings.current
    Scaffold(
        topBar = {
            TopAppBar(
                title = { Text(s.settings) },
                navigationIcon = {
                    TextButton(onClick = onClose) { Text(s.back) }
                },
                actions = {
                    TextButton(onClick = onClose) { Text(s.done) }
                },
            )
        },
    ) { inner ->
        Column(
            modifier = Modifier
                .fillMaxSize()
                .padding(inner)
                .verticalScroll(rememberScrollState())
                .padding(16.dp),
            verticalArrangement = Arrangement.spacedBy(12.dp),
        ) {
            Text(s.model, style = MaterialTheme.typography.titleMedium)
            ModelPicker(
                providers = providers,
                provider = provider,
                model = model,
                thinking = thinking,
                onPick = onProviderPick,
                onRequestModels = onRequestModels,
            )
            Text(s.connection, style = MaterialTheme.typography.titleMedium)
            ConnectionPicker(
                baseUrl = baseUrl,
                apiKey = apiKey,
                onPick = onConnectionPick,
            )
            Text(s.permissions, style = MaterialTheme.typography.titleMedium)
            PermissionModePicker(
                mode = permissionMode,
                onPick = onPermissionPick,
            )
            Text(s.language, style = MaterialTheme.typography.titleMedium)
            LanguagePicker(language = language, onPick = onLanguagePick)
            Text(s.github, style = MaterialTheme.typography.titleMedium)
            OutlinedTextField(
                value = gitToken,
                onValueChange = onTokenChange,
                label = { Text(s.fineGrainedPat) },
                singleLine = true,
                visualTransformation = PasswordVisualTransformation(),
                enabled = tokenLoaded,
                modifier = Modifier.fillMaxWidth(),
            )
            Text(
                s.tokenHint,
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            TextButton(enabled = tokenLoaded, onClick = onForgetToken) {
                Text(s.forgetToken)
            }
            Text(
                "${s.project}: $projectName",
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Button(
                onClick = onReconnect,
                modifier = Modifier.fillMaxWidth(),
            ) { Text(s.applyReconnect) }
            Text(
                "Golder $version",
                style = MaterialTheme.typography.labelSmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
    }
}

@Composable
private fun ProjectDrawer(
    projects: List<Project>,
    activeId: String,
    onSelect: (Project) -> Unit,
    onNew: () -> Unit,
) {
    val s = LocalStrings.current
    ModalDrawerSheet(modifier = Modifier.width(300.dp)) {
        Column(modifier = Modifier.fillMaxSize().padding(16.dp)) {
            Text("Golder", style = MaterialTheme.typography.titleLarge)
            Spacer(Modifier.height(4.dp))
            Text(
                s.projects,
                style = MaterialTheme.typography.labelMedium,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
            Spacer(Modifier.height(8.dp))
            LazyColumn(modifier = Modifier.weight(1f)) {
                items(projects, key = { it.id }) { project ->
                    NavigationDrawerItem(
                        label = {
                            Column {
                                Text(project.name, maxLines = 1, overflow = TextOverflow.Ellipsis)
                                Text(
                                    project.path,
                                    style = MaterialTheme.typography.labelSmall,
                                    color = MaterialTheme.colorScheme.onSurfaceVariant,
                                    maxLines = 1,
                                    softWrap = false,
                                    overflow = TextOverflow.Clip,
                                    modifier = Modifier.fillMaxWidth().basicMarquee(),
                                )
                            }
                        },
                        selected = project.id == activeId,
                        onClick = { onSelect(project) },
                        modifier = Modifier.padding(vertical = 2.dp),
                    )
                }
            }
            TextButton(onClick = onNew, modifier = Modifier.fillMaxWidth()) {
                Text(s.newProject)
            }
        }
    }
}

@Composable
private fun NewProjectDialog(
    onDismiss: () -> Unit,
    onCreate: (String) -> Unit,
) {
    val s = LocalStrings.current
    var name by remember { mutableStateOf("") }
    AlertDialog(
        onDismissRequest = onDismiss,
        title = { Text(s.newProject) },
        text = {
            OutlinedTextField(
                value = name,
                onValueChange = { name = it },
                label = { Text(s.projectName) },
                singleLine = true,
                modifier = Modifier.fillMaxWidth(),
            )
        },
        confirmButton = {
            TextButton(
                enabled = name.isNotBlank(),
                onClick = { onCreate(name.trim()) },
            ) { Text(s.create) }
        },
        dismissButton = {
            TextButton(onClick = onDismiss) { Text(s.cancel) }
        },
    )
}

@Composable
private fun PermissionModePicker(
    mode: String,
    onPick: (String) -> Unit,
) {
    val s = LocalStrings.current
    var open by remember { mutableStateOf(false) }
    val current = permissionModeOptions.firstOrNull { it.id == mode }
        ?: permissionModeOptions.last()
    Card(modifier = Modifier.fillMaxWidth().clickable { open = true }) {
        Column(modifier = Modifier.padding(12.dp)) {
            Text("Permission mode", style = MaterialTheme.typography.labelSmall)
            Text(
                current.label,
                style = MaterialTheme.typography.bodyLarge,
            )
            Text(
                modeDescription(current.id, s),
                style = MaterialTheme.typography.bodySmall,
                color = MaterialTheme.colorScheme.onSurfaceVariant,
            )
        }
    }
    if (!open) return
    AlertDialog(
        onDismissRequest = { open = false },
        title = { Text("Permission mode") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(2.dp)) {
                permissionModeOptions.forEach { option ->
                    Row(
                        modifier = Modifier
                            .fillMaxWidth()
                            .clickable {
                                onPick(option.id)
                                open = false
                            }
                            .padding(vertical = 6.dp),
                        verticalAlignment = Alignment.Top,
                    ) {
                        RadioButton(
                            selected = option.id == mode,
                            onClick = {
                                onPick(option.id)
                                open = false
                            },
                        )
                        Column(modifier = Modifier.padding(top = 10.dp)) {
                            Text(option.label, style = MaterialTheme.typography.bodyMedium)
                            Text(
                                modeDescription(option.id, s),
                                style = MaterialTheme.typography.bodySmall,
                                color = MaterialTheme.colorScheme.onSurfaceVariant,
                            )
                        }
                    }
                }
            }
        },
        confirmButton = {
            TextButton(onClick = { open = false }) { Text("Close") }
        },
    )
}

@Composable
private fun LanguagePicker(
    language: String,
    onPick: (String) -> Unit,
) {
    val s = LocalStrings.current
    var open by remember { mutableStateOf(false) }
    val current = if (language == AppLanguage.ZH.id) s.chinese else s.english
    Card(modifier = Modifier.fillMaxWidth().clickable { open = true }) {
        Column(modifier = Modifier.padding(12.dp)) {
            Text(s.language, style = MaterialTheme.typography.labelSmall)
            Text(current, style = MaterialTheme.typography.bodyLarge)
        }
    }
    if (!open) return
    AlertDialog(
        onDismissRequest = { open = false },
        title = { Text(s.language) },
        text = {
            Column {
                listOf(AppLanguage.EN.id to s.english, AppLanguage.ZH.id to s.chinese)
                    .forEach { (id, label) ->
                        Row(
                            modifier = Modifier
                                .fillMaxWidth()
                                .clickable {
                                    onPick(id)
                                    open = false
                                }
                                .padding(vertical = 6.dp),
                            verticalAlignment = Alignment.CenterVertically,
                        ) {
                            RadioButton(
                                selected = language == id,
                                onClick = {
                                    onPick(id)
                                    open = false
                                },
                            )
                            Text(label, style = MaterialTheme.typography.bodyMedium)
                        }
                    }
            }
        },
        confirmButton = {
            TextButton(onClick = { open = false }) { Text(s.done) }
        },
    )
}

private fun modeDescription(id: String, s: UiStrings): String = when (id) {
    "read-only" -> s.readOnlyDesc
    "ask" -> s.askDesc
    "full-access" -> s.fullAccessDesc
    else -> s.autoDesc
}

@Composable
private fun ModelPicker(
    providers: List<ProviderOption>,
    provider: String,
    model: String,
    thinking: String,
    onPick: (String, String, String) -> Unit,
    onRequestModels: (String) -> Unit,
) {
    val s = LocalStrings.current
    var open by remember { mutableStateOf(false) }
    Card(modifier = Modifier.fillMaxWidth().clickable { open = true }) {
        Column(modifier = Modifier.padding(12.dp)) {
            Text(s.providerModelReasoning, style = MaterialTheme.typography.labelSmall)
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
    LaunchedEffect(open, current.name) {
        if (open) onRequestModels(current.name)
    }
    var custom by remember(open) { mutableStateOf(draftModel !in current.models) }

    AlertDialog(
        onDismissRequest = { open = false },
        title = { Text("Provider & model") },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                Text(s.provider, style = MaterialTheme.typography.labelSmall)
                DropdownField(current.name, choices.map { it.name }) { name ->
                    val next = choices.firstOrNull { it.name == name } ?: current
                    draftProvider = next.name
                    draftModel = next.defaultModel
                    draftThinking = defaultReasoning(next.reasoning)
                    custom = false
                }
                Text(s.model, style = MaterialTheme.typography.labelSmall)
                DropdownField(draftModel, current.models.distinct() + s.customModel) { value ->
                    if (value == s.customModel) {
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
                        label = { Text(s.modelId) },
                        singleLine = true,
                        modifier = Modifier.fillMaxWidth(),
                    )
                }
                Text(s.reasoning, style = MaterialTheme.typography.labelSmall)
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
            ) { Text(s.use) }
        },
        dismissButton = {
            TextButton(onClick = { open = false }) { Text(s.cancel) }
        },
    )
}

@Composable
private fun ConnectionPicker(
    baseUrl: String,
    apiKey: String,
    onPick: (String, String) -> Unit,
) {
    val s = LocalStrings.current
    var open by remember { mutableStateOf(false) }
    val endpoint = baseUrl.ifBlank { s.defaultEndpoint }
    val key = if (apiKey.isBlank()) s.apiKeyNotSet else s.apiKeyMasked
    Card(modifier = Modifier.fillMaxWidth().clickable { open = true }) {
        Column(modifier = Modifier.padding(12.dp)) {
            Text(s.connection, style = MaterialTheme.typography.labelSmall)
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
        title = { Text(s.connection) },
        text = {
            Column(verticalArrangement = Arrangement.spacedBy(8.dp)) {
                OutlinedTextField(
                    value = draftUrl,
                    onValueChange = { draftUrl = it },
                    label = { Text(s.baseUrl) },
                    singleLine = true,
                    modifier = Modifier.fillMaxWidth(),
                )
                OutlinedTextField(
                    value = draftKey,
                    onValueChange = { draftKey = it },
                    label = { Text(s.apiKey) },
                    singleLine = true,
                    visualTransformation = PasswordVisualTransformation(),
                    modifier = Modifier.fillMaxWidth(),
                )
                Text(
                    s.encryptedHint,
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
            ) { Text(s.use) }
        },
        dismissButton = {
            TextButton(onClick = { open = false }) { Text(s.cancel) }
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
