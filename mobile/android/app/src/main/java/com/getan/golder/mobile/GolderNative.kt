package com.getan.golder.mobile

/**
 * JNI surface exported by cmd/golder-mobile. All calls except version/lastError
 * take a handle from [open]; 0 means failure and the reason is in [lastError].
 */
object GolderNative {
    init {
        System.loadLibrary("golder_mobile")
    }

    external fun version(): String
    external fun catalog(): String
    external fun lastError(): String
    external fun open(configJson: String): Int
    external fun start(handle: Int, prompt: String): Int
    external fun poll(handle: Int, max: Int): String?
    external fun snapshot(handle: Int): String?
    external fun cancel(handle: Int)
    external fun close(handle: Int)
}
