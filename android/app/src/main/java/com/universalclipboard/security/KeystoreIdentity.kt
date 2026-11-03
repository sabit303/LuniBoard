package com.universalclipboard.security

import android.content.Context
import android.security.keystore.KeyGenParameterSpec
import android.security.keystore.KeyProperties
import com.universalclipboard.data.network.Crypto
import com.universalclipboard.data.network.DeviceIdentity
import java.math.BigInteger
import java.security.KeyPairGenerator
import java.security.KeyStore
import java.security.PrivateKey
import java.security.cert.X509Certificate
import java.security.spec.ECGenParameterSpec
import java.util.Date
import javax.security.auth.x500.X500Principal

/**
 * The phone's TLS identity. The ECDSA P-256 private key is generated inside
 * the Android Keystore (hardware-backed where available) and is not
 * exportable; the Keystore also produces the self-signed certificate whose
 * SHA-256 fingerprint computers pin when pairing.
 */
class KeystoreIdentity private constructor(
    override val deviceId: String,
    override val certificate: X509Certificate,
    override val privateKey: PrivateKey,
) : DeviceIdentity {

    companion object {
        private const val ALIAS = "uclip-device-identity"
        private const val PREFS = "identity"
        private const val KEY_DEVICE_ID = "device_id"

        @Synchronized
        fun loadOrCreate(context: Context): KeystoreIdentity {
            val prefs = context.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
            val ks = KeyStore.getInstance("AndroidKeyStore").apply { load(null) }
            var id = prefs.getString(KEY_DEVICE_ID, null)
            if (id == null || !ks.containsAlias(ALIAS)) {
                id = Crypto.newId()
                if (ks.containsAlias(ALIAS)) ks.deleteEntry(ALIAS)
                generate(id)
                prefs.edit().putString(KEY_DEVICE_ID, id).commit()
            }
            val entry = ks.getEntry(ALIAS, null) as KeyStore.PrivateKeyEntry
            return KeystoreIdentity(id, entry.certificate as X509Certificate, entry.privateKey)
        }

        private fun generate(deviceId: String) {
            val now = System.currentTimeMillis()
            val spec = KeyGenParameterSpec.Builder(ALIAS, KeyProperties.PURPOSE_SIGN or KeyProperties.PURPOSE_VERIFY)
                .setAlgorithmParameterSpec(ECGenParameterSpec("secp256r1"))
                // TLS 1.2 signs a precomputed digest (NONE); TLS 1.3 uses SHA-256/384.
                .setDigests(KeyProperties.DIGEST_NONE, KeyProperties.DIGEST_SHA256, KeyProperties.DIGEST_SHA384, KeyProperties.DIGEST_SHA512)
                .setCertificateSubject(X500Principal("CN=$deviceId, O=Universal Clipboard"))
                .setCertificateSerialNumber(BigInteger(1, Crypto.randomBytes(16)))
                .setCertificateNotBefore(Date(now - 3_600_000))
                .setCertificateNotAfter(Date(now + 30L * 365 * 24 * 3_600_000))
                .build()
            KeyPairGenerator.getInstance(KeyProperties.KEY_ALGORITHM_EC, "AndroidKeyStore").apply {
                initialize(spec)
                generateKeyPair()
            }
        }
    }
}
