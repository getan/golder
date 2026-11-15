package com.getan.golder.mobile.androiduse

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import com.getan.golder.mobile.MainActivity

object AndroidUseNotifier {
    private const val CHANNEL_ID = "android-use"
    private const val NOTIFICATION_ID = 4242
    const val ACTION_TOGGLE = "com.getan.golder.mobile.action.ANDROID_USE_TOGGLE"

    fun update(context: Context, action: String, packageName: String, paused: Boolean) {
        val manager = context.getSystemService(NotificationManager::class.java) ?: return
        manager.createNotificationChannel(
            NotificationChannel(
                CHANNEL_ID,
                "AndroidUse",
                NotificationManager.IMPORTANCE_LOW,
            ).apply {
                description = "AndroidUse control status"
                setShowBadge(false)
            },
        )
        val open = PendingIntent.getActivity(
            context,
            1,
            Intent(context, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        val toggle = PendingIntent.getBroadcast(
            context,
            2,
            Intent(context, AndroidUseReceiver::class.java).setAction(ACTION_TOGGLE),
            PendingIntent.FLAG_UPDATE_CURRENT or PendingIntent.FLAG_IMMUTABLE,
        )
        val title = if (packageName.isBlank()) "AndroidUse" else "AndroidUse · $packageName"
        val text = if (paused) {
            "Paused — control actions are blocked"
        } else {
            "Active — $action"
        }
        val notification = Notification.Builder(context, CHANNEL_ID)
            .setSmallIcon(android.R.drawable.ic_menu_view)
            .setContentTitle(title)
            .setContentText(text)
            .setContentIntent(open)
            .setOngoing(true)
            .setOnlyAlertOnce(true)
            .addAction(0, if (paused) "Resume" else "Pause", toggle)
            .build()
        manager.notify(NOTIFICATION_ID, notification)
    }

    fun cancel(context: Context) {
        context.getSystemService(NotificationManager::class.java)?.cancel(NOTIFICATION_ID)
    }
}

class AndroidUseReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent?) {
        if (intent?.action != AndroidUseNotifier.ACTION_TOGGLE) return
        val paused = !AndroidUseBridge.isPaused(context)
        AndroidUseBridge.setPaused(context, paused)
        AndroidUseNotifier.update(context, "toggle", "", paused)
    }
}
