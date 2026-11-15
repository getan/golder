package com.getan.golder.mobile

import android.content.Context
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.delay
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.io.IOException
import java.net.HttpURLConnection
import java.net.URL
import java.net.URLEncoder

/**
 * GitHub OAuth App Device Flow. The client ID is public; no client secret is
 * stored in the app. Access tokens expire after eight hours and are refreshed
 * with the rotating refresh token, which is encrypted by SecureStore.
 */
object GitHubOAuth {
    const val CLIENT_ID = "Ov23linhLe0rbhkJgG5B"
    const val VERIFICATION_URI = "https://github.com/login/device"

    private const val SCOPE = "repo workflow"
    private const val DEVICE_CODE_URL = "https://github.com/login/device/code"
    private const val TOKEN_URL = "https://github.com/login/oauth/access_token"
    private const val REFRESH_SKEW_MS = 5 * 60 * 1000L

    data class DeviceCode(
        val deviceCode: String,
        val userCode: String,
        val verificationUri: String,
        val intervalSeconds: Int,
        val expiresAtMillis: Long,
    )

    data class Session(
        val accessToken: String,
        val refreshToken: String,
        val expiresAtMillis: Long,
    )

    fun isConnected(context: Context): Boolean = SecureStore.hasOAuth(context)

    fun revokeUrl(): String = "https://github.com/settings/connections/applications/$CLIENT_ID"

    suspend fun requestDeviceCode(): DeviceCode = withContext(Dispatchers.IO) {
        val json = postForm(
            DEVICE_CODE_URL,
            linkedMapOf(
                "client_id" to CLIENT_ID,
                "scope" to SCOPE,
            ),
        )
        if (json.optString("error").isNotBlank()) {
            throw IOException(json.optString("error_description").ifBlank { json.optString("error") })
        }
        val deviceCode = json.optString("device_code")
        val userCode = json.optString("user_code")
        if (deviceCode.isBlank() || userCode.isBlank()) {
            throw IOException("GitHub did not return a device code")
        }
        val expiresIn = json.optInt("expires_in", 900).coerceAtLeast(60)
        DeviceCode(
            deviceCode = deviceCode,
            userCode = userCode,
            verificationUri = json.optString("verification_uri", VERIFICATION_URI),
            intervalSeconds = json.optInt("interval", 5).coerceAtLeast(1),
            expiresAtMillis = System.currentTimeMillis() + expiresIn * 1000L,
        )
    }

    suspend fun pollForToken(code: DeviceCode): Session = withContext(Dispatchers.IO) {
        var interval = code.intervalSeconds
        while (System.currentTimeMillis() < code.expiresAtMillis) {
            delay(interval * 1000L)
            val json = postForm(
                TOKEN_URL,
                linkedMapOf(
                    "client_id" to CLIENT_ID,
                    "device_code" to code.deviceCode,
                    "grant_type" to "urn:ietf:params:oauth:grant-type:device_code",
                ),
            )
            if (json.optString("access_token").isNotBlank()) {
                return@withContext sessionFromJson(json)
            }
            when (json.optString("error")) {
                "authorization_pending" -> Unit
                "slow_down" -> interval += 5
                "expired_token" -> throw IOException("GitHub device code expired; start again")
                "access_denied" -> throw IOException("GitHub authorization was cancelled")
                "device_flow_disabled" -> throw IOException("GitHub OAuth App has Device Flow disabled")
                else -> throw IOException(
                    json.optString("error_description").ifBlank {
                        json.optString("error").ifBlank { "GitHub authorization failed" }
                    },
                )
            }
        }
        throw IOException("GitHub device code expired; start again")
    }

    fun saveSession(context: Context, session: Session): Boolean =
        SecureStore.saveOAuth(context, session.accessToken, session.refreshToken, session.expiresAtMillis)

    fun clearSession(context: Context) = SecureStore.clearToken(context)

    /**
     * Return a usable token. A manual PAT is returned unchanged. An OAuth
     * session is refreshed five minutes before the eight-hour access token
     * expires; the rotated refresh token is persisted immediately.
     */
    suspend fun freshAccessToken(context: Context, fallbackPat: String): String = withContext(Dispatchers.IO) {
        val state = SecureStore.oauthState(context)
        if (state.refresh.isBlank()) return@withContext fallbackPat
        val access = state.access
        val expiresAt = state.expiresAt
        val now = System.currentTimeMillis()
        if (access.isNotBlank() && (expiresAt <= 0L || now < expiresAt - REFRESH_SKEW_MS)) {
            return@withContext access
        }
        try {
            val json = postForm(
                TOKEN_URL,
                linkedMapOf(
                    "client_id" to CLIENT_ID,
                    "grant_type" to "refresh_token",
                    "refresh_token" to state.refresh,
                ),
            )
            val session = sessionFromJson(json)
            if (!saveSession(context, session)) {
                SecureStore.clearToken(context)
                return@withContext ""
            }
            session.accessToken
        } catch (_: Exception) {
            SecureStore.clearToken(context)
            ""
        }
    }

    private fun sessionFromJson(json: JSONObject): Session {
        val access = json.optString("access_token")
        if (access.isBlank()) {
            throw IOException(json.optString("error_description").ifBlank { "GitHub token exchange failed" })
        }
        val expiresIn = json.optInt("expires_in", 0)
        return Session(
            access,
            refreshValue(json),
            if (expiresIn > 0) System.currentTimeMillis() + expiresIn * 1000L else 0L,
        )
    }

    private fun refreshValue(json: JSONObject): String {
        val key = "refresh" + "_token"
        return json.optString(key)
    }

    private fun postForm(url: String, values: Map<String, String>): JSONObject {
        val body = values.entries.joinToString("&") {
            "${URLEncoder.encode(it.key, "UTF-8")}=${URLEncoder.encode(it.value, "UTF-8")}"
        }
        val connection = (URL(url).openConnection() as HttpURLConnection).apply {
            requestMethod = "POST"
            doOutput = true
            connectTimeout = 15_000
            readTimeout = 20_000
            setRequestProperty("Accept", "application/json")
            setRequestProperty("Content-Type", "application/x-www-form-urlencoded")
            setRequestProperty("User-Agent", "Golder-Android")
        }
        try {
            connection.outputStream.use { it.write(body.toByteArray(Charsets.UTF_8)) }
            val stream = if (connection.responseCode in 200..299) {
                connection.inputStream
            } else {
                connection.errorStream
            }
            val text = stream?.bufferedReader()?.use { it.readText() }.orEmpty()
            if (text.isBlank()) {
                throw IOException("GitHub returned HTTP ${connection.responseCode}")
            }
            return JSONObject(text)
        } finally {
            connection.disconnect()
        }
    }
}
