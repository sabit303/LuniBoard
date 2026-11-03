package com.universalclipboard.data.network

import java.net.URI
import java.nio.ByteBuffer
import java.nio.charset.CharacterCodingException
import java.nio.charset.CodingErrorAction
import java.security.MessageDigest
import java.security.SecureRandom

/** Validation and helpers mirroring daemon/pkg/protocol/validate.go and codec.go. */
object Validation {
    private val idPattern = Regex("^[A-Za-z0-9._-]{1,64}$")
    private val fingerprintPattern = Regex("^[0-9a-f]{64}$")
    const val MAX_DEVICE_NAME_CHARS = 64

    fun validId(s: String) = idPattern.matches(s)

    fun validFingerprint(s: String) = fingerprintPattern.matches(s)

    fun validateDeviceInfo(d: DeviceInfo) {
        if (!validId(d.id)) throw ProtocolException("invalid device.id")
        val nameChars = d.name.codePointCount(0, d.name.length)
        if (d.name.isEmpty() || nameChars > MAX_DEVICE_NAME_CHARS) throw ProtocolException("invalid device.name")
        if (d.name.codePoints().anyMatch { Character.isISOControl(it) }) throw ProtocolException("invalid device.name")
        if (d.platform !in Platforms.known) throw ProtocolException("invalid device.platform")
        if ((d.appVersion?.length ?: 0) > 32) throw ProtocolException("invalid device.app_version")
    }

    fun validateHello(h: Hello) {
        validateDeviceInfo(h.device)
        if (h.minProtocolVersion <= 0 || h.maxProtocolVersion < h.minProtocolVersion) {
            throw ProtocolException("invalid protocol version range")
        }
        if (h.time <= 0) throw ProtocolException("invalid time")
    }

    fun negotiateVersion(h: Hello): Int? {
        val hi = minOf(h.maxProtocolVersion, Protocol.VERSION)
        val lo = maxOf(h.minProtocolVersion, Protocol.MIN_SUPPORTED_VERSION)
        return if (hi >= lo) hi else null
    }

    class TooLarge : Exception("payload exceeds size limit")

    /**
     * Validates an incoming event. The url/text classification is deliberately
     * not enforced on receive: it is metadata, and java.net.URI is stricter than
     * the Go parser the daemon uses.
     */
    fun validateEvent(ev: ClipboardEvent, maxPayloadBytes: Long) {
        if (!Protocol.supportsVersion(ev.protocolVersion)) throw ProtocolException("unsupported protocol_version")
        if (!validId(ev.eventId)) throw ProtocolException("invalid event_id")
        if (!validId(ev.originDeviceId)) throw ProtocolException("invalid origin_device_id")
        if (ev.contentType != Protocol.CONTENT_TEXT && ev.contentType != Protocol.CONTENT_URL) {
            throw ProtocolException("unsupported content_type")
        }
        if (ev.timestamp <= 0) throw ProtocolException("invalid timestamp")
        val limit = if (maxPayloadBytes <= 0 || maxPayloadBytes > Protocol.HARD_MAX_PAYLOAD_BYTES) {
            Protocol.HARD_MAX_PAYLOAD_BYTES
        } else {
            maxPayloadBytes
        }
        val bytes = ev.payload.text.encodeToByteArray()
        if (bytes.size > limit || ev.payload.size > limit) throw TooLarge()
        if (ev.payload.size != bytes.size.toLong()) throw ProtocolException("payload.size mismatch")
        if (bytes.isEmpty()) throw ProtocolException("empty payload.text")
        if (!isWellFormedUtf16(ev.payload.text)) throw ProtocolException("payload.text is not valid UTF-8")
        if (!ev.payload.mimeType.startsWith("text/") || ev.payload.mimeType.length > 128) {
            throw ProtocolException("invalid payload.mime_type")
        }
        if (ev.payload.sha256 != sha256Hex(bytes)) throw ProtocolException("payload.sha256 mismatch")
    }

