package pairing

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/universal-clipboard/universal-clipboard/daemon/internal/device"
	"github.com/universal-clipboard/universal-clipboard/daemon/pkg/protocol"
)

var (
	selfFP = protocol.CertificateFingerprint([]byte("self"))
	peerFP = protocol.CertificateFingerprint([]byte("peer"))
)

func newTestManager(t *testing.T, window time.Duration) (*Manager, *device.Registry) {
	t.Helper()
	reg, _ := device.Open("")
	m := NewManager(Options{
		Self: func() protocol.DeviceInfo {
			return protocol.DeviceInfo{ID: "self", Name: "Desktop", Platform: protocol.PlatformLinux}
		},
		Fingerprint: selfFP,
		ListenPort:  func() int { return 47823 },
		Addresses:   func() []string { return []string{"192.168.1.2:47823"} },
		Registrar:   reg,
		Window:      window,
		MaxAttempts: 3,
	})
	return m, reg
}

func pairReq(code string) protocol.PairRequest {
	return protocol.PairRequest{
		ProtocolVersion: protocol.Version,
		Device:          protocol.DeviceInfo{ID: "phone", Name: "Pixel", Platform: protocol.PlatformAndroid},
		Code:            code,
	}
}

// approveWhenPending approves (or rejects) the next pending request.
func decideWhenPending(t *testing.T, m *Manager, approve bool) <-chan Request {
	out := make(chan Request, 1)
	go func() {
		for i := 0; i < 500; i++ {
			if s := m.State(); len(s.Pending) == 1 {
				if err := m.Decide(s.Pending[0].ID, approve); err != nil {
					t.Error(err)
				}
				out <- s.Pending[0]
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
		t.Error("no pending request appeared")
		close(out)
	}()
	return out
}

func TestPairingApproved(t *testing.T) {
	m, reg := newTestManager(t, time.Minute)
	inv := m.Start()
	if _, err := protocol.ParsePairingURI(inv.URI); err != nil {
		t.Fatalf("invitation URI invalid: %v", err)
	}
	decided := decideWhenPending(t, m, true)
	resp, err := m.Submit(context.Background(), pairReq(inv.Code), peerFP, "192.168.1.50:5555")
	if err != nil {
		t.Fatal(err)
	}
	req := <-decided
	if req.VerificationCode != protocol.VerificationCode(selfFP, peerFP) {
		t.Fatal("verification code mismatch")
	}
	if resp.Status != protocol.PairApproved || resp.Device.ID != "self" {
		t.Fatalf("unexpected response %+v", resp)
	}
	d, ok := reg.ByFingerprint(peerFP)
	if !ok || d.ID != "phone" || !d.Permissions.Send || !d.Permissions.Receive {
		t.Fatalf("device not registered correctly: %+v", d)
	}
	if len(d.Addresses) != 0 {
		t.Fatal("mobile device without listen port should have no addresses")
	}
	if m.State().Open {
		t.Fatal("window should close after successful pairing")
	}
}

func TestPairingRecordsListenAddress(t *testing.T) {
	m, reg := newTestManager(t, time.Minute)
	inv := m.Start()
	decideWhenPending(t, m, true)
	req := pairReq(inv.Code)
	req.ListenPort = 50000
	if _, err := m.Submit(context.Background(), req, peerFP, "[fe80::1]:6000"); err != nil {
		t.Fatal(err)
	}
	d, _ := reg.Get("phone")
	if len(d.Addresses) != 1 || d.Addresses[0] != "[fe80::1]:50000" {
		t.Fatalf("addresses = %v", d.Addresses)
	}
}

func TestPairingRejected(t *testing.T) {
	m, reg := newTestManager(t, time.Minute)
	inv := m.Start()
	decideWhenPending(t, m, false)
	resp, err := m.Submit(context.Background(), pairReq(inv.Code), peerFP, "1.2.3.4:5")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != protocol.PairRejected {
		t.Fatalf("status = %s", resp.Status)
	}
	if len(reg.List()) != 0 {
		t.Fatal("rejected device was registered")
	}
}

func TestPairingRequiresOpenWindow(t *testing.T) {
	m, _ := newTestManager(t, time.Minute)
	if _, err := m.Submit(context.Background(), pairReq("ABCD-EFGH"), peerFP, "1.2.3.4:5"); !errors.Is(err, ErrClosed) {
		t.Fatalf("expected ErrClosed, got %v", err)
	}
}

func TestPairingAttemptLimit(t *testing.T) {
	m, _ := newTestManager(t, time.Minute)
	m.Start()
	for i := 0; i < 3; i++ {
		if _, err := m.Submit(context.Background(), pairReq("2222-2222"), peerFP, "1.2.3.4:5"); !errors.Is(err, ErrInvalidCode) {
			t.Fatalf("attempt %d: expected ErrInvalidCode, got %v", i, err)
		}
	}
	if m.State().Open {
		t.Fatal("window should close after max attempts")
	}
}

func TestPairingRejectsSelf(t *testing.T) {
	m, _ := newTestManager(t, time.Minute)
	inv := m.Start()
	if _, err := m.Submit(context.Background(), pairReq(inv.Code), selfFP, "1.2.3.4:5"); !errors.Is(err, ErrSelf) {
		t.Fatalf("expected ErrSelf, got %v", err)
	}
}

func TestPairingWindowExpiry(t *testing.T) {
	m, _ := newTestManager(t, 50*time.Millisecond)
	inv := m.Start()
	resp, err := m.Submit(context.Background(), pairReq(inv.Code), peerFP, "1.2.3.4:5")
	if err != nil {
		t.Fatal(err)
	}
	if resp.Status != protocol.PairExpired {
		t.Fatalf("status = %s", resp.Status)
	}
}

func TestPairingCodeNormalization(t *testing.T) {
	m, _ := newTestManager(t, time.Minute)
	inv := m.Start()
	decideWhenPending(t, m, true)
	lower := []byte(protocol.NormalizePairingCode(inv.Code))
	for i := range lower {
		if lower[i] >= 'A' && lower[i] <= 'Z' {
			lower[i] += 'a' - 'A'
		}
	}
	if _, err := m.Submit(context.Background(), pairReq(string(lower)), peerFP, "1.2.3.4:5"); err != nil {
		t.Fatalf("lowercase code without dash should be accepted: %v", err)
	}
}
