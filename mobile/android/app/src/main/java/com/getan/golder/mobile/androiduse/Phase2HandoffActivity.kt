package com.getan.golder.mobile.androiduse

import android.app.Activity
import android.os.Bundle

/** Invisible one-shot handoff target for the shell-UID helper. */
class Phase2HandoffActivity : Activity() {
    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        val extras = intent.extras
        val accepted = Phase2Controller.acceptHandoff(
            this,
            extras?.getBinder("binder"),
            extras?.getString("nonce"),
            extras?.getString("apk"),
        )
        setResult(if (accepted) RESULT_OK else RESULT_CANCELED)
        finish()
    }
}
