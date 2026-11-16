package com.getan.golder.mobile.androiduse

import android.app.Activity
import android.os.Bundle
import android.widget.TextView

/** Isolated activity owned by the test APK, never a user's existing app task. */
class DisplayTestActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        setContentView(TextView(this).apply { text = "Background display test" })
    }
}