    fun validateAck(a: Ack) {
        if (!validId(a.eventId)) throw ProtocolException("invalid ack event_id")
        if (a.status !in AckStatus.all) throw ProtocolException("invalid ack status")
    }

    /** Rejects lone surrogates, which cannot be encoded as UTF-8. */
    fun isWellFormedUtf16(s: String): Boolean = try {
        Charsets.UTF_8.newEncoder()
            .onMalformedInput(CodingErrorAction.REPORT)
            .onUnmappableCharacter(CodingErrorAction.REPORT)
            .encode(java.nio.CharBuffer.wrap(s))
        true
    } catch (e: CharacterCodingException) {
        false
    }

    /** True when [bytes] is valid UTF-8. */
    fun isValidUtf8(bytes: ByteArray): Boolean = try {
        Charsets.UTF_8.newDecoder()
            .onMalformedInput(CodingErrorAction.REPORT)
            .onUnmappableCharacter(CodingErrorAction.REPORT)
            .decode(ByteBuffer.wrap(bytes))
        true
    } catch (e: CharacterCodingException) {
        false
    }

    /**
     * Returns "url" when the trimmed text is a single absolute http(s) URL. This is
     * conservative compared to the daemon's classifier so the daemon never
     * rejects an event we label as url.
     */
    fun classifyText(text: String): String {
        val t = text.trim()
        if (t.isEmpty() || t.length > 8192 || t.any { it.isWhitespace() }) return Protocol.CONTENT_TEXT
        val uri = try {
            URI(t)
        } catch (e: Exception) {
            return Protocol.CONTENT_TEXT
        }
        val scheme = uri.scheme?.lowercase()
        if ((scheme == "http" || scheme == "https") && !uri.host.isNullOrEmpty()) return Protocol.CONTENT_URL
        return Protocol.CONTENT_TEXT
    }

    fun mimeTypeFor(contentType: String) =
        if (contentType == Protocol.CONTENT_URL) "text/uri-list" else "text/plain"

    fun newTextEvent(eventId: String, originId: String, text: String, timestampMillis: Long): ClipboardEvent {
        val ct = classifyText(text)
        val bytes = text.encodeToByteArray()
        return ClipboardEvent(
            eventId = eventId,
            originDeviceId = originId,
            contentType = ct,
            timestamp = timestampMillis,
            protocolVersion = Protocol.VERSION,
            payload = Payload(text = text, mimeType = mimeTypeFor(ct), size = bytes.size.toLong(), sha256 = sha256Hex(bytes)),
        )
    }
}

object Crypto {
    private val random = SecureRandom()

    fun newId(): String = java.util.UUID.randomUUID().toString()

    fun randomBytes(n: Int) = ByteArray(n).also { random.nextBytes(it) }
}

fun sha256Hex(bytes: ByteArray): String =
    MessageDigest.getInstance("SHA-256").digest(bytes).joinToString("") { "%02x".format(it) }

fun sha256Hex(text: String) = sha256Hex(text.encodeToByteArray())

/** Fingerprint of a DER-encoded certificate: the identity pinned at pairing. */
fun certificateFingerprint(der: ByteArray) = sha256Hex(der)

/**
 * The 6-digit verification code both devices display during pairing (spec §5).
 * Symmetric in its arguments.
 */
fun verificationCode(fingerprintA: String, fingerprintB: String): String {
    val (a, b) = if (fingerprintB < fingerprintA) fingerprintB to fingerprintA else fingerprintA to fingerprintB
    val sum = MessageDigest.getInstance("SHA-256").digest("uclip-sas-v1:$a:$b".encodeToByteArray())
    val n = ((sum[0].toLong() and 0xff) shl 24) or ((sum[1].toLong() and 0xff) shl 16) or
        ((sum[2].toLong() and 0xff) shl 8) or (sum[3].toLong() and 0xff)
    val s = "%06d".format(n % 1_000_000)
    return s.substring(0, 3) + " " + s.substring(3)
}

const val PAIRING_CODE_LENGTH = 8

fun normalizePairingCode(s: String): String =
    s.uppercase().filter { it != '-' && it != ' ' }
