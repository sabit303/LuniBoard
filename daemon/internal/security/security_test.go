package security

import (
	"crypto/tls"
	"errors"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIdentityPersistence(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreateIdentity(dir)
	if err != nil {
		t.Fatal(err)
	}
	if a.DeviceID != b.DeviceID || a.Fingerprint != b.Fingerprint {
		t.Fatal("identity changed between loads")
	}
	st, err := os.Stat(filepath.Join(dir, keyFile))
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o600 {
		t.Fatalf("key mode = %o", st.Mode().Perm())
	}
}

func TestIdentityRejectsLoosePermissions(t *testing.T) {
	dir := t.TempDir()
	if _, err := LoadOrCreateIdentity(dir); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(dir, keyFile), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateIdentity(dir); err == nil {
		t.Fatal("expected permission error")
	}
}

func handshake(t *testing.T, server, client *tls.Config) (serverState tls.ConnectionState, clientErr error) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	done := make(chan tls.ConnectionState, 1)
	go func() {
		c1, err := ln.Accept()
		if err != nil {
			done <- tls.ConnectionState{}
			return
		}
		s := tls.Server(c1, server)
		_ = s.SetDeadline(time.Now().Add(5 * time.Second))
		_ = s.Handshake()
		done <- s.ConnectionState()
		s.Close()
	}()
	c2, err := net.Dial("tcp", ln.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	c := tls.Client(c2, client)
	_ = c.SetDeadline(time.Now().Add(5 * time.Second))
	clientErr = c.Handshake()
	if clientErr == nil {
		// Complete the TLS 1.3 exchange so the server observes the client certificate.
		_, _ = c.Write([]byte{0})
	}
	c.Close()
	return <-done, clientErr
}

func TestPinnedMutualTLS(t *testing.T) {
	srv, err := LoadOrCreateIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cli, err := LoadOrCreateIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	state, err := handshake(t, ServerTLSConfig(srv), ClientTLSConfig(cli, srv.Fingerprint))
	if err != nil {
		t.Fatalf("pinned handshake failed: %v", err)
	}
	if got := PeerFingerprint(state); got != cli.Fingerprint {
		t.Fatalf("server saw client fp %q, want %q", got, cli.Fingerprint)
	}

	_, err = handshake(t, ServerTLSConfig(srv), ClientTLSConfig(cli, cli.Fingerprint))
	var mismatch *FingerprintMismatchError
	if !errors.As(err, &mismatch) {
		t.Fatalf("expected fingerprint mismatch, got %v", err)
	}
}

func TestServerRequiresClientCertificate(t *testing.T) {
	srv, err := LoadOrCreateIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	cfg := &tls.Config{InsecureSkipVerify: true} //nolint:gosec // test
	state, _ := handshake(t, ServerTLSConfig(srv), cfg)
	if PeerFingerprint(state) != "" {
		t.Fatal("server accepted a connection without client certificate")
	}
}

func TestKeyedLimiter(t *testing.T) {
	l := NewKeyedLimiter(1, 2, 2)
	now := time.Unix(1000, 0)
	l.now = func() time.Time { return now }
	if !l.Allow("a") || !l.Allow("a") {
		t.Fatal("burst should allow two events")
	}
	if l.Allow("a") {
		t.Fatal("third event should be limited")
	}
	if !l.Allow("b") {
		t.Fatal("independent key should be allowed")
	}
	if l.Allow("c") {
		t.Fatal("key table full: should fail closed")
	}
	now = now.Add(time.Hour)
	if !l.Allow("c") {
		t.Fatal("idle keys should have been evicted")
	}
}
