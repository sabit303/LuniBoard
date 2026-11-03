// Package security implements device identity, TLS configuration and abuse limits.
package security

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"time"

	"github.com/universal-clipboard/universal-clipboard/daemon/internal/storage"
	"github.com/universal-clipboard/universal-clipboard/daemon/pkg/protocol"
)

// Identity is this device's long-lived cryptographic identity. The SHA-256
// fingerprint of its self-signed certificate is what peers pin when pairing.
type Identity struct {
	DeviceID    string
	Certificate tls.Certificate
	Fingerprint string
}

type identityMeta struct {
	DeviceID  string    `json:"device_id"`
	CreatedAt time.Time `json:"created_at"`
}

const (
	certFile = "cert.pem"
	keyFile  = "key.pem"
	metaFile = "identity.json"
)

// LoadOrCreateIdentity loads the identity stored in dir, generating a new one
// on first run. Private key material is written with mode 0600.
func LoadOrCreateIdentity(dir string) (*Identity, error) {
	var meta identityMeta
	found, err := storage.ReadJSON(filepath.Join(dir, metaFile), &meta)
	if err != nil {
		return nil, err
	}
	if found {
		return loadIdentity(dir, meta)
	}
	return createIdentity(dir)
}

func loadIdentity(dir string, meta identityMeta) (*Identity, error) {
	if !protocol.ValidID(meta.DeviceID) {
		return nil, errors.New("identity: stored device id is invalid")
	}
	keyPath := filepath.Join(dir, keyFile)
	if err := checkPrivatePerms(keyPath); err != nil {
		return nil, err
	}
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, certFile), keyPath)
	if err != nil {
		return nil, fmt.Errorf("identity: load key pair: %w", err)
	}
	return &Identity{
		DeviceID:    meta.DeviceID,
		Certificate: cert,
		Fingerprint: protocol.CertificateFingerprint(cert.Certificate[0]),
	}, nil
}

func createIdentity(dir string) (*Identity, error) {
	deviceID := protocol.NewID()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("identity: generate key: %w", err)
	}
	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 127))
	if err != nil {
		return nil, fmt.Errorf("identity: serial: %w", err)
	}
	now := time.Now()
	tmpl := &x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: deviceID, Organization: []string{"Universal Clipboard"}},
		NotBefore:             now.Add(-time.Hour),
		NotAfter:              now.AddDate(30, 0, 0),
		KeyUsage:              x509.KeyUsageDigitalSignature,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth, x509.ExtKeyUsageClientAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"universal-clipboard"},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		return nil, fmt.Errorf("identity: create certificate: %w", err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return nil, fmt.Errorf("identity: marshal key: %w", err)
	}
	if err := storage.WriteFileAtomic(filepath.Join(dir, keyFile),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600); err != nil {
		return nil, err
	}
	if err := storage.WriteFileAtomic(filepath.Join(dir, certFile),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644); err != nil {
		return nil, err
	}
	// The metadata file is written last: its presence marks a complete identity.
	if err := storage.WriteJSON(filepath.Join(dir, metaFile), identityMeta{DeviceID: deviceID, CreatedAt: now.UTC()}); err != nil {
		return nil, err
	}
	return &Identity{
		DeviceID:    deviceID,
		Certificate: tls.Certificate{Certificate: [][]byte{der}, PrivateKey: key},
		Fingerprint: protocol.CertificateFingerprint(der),
	}, nil
}

func checkPrivatePerms(path string) error {
	st, err := os.Stat(path)
	if err != nil {
		return fmt.Errorf("identity: %w", err)
	}
	if isWindows() {
		return nil
	}
	if st.Mode().Perm()&0o077 != 0 {
		return fmt.Errorf("identity: %s is accessible by other users (mode %o); run chmod 600", path, st.Mode().Perm())
	}
	return nil
}
