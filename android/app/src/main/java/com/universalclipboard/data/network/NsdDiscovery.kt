package com.universalclipboard.data.network

import android.content.Context
import android.net.nsd.NsdManager
import android.net.nsd.NsdServiceInfo
import android.os.Build
import android.util.Log
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.launch
import kotlinx.coroutines.suspendCancellableCoroutine
import kotlinx.coroutines.withTimeoutOrNull
import java.net.Inet4Address
import java.net.Inet6Address
import java.net.InetAddress
import kotlin.coroutines.resume

/** A computer advertising Universal Clipboard on the local network. */
data class DiscoveredDevice(
    val deviceId: String,
    val name: String,
    val platform: String,
    val fingerprint: String,
    val addresses: List<String>,
    val serviceName: String,
)

/**
 * DNS-SD browsing with Android's NsdManager. Results are hints only: they are
 * used to show nearby computers and to find a paired computer's current
 * address, but connections are always authenticated with the pinned
 * certificate fingerprint.
 */
class NsdDiscovery(context: Context, private val scope: CoroutineScope, private val selfId: () -> String) {
    private val nsd = context.getSystemService(NsdManager::class.java)
    private val _devices = MutableStateFlow<Map<String, DiscoveredDevice>>(emptyMap())
    val devices: StateFlow<Map<String, DiscoveredDevice>> = _devices.asStateFlow()

    var onFound: (DiscoveredDevice) -> Unit = {}

    private var listener: NsdManager.DiscoveryListener? = null
    private var resolver: Job? = null
    private var queue = Channel<NsdServiceInfo>(Channel.UNLIMITED)

    @Synchronized
    fun start() {
        if (listener != null) return
        queue = Channel(Channel.UNLIMITED)
        val q = queue
        resolver = scope.launch { for (info in q) resolve(info)?.let(::add) }
        val l = object : NsdManager.DiscoveryListener {
            override fun onDiscoveryStarted(serviceType: String) {}
            override fun onDiscoveryStopped(serviceType: String) {}
            override fun onStartDiscoveryFailed(serviceType: String, errorCode: Int) {
                Log.w(TAG, "mDNS discovery failed to start: $errorCode")
                synchronized(this@NsdDiscovery) { if (listener === this) listener = null }
            }
            override fun onStopDiscoveryFailed(serviceType: String, errorCode: Int) {}
            override fun onServiceFound(info: NsdServiceInfo) {
                q.trySend(info)
            }
            override fun onServiceLost(info: NsdServiceInfo) {
                _devices.update { m -> m.filterValues { it.serviceName != info.serviceName } }
            }
        }
        listener = l
        runCatching { nsd.discoverServices(Protocol.SERVICE_TYPE, NsdManager.PROTOCOL_DNS_SD, l) }
            .onFailure { Log.w(TAG, "discoverServices", it); listener = null }
    }

    @Synchronized
    fun stop() {
        listener?.let { runCatching { nsd.stopServiceDiscovery(it) } }
        listener = null
        resolver?.cancel()
        queue.close()
    }

    /** Resolves one service at a time (older Android versions allow only one). */
    @Suppress("DEPRECATION")
    private suspend fun resolve(info: NsdServiceInfo): NsdServiceInfo? = withTimeoutOrNull(10_000) {
        suspendCancellableCoroutine { cont ->
            nsd.resolveService(info, object : NsdManager.ResolveListener {
                override fun onResolveFailed(serviceInfo: NsdServiceInfo, errorCode: Int) {
                    if (cont.isActive) cont.resume(null)
                }
                override fun onServiceResolved(serviceInfo: NsdServiceInfo) {
                    if (cont.isActive) cont.resume(serviceInfo)
                }
            })
        }
    }

    private fun add(info: NsdServiceInfo) {
        val txt = info.attributes.mapValues { (_, v) -> v?.decodeToString() ?: "" }
        val id = txt["id"] ?: return
        val fp = txt["fp"]?.lowercase() ?: return
        val version = txt["v"]?.toIntOrNull() ?: return
        if (!Validation.validId(id) || id == selfId() || !Validation.validFingerprint(fp) || !Protocol.supportsVersion(version)) return
        if (info.port !in 1..65535) return
        @Suppress("DEPRECATION")
        val hosts: List<InetAddress> = if (Build.VERSION.SDK_INT >= 34) info.hostAddresses else listOfNotNull(info.host)
        val addrs = hosts
            .filter { !it.isLoopbackAddress && !it.isLinkLocalAddress && !it.isAnyLocalAddress && !it.isMulticastAddress }
            .sortedBy { if (it is Inet4Address) 0 else 1 }
            .map { PairingUri.joinHostPort(if (it is Inet6Address) it.hostAddress!!.substringBefore('%') else it.hostAddress!!, info.port) }
        if (addrs.isEmpty()) return
        val platform = txt["os"].takeIf { it in Platforms.known } ?: "other"
        val name = txt["name"]?.takeIf { it.isNotBlank() && it.length <= 64 } ?: info.serviceName
        val d = DiscoveredDevice(id, name, platform, fp, addrs, info.serviceName)
        _devices.update { it + (id to d) }
        onFound(d)
    }

    private companion object {
        const val TAG = "NsdDiscovery"
    }
}
