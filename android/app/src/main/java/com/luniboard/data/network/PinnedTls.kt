package com.luniboard.data.network

import java.net.Socket
import java.security.KeyStore
import java.security.MessageDigest
import java.security.Principal
import java.security.PrivateKey
import java.security.cert.CertificateException
import java.security.cert.X509Certificate
import javax.net.ssl.SSLContext
import javax.net.ssl.SSLEngine
import javax.net.ssl.SSLSocketFactory
import javax.net.ssl.X509ExtendedKeyManager
import javax.net.ssl.X509TrustManager

/**
 * This device's long-lived TLS identity. On Android the private key lives in
 * the Android Keystore and never leaves it; tests use a PKCS#12 file.
 */
interface DeviceIdentity {
    val deviceId: String
    val certificate: X509Certificate
    val privateKey: PrivateKey
    val fingerprint: String get() = certificateFingerprint(certificate.encoded)
}

/** Identity loaded from a key store entry (used by JVM tests and tools). */
class KeyStoreIdentity(
    override val deviceId: String,
    keyStore: KeyStore,
    alias: String,
    password: CharArray?,
) : DeviceIdentity {
    override val certificate = keyStore.getCertificate(alias) as X509Certificate
    override val privateKey = keyStore.getKey(alias, password) as PrivateKey
}

/** Thrown when the remote certificate is not the one pinned at pairing. */
class FingerprintMismatchException(val expected: String, val actual: String) :
    CertificateException("peer certificate fingerprint ${actual.take(16)}… does not match pinned ${expected.take(16)}…")

/**
 * Accepts the server only if its leaf certificate has the pinned SHA-256
 * fingerprint. Chain validation is replaced by pinning: devices use
 * self-signed certificates whose fingerprints were exchanged during pairing.
 */
class PinnedTrustManager(private val expectedFingerprint: String) : X509TrustManager {
    override fun checkServerTrusted(chain: Array<out X509Certificate>?, authType: String?) {
        val leaf = chain?.firstOrNull() ?: throw CertificateException("server presented no certificate")
        val actual = certificateFingerprint(leaf.encoded)
        if (!MessageDigest.isEqual(actual.toByteArray(), expectedFingerprint.toByteArray())) {
            throw FingerprintMismatchException(expectedFingerprint, actual)
        }
    }

    override fun checkClientTrusted(chain: Array<out X509Certificate>?, authType: String?) =
        throw CertificateException("client mode only")

    override fun getAcceptedIssuers(): Array<X509Certificate> = emptyArray()
}

/** Always presents this device's certificate (servers send no CA list). */
class IdentityKeyManager(private val identity: DeviceIdentity) : X509ExtendedKeyManager() {
    private val alias = "luniboard"
    override fun chooseClientAlias(keyType: Array<out String>?, issuers: Array<out Principal>?, socket: Socket?) = alias
    override fun chooseEngineClientAlias(keyType: Array<out String>?, issuers: Array<out Principal>?, engine: SSLEngine?) = alias
    override fun getClientAliases(keyType: String?, issuers: Array<out Principal>?) = arrayOf(alias)
    override fun getCertificateChain(alias: String?) = arrayOf(identity.certificate)
    override fun getPrivateKey(alias: String?) = identity.privateKey
    override fun getServerAliases(keyType: String?, issuers: Array<out Principal>?) = null
    override fun chooseServerAlias(keyType: String?, issuers: Array<out Principal>?, socket: Socket?) = null
}

/** TLS material for one connection target. */
class PinnedTls(identity: DeviceIdentity, expectedFingerprint: String) {
    val trustManager = PinnedTrustManager(expectedFingerprint)
    val socketFactory: SSLSocketFactory = SSLContext.getInstance("TLS").apply {
        init(arrayOf(IdentityKeyManager(identity)), arrayOf(trustManager), null)
    }.socketFactory
}

/** Walks the cause chain looking for a pin mismatch. */
fun Throwable.findFingerprintMismatch(): FingerprintMismatchException? {
    var t: Throwable? = this
    var depth = 0
    while (t != null && depth++ < 10) {
        if (t is FingerprintMismatchException) return t
        t.suppressed.forEach { s -> s.findFingerprintMismatch()?.let { return it } }
        t = t.cause
    }
    return null
}
