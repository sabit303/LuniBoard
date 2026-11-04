package com.luniboard.data.repository

import com.luniboard.data.local.DeviceStore
import com.luniboard.data.local.HistoryStore
import com.luniboard.data.local.SettingsStore
import com.luniboard.data.network.ConnectionManager
import com.luniboard.data.network.DeviceIdentity
import com.luniboard.data.network.DeviceInfo
import com.luniboard.data.network.DiscoveredDevice
import com.luniboard.data.network.PairRequest
import com.luniboard.data.network.PairStatus
import com.luniboard.data.network.PairingClient
import com.luniboard.data.network.PairingInvitation
import com.luniboard.data.network.Protocol
import com.luniboard.data.network.verificationCode
import com.luniboard.domain.AppSettings
import com.luniboard.domain.ConnectionStatus
import com.luniboard.domain.PairedDevice
import com.luniboard.domain.PeerState
import com.luniboard.domain.Permissions
import com.luniboard.domain.SendResult
import com.luniboard.domain.SyncEngine
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.combine
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.flowOf
import kotlinx.coroutines.withTimeoutOrNull

/** A paired computer with its live connection state, for display. */
data class DeviceWithState(val device: PairedDevice, val state: PeerState) {
    val connected get() = state.status == ConnectionStatus.CONNECTED
}

class PairingFailedException(message: String) : Exception(message)

/**
 * Single entry point for the UI layer. All synchronization rules live in
 * [SyncEngine]; this class only coordinates stores, pairing and connections.
 */
class ClipboardRepository(
    private val identity: DeviceIdentity,
    private val deviceStore: DeviceStore,
    private val historyStore: HistoryStore,
    private val settingsStore: SettingsStore,
    private val engine: SyncEngine,
    private val connections: ConnectionManager,
    private val discovered: StateFlow<Map<String, DiscoveredDevice>>,
    private val selfInfo: () -> DeviceInfo,
    private val clock: () -> Long = System::currentTimeMillis,
) {
    val settings: StateFlow<AppSettings> = settingsStore.settings
    val history = historyStore.entries
    val received = engine.received
    val fingerprint get() = identity.fingerprint
    val deviceId get() = identity.deviceId

    val devices: Flow<List<DeviceWithState>> = combine(deviceStore.devices, connections.states) { list, states ->
        list.map { DeviceWithState(it, states[it.id] ?: PeerState()) }
    }

    /** Nearby computers from mDNS that are not paired yet (or were re-keyed). */
    val nearby: Flow<List<DiscoveredDevice>> = combine(discovered, deviceStore.devices) { found, paired ->
        found.values.filter { d -> paired.none { it.id == d.deviceId && it.fingerprint == d.fingerprint } }.sortedBy { it.name }
    }

    fun device(id: String) = deviceStore.get(id)

    suspend fun send(text: String, targets: Collection<String>?, source: String): SendResult =
        engine.send(text, targets, source)

    /** Waits until at least one computer is connected (or the timeout passes). */
    suspend fun awaitConnected(timeoutMillis: Long): Boolean {
        if (connections.connectedCount() > 0) return true
        if (!settings.value.syncEnabled || deviceStore.devices.value.isEmpty()) return false
        connections.kick()
        return withTimeoutOrNull(timeoutMillis) {
            connections.states.first { s -> s.values.any { it.status == ConnectionStatus.CONNECTED } }
        } != null
    }

    fun verificationCodeFor(peerFingerprint: String) = verificationCode(identity.fingerprint, peerFingerprint)

    fun invitationFor(d: DiscoveredDevice, code: String) = PairingInvitation(
        device = DeviceInfo(d.deviceId, d.name, d.platform),
        fingerprint = d.fingerprint,
        addresses = d.addresses,
        code = code,
    )

    /**
     * Sends a pairing request. Suspends until the user approves or rejects it
     * on the computer, or its pairing window expires.
     */
    suspend fun pair(invitation: PairingInvitation): PairedDevice {
        val result = PairingClient(identity).pair(
            PairRequest(Protocol.VERSION, selfInfo(), invitation.code, listenPort = 0),
            invitation,
        )
        when (result.response.status) {
            PairStatus.APPROVED -> {}
            PairStatus.REJECTED -> throw PairingFailedException("${invitation.device.name} rejected the request.")
            else -> throw PairingFailedException("The pairing window on ${invitation.device.name} expired. Start pairing there again.")
        }
        val info = result.response.device
        val device = PairedDevice(
            id = info.id,
            name = info.name,
            platform = info.platform,
            appVersion = info.appVersion,
            fingerprint = invitation.fingerprint,
            pairedAt = clock(),
            addresses = (listOf(result.address) + invitation.addresses).distinct(),
            permissions = Permissions(),
        )
        deviceStore.add(device)
        connections.kick()
        return device
    }

    fun unpair(id: String) {
        deviceStore.remove(id)
    }

    fun setPermissions(id: String, permissions: Permissions) = deviceStore.setPermissions(id, permissions)

    fun retryConnections() = connections.kick()

    fun deleteHistoryEntry(eventId: String) = historyStore.delete(eventId)

    fun clearHistory() = historyStore.clear()

    fun updateSettings(fn: (AppSettings) -> AppSettings) {
        settingsStore.update(fn)
        historyStore.onSettingsChanged(settingsStore.current)
    }

    companion object {
        fun empty(): Flow<List<DeviceWithState>> = flowOf(emptyList())
    }
}
