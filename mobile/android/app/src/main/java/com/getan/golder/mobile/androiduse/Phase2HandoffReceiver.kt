package com.getan.golder.mobile.androiduse

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.util.Log

/** Invisible one-shot handoff target for the shell-UID helper. */
class Phase2HandoffReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        val extras = intent.extras
        val accepted = Phase2Controller.acceptHandoff(
            context,
            extras?.getBinder("binder"),
            extras?.getString("nonce"),
            extras?.getString("apk"),
        )
        Log.i("GolderPhase2", "handoff accepted=$accepted")
    }
}
