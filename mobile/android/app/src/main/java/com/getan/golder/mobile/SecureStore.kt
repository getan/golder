package com.getan.golder.mobile

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import android.util.Base64
import java.security.KeyStore
import javax.crypto.Cipher
import javax.crypto.KeyGenerator
import javax.crypto.SecretKey
import javax.crypto.spec.GCMParameterSpec

/**
 * Encrypts the GitHub token and model API key with an Android Keystore key
 * before putting them in SharedPreferences. Keystore material never leaves the
 * device; the preference contains only the IV and ciphertext. A changed lock
 * screen or a restored backup can invalidate the key, in which case the secret
 * reads as empty and the person enters it again.
 */
object SecureStore {
    private const val PREFS = "golder-secure"
    private const val TOKEN_KEY = "github-token"
    private const val API_KEY = "model-api-key"
    private const val KEY_ALIAS = "golder-mobile-secrets"
    private const val TRANSFORM = "AES/GCM/NoPadding"
    private const val GCM_TAG_BITS = 128

    fun saveToken(context: Context, token: String): Boolean =
        save(context, TOKEN_KEY, token)

    fun getToken(context: Context): String = read(context, TOKEN_KEY)

    fun clearToken(context: Context) = clear(context, TOKEN_KEY)

    fun saveAPIKey(context: Context, apiKey: String): Boolean =
        save(context, API_KEY, apiKey)

    fun getAPIKey(context: Context): String = read(context, API_KEY)

    fun clearAPIKey(context: Context) = clear(context, API_KEY)

    private fun save(context: Context, keyName: String, value: String): Boolean = runCatching {
        if (value.isBlank()) {
            clear(context, keyName)
            return@runCatching true
        }
        val cipher = Cipher.getInstance(TRANSFORM).apply {
            init(Cipher.ENCRYPT_MODE, key())
        }
        val ciphertext = cipher.doFinal(value.toByteArray(Charsets.UTF_8))
        val encoded = Base64.encodeToString(cipher.iv, Base64.NO_WRAP) + ":" +
            Base64.encodeToString(ciphertext, Base64.NO_WRAP)
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            .edit().putString(keyName, encoded).commit()
    }.getOrDefault(false)

    private fun read(context: Context, keyName: String): String = runCatching {
        val encoded = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            .getString(keyName, null) ?: return@runCatching ""
        val parts = encoded.split(":")
        if (parts.size != 2) return@runCatching ""
        val cipher = Cipher.getInstance(TRANSFORM).apply {
            init(
                Cipher.DECRYPT_MODE,
                key(),
                GCMParameterSpec(GCM_TAG_BITS, Base64.decode(parts[0], Base64.NO_WRAP)),
            )
        }
        String(cipher.doFinal(Base64.decode(parts[1], Base64.NO_WRAP)), Charsets.UTF_8)
    }.getOrDefault("")

    private fun clear(context: Context, keyName: String) {
        context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            .edit().remove(keyName).commit()
    }

    private fun key(): SecretKey {
        val store = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
        (store.getKey(KEY_ALIAS, null) as? SecretKey)?.let { return it }
        val generator = KeyGenerator.getInstance(KeyProperties.KEY_ALGORITHM_AES, "AndroidKeyStore")
        generator.init(
            KeyGenParameterSpec.Builder(
                KEY_ALIAS,
                KeyProperties.PURPOSE_ENCRYPT or KeyProperties.PURPOSE_DECRYPT,
            )
                .setBlockModes(KeyProperties.BLOCK_MODE_GCM)
                .setEncryptionPaddings(KeyProperties.ENCRYPTION_PADDING_NONE)
                .setRandomizedEncryptionRequired(true)
                .build(),
        )
        return generator.generateKey()
    }
}
