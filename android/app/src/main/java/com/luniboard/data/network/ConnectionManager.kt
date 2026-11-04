package com.luniboard.data.network

import com.luniboard.data.local.DeviceStore
import com.luniboard.domain.ConnectedPeer
import com.luniboard.domain.ConnectionStatus
import com.luniboard.domain.PairedDevice
import com.luniboard.domain.PeerLink
import com.luniboard.domain.PeerState
import com.luniboard.domain.SyncEngine
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Job
import kotlinx.coroutines.channels.Channel
import kotlinx.coroutines.coroutineScope
import kotlinx.coroutines.currentCoroutineContext
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.asStateFlow
import kotlinx.coroutines.flow.first
import kotlinx.coroutines.flow.update
import kotlinx.coroutines.isActive
import kotlinx.coroutines.launch
import kotlinx.coroutines.withTimeoutOrNull
import okhttp3.Request
import okhttp3.Response
import okhttp3.WebSocket
import okhttp3.WebSocketListener
import okio.ByteString
import java.util.concurrent.ConcurrentHashMap
import java.util.concurrent.TimeUnit
import kotlin.random.Random

/**
 * Keeps one authenticated WebSocket session (spec §6) to every paired
 * computer while [setActive] is true. Phones never listen; they always dial.
 */
class ConnectionManager(
    private val identity: DeviceIdentity,
    private val devices: DeviceStore,
    private val engine: SyncEngine,
    private val selfInfo: () -> DeviceInfo,
    private val addressesFor: (PairedDevice) -> List<String>,
    private val scope: CoroutineScope,
    private val clock: () -> Long = System::currentTimeMillis,
    private val timings: Timings = Timings(),
    private val log: (String) -> Unit = {},
) : PeerLink {

    data class Timings(
        val handshakeMillis: Long = 10_000,
        val pingSeconds: Long = 30,
        val initialBackoffMillis: Long = 1_000,
        val maxBackoffMillis: Long = 60_000,
        val unauthorizedRetryMillis: Long = 5 * 60_000,
    )

    private class LiveSession(val ws: WebSocket, val hello: Hello, val offsetMillis: Long)

    private sealed interface Frame {
        data object Open : Frame
        data class Text(val text: String) : Frame
        data object Binary : Frame
        data class Closed(val code: Int, val reason: String) : Frame
        data class Failure(val error: Throwable, val httpCode: Int?) : Frame
    }

    private sealed interface Outcome {
        data object Ended : Outcome
        data object Unauthorized : Outcome
        data class Failed(val reason: String) : Outcome
    }

    private val _states = MutableStateFlow<Map<String, PeerState>>(emptyMap())
    val states: StateFlow<Map<String, PeerState>> = _states.asStateFlow()

    private val sessions = ConcurrentHashMap<String, LiveSession>()
    private val kicks = MutableStateFlow(0L)
    private var supervisor: Job? = null

    init {
        engine.peers = this
    }

    val isActive: Boolean @Synchronized get() = supervisor != null

    /** Starts or stops maintaining connections. */
    @Synchronized
    fun setActive(active: Boolean) {
        if (active && supervisor == null) {
            supervisor = scope.launch { supervise() }
        } else if (!active && supervisor != null) {
            supervisor?.cancel()
            supervisor = null
            _states.value = emptyMap()
        }
    }

    /** Retry now (network changed, device discovered, user pressed retry). */
    fun kick() = kicks.update { it + 1 }

    override fun connected(): List<ConnectedPeer> = sessions.map { (id, s) ->
        ConnectedPeer(id, Protocol.CAPABILITY_RECEIVE in s.hello.capabilities, s.hello.maxPayloadBytes)
    }

    override fun send(peerId: String, message: String): Boolean = sessions[peerId]?.ws?.send(message) ?: false

    fun connectedCount() = sessions.size

    private suspend fun supervise(): Unit = coroutineScope {
        val jobs = HashMap<String, Job>()
        try {
            devices.devices.collect { list ->
                val ids = list.map { it.id }.toSet()
                (jobs.keys - ids).forEach { id ->
                    jobs.remove(id)?.cancel()
                    _states.update { it - id }
                }
                (ids - jobs.keys).forEach { id -> jobs[id] = launch { runDevice(id) } }
            }
        } finally {
            jobs.values.forEach { it.cancel() }
        }
    }

    private fun setState(id: String, state: PeerState) = _states.update { it + (id to state) }

    private suspend fun waitForKick(timeoutMillis: Long) {
        val start = kicks.value
        withTimeoutOrNull(timeoutMillis) { kicks.first { it != start } }
    }

    private fun backoff(failures: Int): Long {
        val base = (timings.initialBackoffMillis shl minOf(failures, 10)).coerceAtMost(timings.maxBackoffMillis)
        return base + Random.nextLong(base / 5 + 1) - base / 10
    }

    private suspend fun runDevice(id: String) {
        var failures = 0
        while (currentCoroutineContext().isActive) {
            val device = devices.get(id) ?: return
            val addrs = addressesFor(device)
            if (addrs.isEmpty()) {
                setState(id, PeerState(ConnectionStatus.OFFLINE, detail = "Address unknown — is it on the same network?"))
                waitForKick(timings.maxBackoffMillis)
                continue
            }
            setState(id, PeerState(ConnectionStatus.CONNECTING, detail = addrs.first()))
            when (val outcome = connectOnce(device, addrs)) {
                Outcome.Ended -> {
                    failures = 0
                    setState(id, PeerState(ConnectionStatus.OFFLINE, detail = "Disconnected"))
                    waitForKick(timings.initialBackoffMillis)
                }
                Outcome.Unauthorized -> {
                    setState(
                        id,
                        PeerState(ConnectionStatus.UNAUTHORIZED, detail = "${device.name} no longer trusts this phone. Remove it here and pair again."),
                    )
                    waitForKick(timings.unauthorizedRetryMillis)
                }
                is Outcome.Failed -> {
                    failures++
                    setState(id, PeerState(ConnectionStatus.OFFLINE, detail = outcome.reason))
                    waitForKick(backoff(failures))
                }
            }
        }
    }

    private suspend fun connectOnce(device: PairedDevice, addrs: List<String>): Outcome {
        val client = PeerHttp.client(identity, device.fingerprint) {
            pingInterval(timings.pingSeconds, TimeUnit.SECONDS)
            readTimeout(0, TimeUnit.SECONDS)
        }
        var last: Outcome = Outcome.Failed("Unreachable")
        run {
            for (addr in addrs) {
                val frames = Channel<Frame>(capacity = 64)
                val request = Request.Builder()
                    .url("https://$addr${Protocol.PATH_WEBSOCKET}")
                    .header("Sec-WebSocket-Protocol", Protocol.WEBSOCKET_SUBPROTOCOL)
                    .build()
                val ws = client.newWebSocket(request, object : WebSocketListener() {
                    override fun onOpen(webSocket: WebSocket, response: Response) {
                        frames.trySend(Frame.Open)
                    }

                    override fun onMessage(webSocket: WebSocket, text: String) {
                        // A peer that floods faster than we process is disconnected.
                        if (frames.trySend(Frame.Text(text)).isFailure) webSocket.cancel()
                    }

                    override fun onMessage(webSocket: WebSocket, bytes: ByteString) {
                        frames.trySend(Frame.Binary)
                    }

                    override fun onClosing(webSocket: WebSocket, code: Int, reason: String) {
                        webSocket.close(1000, null)
                        frames.trySend(Frame.Closed(code, reason))
                        frames.close()
                    }

                    override fun onClosed(webSocket: WebSocket, code: Int, reason: String) {
                        frames.trySend(Frame.Closed(code, reason))
                        frames.close()
                    }

                    override fun onFailure(webSocket: WebSocket, t: Throwable, response: Response?) {
                        frames.trySend(Frame.Failure(t, response?.code))
                        frames.close()
                    }
                })
                try {
                    last = serve(device, addr, ws, frames)
                } finally {
                    if (!ws.close(1001, "going away")) ws.cancel()
                }
                if (last == Outcome.Ended || last == Outcome.Unauthorized) return last
            }
        }
        return last
    }

    private suspend fun serve(device: PairedDevice, addr: String, ws: WebSocket, frames: Channel<Frame>): Outcome {
        // Handshake: exchange hello messages and bind the session to the paired identity.
        var handshakeError: Outcome? = null
        val hello: Pair<Hello, Long>? = withTimeoutOrNull(timings.handshakeMillis) {
            for (frame in frames) {
                when (frame) {
                    Frame.Open -> ws.send(Codec.encode(Protocol.TYPE_HELLO, selfHello()))
                    is Frame.Text -> {
                        val received = clock()
                        val result = runCatching {
                            val env = Codec.decodeEnvelope(frame.text)
                            if (env.type == Protocol.TYPE_ERROR) {
                                val e = Codec.decodeData<ErrorMessage>(env)
                                throw ProtocolException("${e.code}: ${e.message}")
                            }
                            if (env.type != Protocol.TYPE_HELLO) throw ProtocolException("expected hello, got ${env.type}")
                            val h = Codec.decodeData<Hello>(env)
                            Validation.validateHello(h)
                            if (h.device.id != device.id) throw ProtocolException("device id does not match the paired certificate")
                            Validation.negotiateVersion(h) ?: throw ProtocolException("no common protocol version")
                            h
                        }
                        result.onSuccess { return@withTimeoutOrNull it to (it.time - received) }
                        handshakeError = Outcome.Failed("Handshake failed: ${result.exceptionOrNull()?.message}")
                        return@withTimeoutOrNull null
                    }
                    Frame.Binary -> {}
                    is Frame.Closed -> {
                        handshakeError = Outcome.Failed("Closed by ${device.name} (${frame.code} ${frame.reason})".trim())
                        return@withTimeoutOrNull null
                    }
                    is Frame.Failure -> {
                        handshakeError = classifyFailure(frame)
                        return@withTimeoutOrNull null
                    }
                }
            }
            null
        }
        if (hello == null) {
            val outcome = handshakeError ?: Outcome.Failed("Timed out connecting to $addr")
            log("connect ${device.name} at $addr: $outcome")
            return outcome
        }

        val (h, offset) = hello
        val live = LiveSession(ws, h, offset)
        sessions[device.id] = live
        devices.recordSeen(device.id, addr, clock())
        if (h.device.name != device.name || h.device.platform != device.platform || h.device.appVersion != device.appVersion) {
            devices.setInfo(device.id, h.device.name, h.device.platform, h.device.appVersion)
        }
        setState(device.id, PeerState(ConnectionStatus.CONNECTED, address = addr, since = clock()))
        log("connected to ${device.name} at $addr (clock offset ${offset} ms)")
        try {
            var invalid = 0
            for (frame in frames) {
                when (frame) {
                    is Frame.Text -> if (!dispatch(device, live, frame.text) && ++invalid >= 20) {
                        ws.close(1002, "too many invalid messages")
                        return Outcome.Ended
                    }
                    Frame.Binary -> invalid++
                    is Frame.Closed -> return Outcome.Ended
                    is Frame.Failure -> {
                        log("session with ${device.name} failed: ${frame.error.message}")
                        return Outcome.Ended
                    }
                    Frame.Open -> {}
                }
            }
            return Outcome.Ended
        } finally {
            sessions.remove(device.id, live)
            devices.recordSeen(device.id, null, clock())
        }
    }

    /** Handles one message; returns false when it was invalid. */
    private suspend fun dispatch(device: PairedDevice, live: LiveSession, text: String): Boolean {
        val env = try {
            Codec.decodeEnvelope(text)
        } catch (e: ProtocolException) {
            live.ws.send(Codec.encode(Protocol.TYPE_ERROR, ErrorMessage(ErrorCodes.BAD_REQUEST, e.message ?: "invalid")))
            return false
        }
        try {
            when (env.type) {
                Protocol.TYPE_EVENT -> {
                    val ev = Codec.decodeData<ClipboardEvent>(env)
                    val ack = engine.handleIncoming(device.id, live.offsetMillis, ev)
                    live.ws.send(Codec.encode(Protocol.TYPE_ACK, ack))
                }
                Protocol.TYPE_ACK -> engine.handleAck(device.id, Codec.decodeData<Ack>(env))
                Protocol.TYPE_ERROR -> {
                    val e = Codec.decodeData<ErrorMessage>(env)
                    log("${device.name} reported error ${e.code.take(64)}: ${e.message.take(200)}")
                }
                Protocol.TYPE_HELLO -> return false
                else -> {} // unknown types are ignored for forward compatibility
            }
        } catch (e: ProtocolException) {
            live.ws.send(Codec.encode(Protocol.TYPE_ERROR, ErrorMessage(ErrorCodes.BAD_REQUEST, e.message ?: "invalid")))
            return false
        }
        return true
    }

    private fun classifyFailure(f: Frame.Failure): Outcome {
        if (f.httpCode == 401) return Outcome.Unauthorized
        f.error.findFingerprintMismatch()?.let {
            return Outcome.Failed("A different device answered at this address (certificate mismatch)")
        }
        val msg = f.error.message ?: f.error.javaClass.simpleName
        return Outcome.Failed(if (f.httpCode != null) "HTTP ${f.httpCode}: $msg" else msg)
    }

    private fun selfHello() = Hello(
        device = selfInfo(),
        minProtocolVersion = Protocol.MIN_SUPPORTED_VERSION,
        maxProtocolVersion = Protocol.VERSION,
        time = clock(),
        capabilities = listOf(Protocol.CAPABILITY_SEND, Protocol.CAPABILITY_RECEIVE),
        maxPayloadBytes = engine.maxPayloadBytes,
        listenPort = 0,
    )
}
