package com.universalclipboard.data.network

import kotlinx.serialization.SerialName
import kotlinx.serialization.Serializable
import kotlinx.serialization.json.Json
import kotlinx.serialization.json.JsonElement
import kotlinx.serialization.json.JsonObject
import kotlinx.serialization.json.decodeFromJsonElement
import kotlinx.serialization.json.encodeToJsonElement

/**
 * Universal Clipboard protocol v1 (see protocol/protocol.md at the repository root).
 * These types mirror daemon/pkg/protocol and the JSON Schemas in protocol/schemas/v1.
 */
object Protocol {
    const val VERSION = 1
    const val MIN_SUPPORTED_VERSION = 1
    const val SERVICE_TYPE = "_uclip._tcp"
    const val PATH_PAIR = "/v1/pair"
    const val PATH_WEBSOCKET = "/v1/ws"
    const val WEBSOCKET_SUBPROTOCOL = "uclip.v1"

    const val TYPE_HELLO = "hello"
    const val TYPE_EVENT = "clipboard.event"
    const val TYPE_ACK = "clipboard.ack"
    const val TYPE_ERROR = "error"

    const val CAPABILITY_SEND = "clipboard.send"
    const val CAPABILITY_RECEIVE = "clipboard.receive"

    const val CONTENT_TEXT = "text"
    const val CONTENT_URL = "url"

    const val HARD_MAX_PAYLOAD_BYTES = 16L shl 20
    const val ENVELOPE_OVERHEAD_BYTES = 16L shl 10

    fun supportsVersion(v: Int) = v in MIN_SUPPORTED_VERSION..VERSION
}

object Platforms {
    const val ANDROID = "android"
    val known = setOf("linux", "windows", "macos", "android", "ios", "other")
}

object AckStatus {
    const val APPLIED = "applied"
    const val DUPLICATE = "duplicate"
    const val STALE = "stale"
    const val EXPIRED = "expired"
    const val IGNORED = "ignored"
    const val REJECTED = "rejected"
    val all = setOf(APPLIED, DUPLICATE, STALE, EXPIRED, IGNORED, REJECTED)
}

object ErrorCodes {
    const val BAD_REQUEST = "bad_request"
    const val UNAUTHORIZED = "unauthorized"
    const val PAIRING_CLOSED = "pairing_closed"
    const val INVALID_CODE = "invalid_code"
    const val RATE_LIMITED = "rate_limited"
    const val TOO_LARGE = "too_large"
    const val IDENTITY_MISMATCH = "identity_mismatch"
    const val UNSUPPORTED_VERSION = "unsupported_version"
}

@Serializable
data class DeviceInfo(
    val id: String,
    val name: String,
    val platform: String,
    @SerialName("app_version") val appVersion: String? = null,
)

@Serializable
data class Payload(
    val text: String,
    @SerialName("mime_type") val mimeType: String,
    val size: Long,
    val sha256: String,
)

@Serializable
data class ClipboardEvent(
    @SerialName("event_id") val eventId: String,
    @SerialName("origin_device_id") val originDeviceId: String,
    @SerialName("content_type") val contentType: String,
    /** Origin wall clock, Unix milliseconds. */
    val timestamp: Long,
    @SerialName("protocol_version") val protocolVersion: Int,
    val payload: Payload,
)

@Serializable
data class Hello(
    val device: DeviceInfo,
    @SerialName("min_protocol_version") val minProtocolVersion: Int,
    @SerialName("max_protocol_version") val maxProtocolVersion: Int,
    val time: Long,
    val capabilities: List<String> = emptyList(),
    @SerialName("max_payload_bytes") val maxPayloadBytes: Long = 0,
    @SerialName("listen_port") val listenPort: Int = 0,
)

@Serializable
data class Ack(
    @SerialName("event_id") val eventId: String,
    val status: String,
    val reason: String? = null,
)

@Serializable
data class ErrorMessage(val code: String = "", val message: String = "")

@Serializable
data class HttpError(val error: ErrorMessage = ErrorMessage())

@Serializable
data class Envelope(
    val type: String,
    @SerialName("protocol_version") val protocolVersion: Int,
    val data: JsonElement,
)

@Serializable
data class PairRequest(
    @SerialName("protocol_version") val protocolVersion: Int,
    val device: DeviceInfo,
    val code: String,
    /** 0: this device does not accept connections (mobile). */
    @SerialName("listen_port") val listenPort: Int = 0,
)

@Serializable
data class PairResponse(
    @SerialName("protocol_version") val protocolVersion: Int,
    val status: String,
    val device: DeviceInfo,
    @SerialName("listen_port") val listenPort: Int = 0,
)

object PairStatus {
    const val APPROVED = "approved"
    const val REJECTED = "rejected"
    const val EXPIRED = "expired"
}

/** JSON configuration shared by the protocol and local storage. */
val ProtocolJson = Json {
    ignoreUnknownKeys = true // forward compatibility (spec §6.1)
    explicitNulls = false // omit null optional fields like Go's omitempty
    encodeDefaults = true
}

class ProtocolException(message: String) : Exception(message)

object Codec {
    inline fun <reified T> encode(type: String, value: T): String {
        val data = ProtocolJson.encodeToJsonElement(value)
        return ProtocolJson.encodeToString(Envelope.serializer(), Envelope(type, Protocol.VERSION, data))
    }

    fun decodeEnvelope(text: String): Envelope {
        val env = try {
            ProtocolJson.decodeFromString(Envelope.serializer(), text)
        } catch (e: Exception) {
            throw ProtocolException("malformed envelope")
        }
        if (env.type.isEmpty()) throw ProtocolException("missing type")
        if (!Protocol.supportsVersion(env.protocolVersion)) {
            throw ProtocolException("unsupported protocol version ${env.protocolVersion}")
        }
        if (env.data !is JsonObject) throw ProtocolException("data must be an object")
        return env
    }

    inline fun <reified T> decodeData(env: Envelope): T = try {
        ProtocolJson.decodeFromJsonElement<T>(env.data)
    } catch (e: Exception) {
        throw ProtocolException("malformed ${env.type}")
    }
}
