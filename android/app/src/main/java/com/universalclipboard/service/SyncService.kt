package com.universalclipboard.service

import android.app.ForegroundServiceStartNotAllowedException
import android.app.Service
import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent
import android.content.pm.ServiceInfo
import android.os.Build
import android.os.IBinder
import android.util.Log
import androidx.core.app.NotificationManagerCompat
import androidx.core.app.ServiceCompat
import androidx.core.content.ContextCompat
import com.universalclipboard.R
import com.universalclipboard.UniversalClipboardApp
import com.universalclipboard.domain.ConnectionStatus
import kotlinx.coroutines.Job
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.launch

/**
 * Optional foreground service (type connectedDevice) that keeps connections to
 * paired computers while the app is in the background, so content copied on a
 * computer can be written to this phone's clipboard. It is only started when
 * the user enables "Background connection", shows a permanent notification
 * and can be stopped from it at any time.
 */
class SyncService : Service() {
    private var job: Job? = null

    override fun onBind(intent: Intent?): IBinder? = null

    override fun onStartCommand(intent: Intent?, flags: Int, startId: Int): Int {
        val container = (application as UniversalClipboardApp).container
        if (intent?.action == ACTION_STOP) {
            container.repository.updateSettings { it.copy(backgroundConnection = false) }
            stopSelf()
            return START_NOT_STICKY
        }
        Notifications.ensureChannels(this)
        val type = if (Build.VERSION.SDK_INT >= 29) ServiceInfo.FOREGROUND_SERVICE_TYPE_CONNECTED_DEVICE else 0
        try {
            ServiceCompat.startForeground(this, Notifications.ID_SERVICE, Notifications.service(this, getString(R.string.service_starting)), type)
        } catch (e: Exception) {
            Log.w(TAG, "cannot start foreground service", e)
            stopSelf()
            return START_NOT_STICKY
        }
        container.setServiceRunning(true)
        if (job == null) {
            job = container.scope.launch {
                combine(container.repository.devices, container.repository.settings) { devices, settings ->
                    val connected = devices.count { it.state.status == ConnectionStatus.CONNECTED }
                    when {
                        !settings.syncEnabled -> getString(R.string.service_sync_off)
                        devices.isEmpty() -> getString(R.string.service_no_devices)
                        connected == 0 -> getString(R.string.service_waiting)
                        else -> resources.getQuantityString(R.plurals.service_connected, connected, connected)
                    }
                }.collect { text ->
                    if (Notifications.canPost(this@SyncService)) {
                        NotificationManagerCompat.from(this@SyncService)
                            .notify(Notifications.ID_SERVICE, Notifications.service(this@SyncService, text))
                    }
                }
            }
        }
        return START_STICKY
    }

    override fun onDestroy() {
        job?.cancel()
        (application as UniversalClipboardApp).container.setServiceRunning(false)
        super.onDestroy()
    }

    companion object {
        const val ACTION_STOP = "com.universalclipboard.action.STOP_SERVICE"
        private const val TAG = "SyncService"

        fun start(context: Context) {
            try {
                ContextCompat.startForegroundService(context, Intent(context, SyncService::class.java))
            } catch (e: IllegalStateException) {
                // Android 12+ forbids starting from the background; it starts next time the app opens.
                if (Build.VERSION.SDK_INT >= 31 && e is ForegroundServiceStartNotAllowedException) {
                    Log.i(TAG, "background start not allowed now")
                } else {
                    Log.w(TAG, "cannot start service", e)
                }
            }
        }

        fun stop(context: Context) {
            context.stopService(Intent(context, SyncService::class.java))
        }
    }
}

/** Restarts the background connection after reboot if the user enabled it. */
class BootReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action != Intent.ACTION_BOOT_COMPLETED && intent.action != Intent.ACTION_MY_PACKAGE_REPLACED) return
        val container = (context.applicationContext as UniversalClipboardApp).container
        val s = container.settingsStore.current
        if (s.backgroundConnection && s.syncEnabled) SyncService.start(context)
    }
}
