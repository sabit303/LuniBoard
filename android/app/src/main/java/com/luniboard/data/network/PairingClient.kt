package com.luniboard.data.network

import kotlinx.coroutines.suspendCancellableCoroutine
import okhttp3.Call
import okhttp3.Callback
import okhttp3.ConnectionSpec
import okhttp3.MediaType.Companion.toMediaType
import okhttp3.OkHttpClient
import okhttp3.Protocol as HttpProtocol
import okhttp3.Request
import okhttp3.RequestBody.Companion.toRequestBody
import okhttp3.Response
import java.io.IOException
import java.net.Proxy
import java.util.concurrent.TimeUnit
import kotlin.coroutines.resume
import kotlin.coroutines.resumeWithException

/** Builds OkHttp clients bound to a pinned peer. Shared by pairing and sessions. */
object PeerHttp {
    private val base: OkHttpClient by lazy {
        OkHttpClient.Builder()
            // LAN traffic must never go through a proxy.
            .proxy(Proxy.NO_PROXY)
            .protocols(listOf(HttpProtocol.HTTP_1_1))
            .connectionSpecs(listOf(ConnectionSpec.RESTRICTED_TLS, ConnectionSpec.MODERN_TLS))
            .followRedirects(false)
            .followSslRedirects(false)
            .retryOnConnectionFailure(false)
            .connectTimeout(5, TimeUnit.SECONDS)
            .build()
    }

    fun client(identity: DeviceIdentity, fingerprint: String, configure: OkHttpClient.Builder.() -> Unit = {}): OkHttpClient {
        val tls = PinnedTls(identity, fingerprint)
        return base.newBuilder()
            .sslSocketFactory(tls.socketFactory, tls.trustManager)
            // Identity is established by the pinned certificate, not by host names (peers are IPs).
            .hostnameVerifier { _, _ -> true }
            .apply(configure)
            .build()
    }
}

suspend fun Call.await(): Response = suspendCancellableCoroutine { cont ->
    cont.invokeOnCancellation { cancel() }
    enqueue(object : Callback {
        override fun onFailure(call: Call, e: IOException) {
            if (cont.isActive) cont.resumeWithException(e)
        }

        override fun onResponse(call: Call, response: Response) {
            cont.resume(response)
        }
    })
}

/** The remote device refused the pairing request (HTTP error). */
class PairingRefusedException(val httpStatus: Int, val code: String, message: String) : Exception(message)

class PairingResult(val response: PairResponse, val address: String)

/**
 * Sends pairing requests (spec §5). The server certificate is pinned to the
 * fingerprint from the invitation, so the code can only reach the device that
 * generated it. The call blocks until the user decides on the other device.
 */
class PairingClient(private val identity: DeviceIdentity) {

    suspend fun pair(request: PairRequest, invitation: PairingInvitation): PairingResult {
        val client = PeerHttp.client(identity, invitation.fingerprint) {
            // The server holds the request until its user approves; its window bounds the wait.
            readTimeout(0, TimeUnit.SECONDS)
            callTimeout(65, TimeUnit.MINUTES)
        }
        val body = ProtocolJson.encodeToString(PairRequest.serializer(), request)
            .toRequestBody("application/json".toMediaType())
        val errors = mutableListOf<String>()
        run {
            for (addr in invitation.addresses) {
                val req = Request.Builder().url("https://$addr${Protocol.PATH_PAIR}").post(body).build()
                val response = try {
                    client.newCall(req).await()
                } catch (e: IOException) {
                    e.findFingerprintMismatch()?.let { throw it }
                    errors += "$addr: ${e.message ?: e.javaClass.simpleName}"
                    continue
                }
                return response.use { handle(it, addr, invitation) }
            }
        }
        throw IOException("could not reach the device (${errors.joinToString("; ")})")
    }

    private fun handle(response: Response, addr: String, invitation: PairingInvitation): PairingResult {
        val source = response.body.source()
        source.request(64 * 1024)
        val raw = source.buffer.readUtf8(minOf(source.buffer.size, 64L * 1024))
        if (!response.isSuccessful) {
            val err = runCatching { ProtocolJson.decodeFromString(HttpError.serializer(), raw).error }.getOrNull()
            throw PairingRefusedException(response.code, err?.code ?: "", err?.message?.ifEmpty { null } ?: "HTTP ${response.code}")
        }
        val pr = try {
            ProtocolJson.decodeFromString(PairResponse.serializer(), raw)
        } catch (e: Exception) {
            throw ProtocolException("malformed pairing response")
        }
        if (pr.status == PairStatus.APPROVED) {
            Validation.validateDeviceInfo(pr.device)
            if (pr.device.id != invitation.device.id) throw ProtocolException("device identity does not match the invitation")
            if (pr.listenPort !in 0..65535) throw ProtocolException("invalid listen_port")
        }
        return PairingResult(pr, addr)
    }
}
