package com.getan.golder.mobile

import android.content.Context
import java.io.File
import java.util.UUID
import org.json.JSONArray
import org.json.JSONObject

data class Project(val id: String, val name: String, val path: String)

data class ProjectsState(val projects: List<Project>, val activeId: String)

/**
 * Projects are app-private directories, each with its own workspace root. This
 * keeps Git's repository root and the selected project the same thing.
 */
object ProjectStore {
    private const val PREFS = "golder-projects"
    private const val PROJECTS = "projects"
    private const val ACTIVE = "active"
    private const val DEFAULT_ID = "default"

    fun load(context: Context): ProjectsState {
        val prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        val parsed = mutableListOf<Project>()
        runCatching {
            val raw = prefs.getString(PROJECTS, null) ?: return@runCatching
            val array = JSONArray(raw)
            for (i in 0 until array.length()) {
                val obj = array.getJSONObject(i)
                val id = obj.optString("id").trim()
                val name = obj.optString("name").trim()
                val path = obj.optString("path").trim()
                if (id.isNotEmpty() && name.isNotEmpty() && path.isNotEmpty()) {
                    parsed.add(Project(id, name, path))
                }
            }
        }
        if (parsed.isEmpty()) {
            val dir = File(context.filesDir, "workspace").apply { mkdirs() }
            parsed.add(Project(DEFAULT_ID, "Default project", dir.absolutePath))
        }
        val active = prefs.getString(ACTIVE, "").orEmpty()
            .takeIf { id -> parsed.any { it.id == id } }
            ?: parsed.first().id
        return ProjectsState(parsed, active)
    }

    fun save(context: Context, projects: List<Project>, activeId: String) {
        val array = JSONArray()
        for (project in projects) {
            array.put(
                JSONObject()
                    .put("id", project.id)
                    .put("name", project.name)
                    .put("path", project.path),
            )
        }
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            .edit()
            .putString(PROJECTS, array.toString())
            .putString(ACTIVE, activeId)
            .apply()
    }

    fun create(context: Context, name: String): Project {
        val id = UUID.randomUUID().toString().take(8)
        val dir = File(context.filesDir, "projects/$id").apply { mkdirs() }
        return Project(id, name.trim(), dir.absolutePath)
    }
}
