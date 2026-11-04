package com.luniboard.di

import android.content.Context
import android.net.ConnectivityManager
import android.net.Network
import android.os.Build
import android.provider.Settings
import android.util.Log
import androidx.lifecycle.DefaultLifecycleObserver
import androidx.lifecycle.LifecycleOwner
import androidx.lifecycle.ProcessLifecycleOwner
import com.luniboard.BuildConfig
import com.luniboard.clipboard.AndroidClipboard
import com.luniboard.data.local.DeviceStore
import com.luniboard.data.local.HistoryStore
import com.luniboard.data.local.SettingsStore
import com.luniboard.data.network.ConnectionManager
import com.luniboard.data.network.DeviceInfo
import com.luniboard.data.network.NsdDiscovery
import com.luniboard.data.network.Platforms
import com.luniboard.data.network.Validation
import com.luniboard.data.repository.ClipboardRepository
import com.luniboard.domain.AppSettings
import com.luniboard.domain.PairedDevice
import com.luniboard.domain.SyncEngine
import com.luniboard.security.KeystoreIdentity
import com.luniboard.service.Notifications
import com.luniboard.service.SyncService
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.SupervisorJob
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch

/**
 * Manual dependency injection: constructs and wires the app's singletons.
 * Also decides when connections run: while synchronization is enabled and
 * either the app is visible or the user enabled background connection.
 */
class AppContainer(private val context: Context) {
    val scope = CoroutineScope(SupervisorJob() + Dispatchers.Default)

    val identity by lazy { KeystoreIdentity.loadOrCreate(context) }
    val clipboard = AndroidClipboard(context)

    private val dir = context.filesDir
    val settingsStore = SettingsStore(dir, AppSettings(deviceName = defaultDeviceName()))
    val deviceStore = DeviceStore(dir)
    val historyStore = HistoryStore(dir) { settingsStore.current }

    val discovery = NsdDiscovery(context, scope) { identity.deviceId }

    val engine = SyncEngine(
        selfId = { identity.deviceId },
        devices = deviceStore,
        history = historyStore,
        settings = { settingsStore.current },
        clipboard = clipboard,
    )

    val connections: ConnectionManager by lazy {
        ConnectionManager(
            identity = identity,
            devices = deviceStore,
            engine = engine,
            selfInfo = ::selfInfo,
            addressesFor = ::addressesFor,
            scope = scope,
            log = { Log.i("Connections", it) },
        )
    }

    val repository: ClipboardRepository by lazy {
        ClipboardRepository(identity, deviceStore, historyStore, settingsStore, engine, connections, discovery.devices, ::selfInfo)
    }

    private val uiVisible = MutableStateFlow(false)
    private val serviceRunning = MutableStateFlow(false)

    fun selfInfo(): DeviceInfo {
        val name = settingsStore.current.deviceName.trim().take(Validation.MAX_DEVICE_NAME_CHARS)
            .filterNot { Character.isISOControl(it) }.ifBlank { "Android device" }
        return DeviceInfo(identity.deviceId, name, Platforms.ANDROID, BuildConfig.VERSION_NAME)
    }

    /** Fresh mDNS addresses (only if they advertise the pinned fingerprint) first, then remembered ones. */
    private fun addressesFor(d: PairedDevice): List<String> {
        val found = discovery.devices.value[d.id]?.takeIf { it.fingerprint == d.fingerprint }?.addresses.orEmpty()
        return (found + d.addresses).distinct()
    }

    fun setServiceRunning(running: Boolean) = serviceRunning.update { running }

    fun start() {
        ProcessLifecycleOwner.get().lifecycle.addObserver(object : DefaultLifecycleObserver {
            override fun onStart(owner: LifecycleOwner) = uiVisible.update { true }
            override fun onStop(owner: LifecycleOwner) = uiVisible.update { false }
        })

        discovery.onFound = { found ->
            deviceStore.get(found.deviceId)?.takeIf { it.fingerprint == found.fingerprint }?.let { connections.kick() }
        }

        scope.launch {
            combine(settingsStore.settings, uiVisible, serviceRunning) { s, visible, service ->
                Triple(s.syncEnabled && (visible || service), visible || service, s.backgroundConnection)
            }.distinctUntilChanged().collect { (connect, discover, background) ->
                connections.setActive(connect)
                if (discover) discovery.start() else discovery.stop()
                if (background) SyncService.start(context) else SyncService.stop(context)
            }
        }

        scope.launch {
            engine.received.collect { clip ->
                if (settingsStore.current.notifyOnReceive) Notifications.showReceived(context, clip)
            }
        }

        context.getSystemService(ConnectivityManager::class.java).registerDefaultNetworkCallback(
            object : ConnectivityManager.NetworkCallback() {
                override fun onAvailable(network: Network) = connections.kick()
            },
        )
    }

    private fun defaultDeviceName(): String {
        val name = if (Build.VERSION.SDK_INT >= 25) Settings.Global.getString(context.contentResolver, Settings.Global.DEVICE_NAME) else null
        return (name?.takeIf { it.isNotBlank() } ?: "${Build.MANUFACTURER} ${Build.MODEL}").take(Validation.MAX_DEVICE_NAME_CHARS)
    }
}
