package com.universalclipboard.data.network

import java.net.URI
import java.net.URLDecoder

/** A decoded uclip://pair invitation (spec §5). */
data class PairingInvitation(
    val device: DeviceInfo,
    val fingerprint: String,
    /** host:port pairs, IPv6 hosts in brackets. */
    val addresses: List<String>,
    val code: String,
)

object PairingUri {
    const val SCHEME = "uclip"

    fun parse(raw: String): PairingInvitation {
        val uri = try {
            URI(raw.trim())
        } catch (e: Exception) {
            throw ProtocolException("not a valid link")
        }
        if (uri.scheme != SCHEME || uri.host != "pair" && uri.authority != "pair") {
            throw ProtocolException("not a uclip://pair link")
        }
        val query = parseQuery(uri.rawQuery ?: "")
        fun one(key: String) = query[key]?.firstOrNull() ?: ""
        val version = one("v").toIntOrNull() ?: throw ProtocolException("missing version")
        if (!Protocol.supportsVersion(version)) {
            throw ProtocolException("this link needs a newer version of the app (protocol $version)")
        }
        val inv = PairingInvitation(
            device = DeviceInfo(id = one("id"), name = one("name"), platform = one("os")),
            fingerprint = one("fp").lowercase(),
            addresses = query["addr"].orEmpty(),
            code = one("code"),
        )
        Validation.validateDeviceInfo(inv.device)
        if (!Validation.validFingerprint(inv.fingerprint)) throw ProtocolException("invalid fingerprint")
        if (normalizePairingCode(inv.code).length != PAIRING_CODE_LENGTH) throw ProtocolException("invalid pairing code")
        if (inv.addresses.isEmpty() || inv.addresses.size > 16) throw ProtocolException("missing addresses")
        inv.addresses.forEach { if (!isHostPort(it)) throw ProtocolException("invalid address $it") }
        return inv
    }

    private fun parseQuery(raw: String): Map<String, List<String>> {
        val out = linkedMapOf<String, MutableList<String>>()
        for (part in raw.split('&')) {
            if (part.isEmpty()) continue
            val i = part.indexOf('=')
            val k = decode(if (i < 0) part else part.substring(0, i))
            val v = if (i < 0) "" else decode(part.substring(i + 1))
            out.getOrPut(k) { mutableListOf() }.add(v)
        }
        return out
    }

    private fun decode(s: String) = URLDecoder.decode(s, Charsets.UTF_8)

    fun isHostPort(s: String): Boolean {
        val (host, port) = splitHostPort(s) ?: return false
        return host.isNotEmpty() && port in 1..65535
    }

    /** Splits "host:port" / "[v6]:port"; returns null when malformed. */
    fun splitHostPort(s: String): Pair<String, Int>? {
        if (s.startsWith("[")) {
            val end = s.indexOf("]:")
            if (end < 0) return null
            return s.substring(1, end) to (s.substring(end + 2).toIntOrNull() ?: return null)
        }
        val i = s.lastIndexOf(':')
        if (i <= 0 || s.indexOf(':') != i) return null
        return s.substring(0, i) to (s.substring(i + 1).toIntOrNull() ?: return null)
    }

    fun joinHostPort(host: String, port: Int) = if (host.contains(':')) "[$host]:$port" else "$host:$port"
}
