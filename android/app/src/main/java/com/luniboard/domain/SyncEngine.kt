package com.luniboard.domain

import com.luniboard.data.network.Ack
import com.luniboard.data.network.AckStatus
import com.luniboard.data.network.ClipboardEvent
import com.luniboard.data.network.Codec
import com.luniboard.data.network.Crypto
import com.luniboard.data.network.ErrorCodes
import com.luniboard.data.network.Protocol
import com.luniboard.data.network.Validation
import kotlinx.coroutines.CompletableDeferred
import kotlinx.coroutines.flow.MutableSharedFlow
import kotlinx.coroutines.flow.SharedFlow
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withTimeoutOrNull

/** Writes the phone's clipboard. */
fun interface ClipboardWriter {
    suspend fun writeText(text: String, fromDeviceName: String)
}

data class ConnectedPeer(val id: String, val canReceive: Boolean, val maxPayloadBytes: Long)

/** Live sessions, provided by the connection manager. */
interface PeerLink {
    fun connected(): List<ConnectedPeer>
    fun send(peerId: String, message: String): Boolean
}

interface DeviceLookup {
    fun get(id: String): PairedDevice?
}

interface HistoryRecorder {
    fun add(entry: HistoryEntry)
    fun updateDelivery(eventId: String, deviceId: String, status: String)
}

/**
 * Synchronization policy for the phone, following protocol §7 and the
 * daemon's engine: deduplication, origin checks, expiry, stale-overwrite
 * protection, per-device permissions and acknowledgements.
 *
 * Android cannot observe clipboard changes in the background, so outgoing
 * events only come from explicit user actions ([send]).
 */
