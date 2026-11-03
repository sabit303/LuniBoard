package security

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/rsa"
	"crypto/subtle"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"runtime"

	"github.com/universal-clipboard/universal-clipboard/daemon/pkg/protocol"
)

func isWindows() bool { return runtime.GOOS == "windows" }

// maxPeerCertBytes bounds the size of certificates we are willing to parse.
const maxPeerCertBytes = 8 << 10

// ServerTLSConfig returns the TLS configuration for the LAN listener. Every
// client must present a certificate; whether that certificate is trusted is
// decided per request by the HTTP layer (pairing accepts unknown certificates,
// everything else requires a paired fingerprint).
func ServerTLSConfig(id *Identity) *tls.Config {
	return &tls.Config{
		Certificates:          []tls.Certificate{id.Certificate},
		ClientAuth:            tls.RequireAnyClientCert,
		MinVersion:            tls.VersionTLS12,
		CipherSuites:          tls12Suites,
		VerifyPeerCertificate: verifyPeerShape,
		NextProtos:            []string{"http/1.1"},
	}
}

// ClientTLSConfig returns a TLS configuration that presents our identity and
// accepts the server only if its certificate fingerprint equals expectedFP.
func ClientTLSConfig(id *Identity, expectedFP string) *tls.Config {
	return &tls.Config{
		Certificates: []tls.Certificate{id.Certificate},
		MinVersion:   tls.VersionTLS12,
		CipherSuites: tls12Suites,
		// Chain verification is replaced by fingerprint pinning in VerifyConnection,
		// which runs even when InsecureSkipVerify is set.
		InsecureSkipVerify:    true, //nolint:gosec // pinned below
		VerifyPeerCertificate: verifyPeerShape,
		VerifyConnection: func(cs tls.ConnectionState) error {
			got := PeerFingerprint(cs)
			if got == "" || subtle.ConstantTimeCompare([]byte(got), []byte(expectedFP)) != 1 {
				return &FingerprintMismatchError{Expected: expectedFP, Got: got}
			}
			return nil
		},
		NextProtos: []string{"http/1.1"},
	}
}

// FingerprintMismatchError means the remote certificate is not the pinned one.
type FingerprintMismatchError struct{ Expected, Got string }

func (e *FingerprintMismatchError) Error() string {
	return fmt.Sprintf("peer certificate fingerprint %s does not match pinned %s", short(e.Got), short(e.Expected))
}

func short(fp string) string {
	if len(fp) > 16 {
		return fp[:16] + "…"
	}
	return fp
}

// PeerFingerprint returns the fingerprint of the leaf certificate the peer presented.
func PeerFingerprint(cs tls.ConnectionState) string {
	if len(cs.PeerCertificates) == 0 {
		return ""
	}
	return protocol.CertificateFingerprint(cs.PeerCertificates[0].Raw)
}

// verifyPeerShape performs cheap sanity checks on a self-signed peer certificate.
func verifyPeerShape(rawCerts [][]byte, _ [][]*x509.Certificate) error {
	if len(rawCerts) == 0 {
		return errors.New("peer presented no certificate")
	}
	if len(rawCerts[0]) > maxPeerCertBytes {
		return errors.New("peer certificate too large")
	}
	cert, err := x509.ParseCertificate(rawCerts[0])
	if err != nil {
		return fmt.Errorf("parse peer certificate: %w", err)
	}
	switch k := cert.PublicKey.(type) {
	case *ecdsa.PublicKey, ed25519.PublicKey:
	case *rsa.PublicKey:
		if k.N.BitLen() < 2048 {
			return errors.New("peer RSA key too small")
		}
	default:
		return errors.New("unsupported peer key type")
	}
	return nil
}

// tls12Suites restricts TLS 1.2 to forward-secret AEAD suites. TLS 1.3 suites are not configurable.
var tls12Suites = []uint16{
	tls.TLS_ECDHE_ECDSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_ECDSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_ECDSA_WITH_CHACHA20_POLY1305_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_128_GCM_SHA256,
	tls.TLS_ECDHE_RSA_WITH_AES_256_GCM_SHA384,
	tls.TLS_ECDHE_RSA_WITH_CHACHA20_POLY1305_SHA256,
}
