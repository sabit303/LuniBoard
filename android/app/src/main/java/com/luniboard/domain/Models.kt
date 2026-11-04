package com.luniboard.domain

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable

@Serializable
data class Permissions(
    /** Send content from this phone to the device. */
    val send: Boolean = true,
    /** Apply content received from the device to this phone's clipboard. */
    val receive: Boolean = true,
)

/** A trusted computer. Its certificate fingerprint was pinned at pairing. */
@Serializable
data class PairedDevice(
    val id: String,
    val name: String,
    val platform: String,
    @SerialName("app_version") val appVersion: String? = null,
    val fingerprint: String,
    @SerialName("paired_at") val pairedAt: Long,
    @SerialName("last_seen") val lastSeen: Long = 0,
    /** host:port addresses, most recently working first. */
    val addresses: List<String> = emptyList(),
    val permissions: Permissions = Permissions(),
)

enum class ConnectionStatus {
    /** Synchronization disabled or the app is not allowed to connect right now. */
    IDLE,
    CONNECTING,
    CONNECTED,
    /** Not reachable; retrying with backoff. */
    OFFLINE,
    /** The computer no longer trusts this phone (it was unpaired there). */
    UNAUTHORIZED,
}

data class PeerState(
    val status: ConnectionStatus = ConnectionStatus.IDLE,
    val address: String? = null,
    val detail: String? = null,
    val since: Long = 0,
)

object Direction {
    const val OUTGOING = "outgoing"
    const val INCOMING = "incoming"
}

object Source {
    const val SHARE = "share"
    const val MANUAL = "manual"
    const val CLIPBOARD = "clipboard"
    const val PEER = "peer"
}

@Serializable
data class HistoryEntry(
    @SerialName("event_id") val eventId: String,
    @SerialName("origin_device_id") val originDeviceId: String,
    @SerialName("from_device_id") val fromDeviceId: String? = null,
    val direction: String,
    val source: String,
    @SerialName("content_type") val contentType: String,
    val text: String,
    val timestamp: Long,
    @SerialName("recorded_at") val recordedAt: Long,
    /** device id -> "sent", "failed" or an ack status. */
    val deliveries: Map<String, String> = emptyMap(),
)

@Serializable
data class AppSettings(
    @SerialName("device_name") val deviceName: String = "Android device",
    /** Master switch: when off, no connections are made. */
    @SerialName("sync_enabled") val syncEnabled: Boolean = true,
    /** Apply clipboard content received from computers. */
    @SerialName("receive_enabled") val receiveEnabled: Boolean = true,
    /** Keep connections in a foreground service while the app is in the background. */
    @SerialName("background_connection") val backgroundConnection: Boolean = false,
    /** Post a notification when content from a computer was copied. */
    @SerialName("notify_on_receive") val notifyOnReceive: Boolean = true,
    @SerialName("history_enabled") val historyEnabled: Boolean = true,
    @SerialName("history_persist") val historyPersist: Boolean = true,
    @SerialName("history_max_entries") val historyMaxEntries: Int = 100,
)

data class SendOutcome(val status: String, val reason: String? = null) {
    val delivered get() = status == "applied" || status == "duplicate"
}

data class SendResult(val eventId: String, val results: Map<String, SendOutcome>)

/** Emitted when a computer's content was written to the clipboard. */
data class ReceivedClip(val fromDeviceId: String, val fromName: String, val text: String)