class SyncEngine(
    private val selfId: () -> String,
    private val devices: DeviceLookup,
    private val history: HistoryRecorder,
    private val settings: () -> AppSettings,
    private val clipboard: ClipboardWriter,
    private val clock: () -> Long = System::currentTimeMillis,
    private val config: Config = Config(),
) {
    data class Config(
        val maxPayloadBytes: Long = 1L shl 20,
        val maxEventAgeMillis: Long = 2 * 60_000,
        val orderingToleranceMillis: Long = 2_000,
        val maxFutureSkewMillis: Long = 5 * 60_000,
        val seenTtlMillis: Long = 10 * 60_000,
        val seenMax: Int = 2_000,
    )

    lateinit var peers: PeerLink

    private val mutex = Mutex()
    private val seen = LinkedHashMap<String, Long>()
    private var latestMillis = 0L
    private var lastWrittenHash: String? = null
    private val waiters = HashMap<String, AckWaiter>()

    private val _received = MutableSharedFlow<ReceivedClip>(extraBufferCapacity = 8)
    val received: SharedFlow<ReceivedClip> = _received

    private class AckWaiter(val pending: MutableSet<String>, val results: MutableMap<String, Ack> = HashMap()) {
        val done = CompletableDeferred<Unit>()
    }

    val maxPayloadBytes get() = config.maxPayloadBytes

    class SendException(message: String) : Exception(message)

    /**
     * Sends text to [targets] (all connected devices with send permission when
     * null or empty) and waits up to [ackTimeoutMillis] for acknowledgements.
     */
    suspend fun send(text: String, targets: Collection<String>?, source: String, ackTimeoutMillis: Long = 3_000): SendResult {
        if (text.isEmpty()) throw SendException("Nothing to send")
        if (!Validation.isWellFormedUtf16(text)) throw SendException("The text contains invalid characters")
        val size = text.encodeToByteArray().size.toLong()
        if (size > config.maxPayloadBytes) throw SendException("Text is too large (${size / 1024} KiB, limit ${config.maxPayloadBytes / 1024} KiB)")
        val now = clock()
        val ev = Validation.newTextEvent(Crypto.newId(), selfId(), text, now)
        val msg = Codec.encode(Protocol.TYPE_EVENT, ev)

        val connected = peers.connected().associateBy { it.id }
        val wanted = if (targets.isNullOrEmpty()) {
            connected.keys.filter { devices.get(it)?.permissions?.send == true }
        } else {
            targets.toList()
        }
        val results = LinkedHashMap<String, SendOutcome>()
        val waiter = AckWaiter(HashSet())
        mutex.withLock { remember(ev.eventId, now) }
        synchronized(waiters) { waiters[ev.eventId] = waiter }
        record(ev, Direction.OUTGOING, source, null)
        try {
            for (id in wanted) {
                val device = devices.get(id)
                val peer = connected[id]
                val outcome = when {
                    device == null -> SendOutcome("unknown_device")
                    !device.permissions.send -> SendOutcome("not_permitted", "sending to this device is disabled")
                    peer == null -> SendOutcome("offline")
                    !peer.canReceive -> SendOutcome("not_supported")
                    peer.maxPayloadBytes in 1 until size -> SendOutcome("too_large", "exceeds the device's limit")
                    else -> null
                }
                if (outcome != null) {
                    results[id] = outcome
                    continue
                }
                // Registered before sending: the ack may arrive before send() returns.
                synchronized(waiters) { waiter.pending += id }
                history.updateDelivery(ev.eventId, id, "sent")
                if (peers.send(id, msg)) {
                    results[id] = SendOutcome("sent")
                } else {
                    synchronized(waiters) { waiter.pending -= id }
                    history.updateDelivery(ev.eventId, id, "failed")
                    results[id] = SendOutcome("failed", "connection lost")
                }
            }
            synchronized(waiters) { if (waiter.pending.isEmpty()) waiter.done.complete(Unit) }
            withTimeoutOrNull(ackTimeoutMillis) { waiter.done.await() }
            synchronized(waiters) {
                waiter.results.forEach { (id, ack) -> results[id] = SendOutcome(ack.status, ack.reason) }
            }
        } finally {
            synchronized(waiters) { waiters.remove(ev.eventId) }
        }
        return SendResult(ev.eventId, results)
    }

    fun handleAck(fromPeer: String, ack: Ack) {
        runCatching { Validation.validateAck(ack) }.getOrElse { return }
        history.updateDelivery(ack.eventId, fromPeer, ack.status)
        synchronized(waiters) {
            val w = waiters[ack.eventId] ?: return
            if (w.pending.remove(fromPeer)) {
                w.results[fromPeer] = ack
                if (w.pending.isEmpty()) w.done.complete(Unit)
            }
        }
    }

    /**
     * Processes an event from a directly connected computer. [clockOffsetMillis]
     * is the estimated (peer - local) clock difference for the session.
     */
    suspend fun handleIncoming(fromPeer: String, clockOffsetMillis: Long, ev: ClipboardEvent): Ack {
        fun ack(status: String, reason: String? = null) = Ack(ev.eventId, status, reason)
        if (!Validation.validId(ev.eventId)) return Ack("invalid", AckStatus.REJECTED, "invalid event_id")
        try {
            Validation.validateEvent(ev, config.maxPayloadBytes)
        } catch (e: Validation.TooLarge) {
            return ack(AckStatus.REJECTED, ErrorCodes.TOO_LARGE)
        } catch (e: Exception) {
            return ack(AckStatus.REJECTED, (e.message ?: "invalid").take(256))
        }
        if (ev.originDeviceId == selfId()) return ack(AckStatus.IGNORED, "own_origin")
        val now = clock()
        val duplicate = mutex.withLock {
            if (isSeen(ev.eventId, now)) true else {
                remember(ev.eventId, now)
                false
            }
        }
        if (duplicate) return ack(AckStatus.DUPLICATE)

        val device = devices.get(fromPeer)
        if (device == null || !device.permissions.receive) return ack(AckStatus.REJECTED, "not_permitted")
        val s = settings()
        if (!s.syncEnabled) return ack(AckStatus.IGNORED, "paused")
        if (!s.receiveEnabled) return ack(AckStatus.IGNORED, "receive_disabled")

        val adjusted = if (ev.originDeviceId == fromPeer) ev.timestamp - clockOffsetMillis else ev.timestamp
        if (adjusted > now + config.maxFutureSkewMillis) return ack(AckStatus.REJECTED, "timestamp_in_future")
        if (now - adjusted > config.maxEventAgeMillis) return ack(AckStatus.EXPIRED)

        val write = mutex.withLock {
            if (adjusted < latestMillis - config.orderingToleranceMillis) return ack(AckStatus.STALE, "newer clipboard content present")
            latestMillis = maxOf(latestMillis, adjusted)
            val changed = lastWrittenHash != ev.payload.sha256
            lastWrittenHash = ev.payload.sha256
            changed
        }
        if (write) {
            try {
                clipboard.writeText(ev.payload.text, device.name)
            } catch (e: Exception) {
                mutex.withLock { lastWrittenHash = null }
                return ack(AckStatus.REJECTED, "clipboard_unavailable")
            }
        }
        record(ev, Direction.INCOMING, Source.PEER, fromPeer)
        _received.tryEmit(ReceivedClip(fromPeer, device.name, ev.payload.text))
        return ack(AckStatus.APPLIED)
    }

    private fun record(ev: ClipboardEvent, direction: String, source: String, from: String?) {
        history.add(
            HistoryEntry(
                eventId = ev.eventId,
                originDeviceId = ev.originDeviceId,
                fromDeviceId = from,
                direction = direction,
                source = source,
                contentType = ev.contentType,
                text = ev.payload.text,
                timestamp = ev.timestamp,
                recordedAt = clock(),
            ),
        )
    }

    private fun isSeen(id: String, now: Long): Boolean {
        val t = seen[id] ?: return false
        return now - t <= config.seenTtlMillis
    }

    private fun remember(id: String, now: Long) {
        seen[id] = now
        val it = seen.entries.iterator()
        while (it.hasNext()) {
            val e = it.next()
            if (seen.size > config.seenMax || now - e.value > config.seenTtlMillis) it.remove() else break
        }
    }
}
