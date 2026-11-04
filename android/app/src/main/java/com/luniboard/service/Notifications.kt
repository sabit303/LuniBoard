package com.luniboard.service

import android.Manifest
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import com.luniboard.MainActivity
import com.luniboard.R
import com.luniboard.domain.ReceivedClip
import com.luniboard.sharing.SendClipboardActivity

object Notifications {
    const val CHANNEL_SERVICE = "sync_service"
    const val CHANNEL_RECEIVED = "clipboard_received"
    const val ID_SERVICE = 1
    private const val ID_RECEIVED = 2

    fun ensureChannels(context: Context) {
        val nm = context.getSystemService(NotificationManager::class.java)
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_SERVICE, context.getString(R.string.channel_service), NotificationManager.IMPORTANCE_LOW).apply {
                description = context.getString(R.string.channel_service_description)
                setShowBadge(false)
            },
        )
        nm.createNotificationChannel(
            NotificationChannel(CHANNEL_RECEIVED, context.getString(R.string.channel_received), NotificationManager.IMPORTANCE_LOW).apply {
                description = context.getString(R.string.channel_received_description)
            },
        )
    }

    private fun openApp(context: Context) = PendingIntent.getActivity(
        context, 0, Intent(context, MainActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_SINGLE_TOP),
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT,
    )

    fun service(context: Context, text: String) = NotificationCompat.Builder(context, CHANNEL_SERVICE)
        .setSmallIcon(R.drawable.ic_notification)
        .setContentTitle(context.getString(R.string.app_name))
        .setContentText(text)
        .setOngoing(true)
        .setOnlyAlertOnce(true)
        .setContentIntent(openApp(context))
        .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
        .addAction(
            0, context.getString(R.string.action_send_clipboard),
            PendingIntent.getActivity(
                context, 1, Intent(context, SendClipboardActivity::class.java).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK),
                PendingIntent.FLAG_IMMUTABLE,
            ),
        )
        .addAction(
            0, context.getString(R.string.action_stop),
            PendingIntent.getService(
                context, 2, Intent(context, SyncService::class.java).setAction(SyncService.ACTION_STOP),
                PendingIntent.FLAG_IMMUTABLE,
            ),
        )
        .build()

    fun showReceived(context: Context, clip: ReceivedClip) {
        if (!canPost(context)) return
        val preview = clip.text.replace(Regex("\\s+"), " ").take(80)
        val n = NotificationCompat.Builder(context, CHANNEL_RECEIVED)
            .setSmallIcon(R.drawable.ic_notification)
            .setContentTitle(context.getString(R.string.notification_received_title, clip.fromName))
            .setContentText(preview)
            // The content may be sensitive: hide it on the lock screen.
            .setVisibility(NotificationCompat.VISIBILITY_PRIVATE)
            .setContentIntent(openApp(context))
            .setAutoCancel(true)
            .setTimeoutAfter(60_000)
            .build()
        NotificationManagerCompat.from(context).notify(ID_RECEIVED, n)
    }

    fun canPost(context: Context) = Build.VERSION.SDK_INT < 33 ||
        ContextCompat.checkSelfPermission(context, Manifest.permission.POST_NOTIFICATIONS) == PackageManager.PERMISSION_GRANTED
}
