package integration

import (
	"context"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/universal-clipboard/universal-clipboard/daemon/internal/pairing"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/security"
	"github.com/universal-clipboard/universal-clipboard/daemon/pkg/protocol"
)

// mobileClient implements the client side of the protocol the way the
// Android/iOS apps do: it never listens, it pairs via the QR invitation and
// keeps one WebSocket to the desktop.
type mobileClient struct {
	t      *testing.T
	id     *security.Identity
	info   protocol.DeviceInfo
	inv    protocol.PairingInvitation
	conn   *websocket.Conn
	mu     sync.Mutex
	events []protocol.ClipboardEvent
	acks   map[string]protocol.Ack
}

func newMobileClient(t *testing.T) *mobileClient {
	id, err := security.LoadOrCreateIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return &mobileClient{
		t:    t,
		id:   id,
		info: protocol.DeviceInfo{ID: id.DeviceID, Name: "Pixel Test", Platform: protocol.PlatformAndroid, AppVersion: "test"},
		acks: map[string]protocol.Ack{},
	}
}

// pairWith scans the desktop's QR URI and sends a pairing request; the desktop approves.
func (m *mobileClient) pairWith(desktop *node) {
	t := m.t
	ctx := context.Background()
	inv, err := desktop.client.StartPairing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	m.inv, err = protocol.ParsePairingURI(inv.URI)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		waitFor(t, "pending request", func() bool {
			st, err := desktop.client.PairingState(ctx)
			if err != nil || len(st.Pending) == 0 {
				return false
			}
			if st.Pending[0].VerificationCode != protocol.VerificationCode(m.id.Fingerprint, m.inv.Fingerprint) {
				t.Error("verification code mismatch")
			}
			return desktop.client.DecidePairing(ctx, st.Pending[0].ID, true) == nil
		})
	}()
	res, err := pairing.RequestPairing(ctx, m.id, protocol.PairRequest{
		ProtocolVersion: protocol.Version, Device: m.info, Code: m.inv.Code, ListenPort: 0,
	}, m.inv)
	if err != nil {
		t.Fatal(err)
	}
	if res.Response.Status != protocol.PairApproved || res.Response.Device.ID != desktop.id() {
		t.Fatalf("unexpected pairing response %+v", res.Response)
	}
}

func (m *mobileClient) connect() {
	t := m.t
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: security.ClientTLSConfig(m.id, m.inv.Fingerprint)}}
	conn, _, err := websocket.Dial(ctx, "wss://"+m.inv.Addresses[0]+protocol.PathWebSocket, &websocket.DialOptions{
		HTTPClient: client, Subprotocols: []string{protocol.WebSocketSubprotocol},
	})
	if err != nil {
		t.Fatal(err)
	}
	m.conn = conn
	t.Cleanup(func() { conn.CloseNow() })
	hello, _ := protocol.Encode(protocol.TypeHello, protocol.Hello{
		Device: m.info, MinProtocolVersion: 1, MaxProtocolVersion: 1, Time: time.Now().UnixMilli(),
		Capabilities: []string{protocol.CapabilitySend, protocol.CapabilityReceive}, MaxPayloadBytes: 1 << 20,
	})
	if err := conn.Write(ctx, websocket.MessageText, hello); err != nil {
		t.Fatal(err)
	}
	_, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatal(err)
	}
	env, err := protocol.DecodeEnvelope(data)
	if err != nil || env.Type != protocol.TypeHello {
		t.Fatalf("expected hello: %v %s", err, env.Type)
	}
	go m.readLoop()
}

func (m *mobileClient) readLoop() {
	for {
		_, data, err := m.conn.Read(context.Background())
		if err != nil {
			return
		}
		env, err := protocol.DecodeEnvelope(data)
		if err != nil {
			continue
		}
		switch env.Type {
		case protocol.TypeClipboardEvent:
			var ev protocol.ClipboardEvent
			if env.DecodeData(&ev) != nil {
				continue
			}
			m.mu.Lock()
			m.events = append(m.events, ev)
			m.mu.Unlock()
			ack, _ := protocol.Encode(protocol.TypeAck, protocol.Ack{EventID: ev.EventID, Status: protocol.AckApplied})
			_ = m.conn.Write(context.Background(), websocket.MessageText, ack)
		case protocol.TypeAck:
			var a protocol.Ack
			if env.DecodeData(&a) == nil {
				m.mu.Lock()
				m.acks[a.EventID] = a
				m.mu.Unlock()
			}
		}
	}
}

// share sends text the way the Android share sheet / in-app send does.
func (m *mobileClient) share(text string) protocol.ClipboardEvent {
	ev := protocol.NewTextEvent(protocol.NewID(), m.info.ID, text, time.Now().UnixMilli())
	b, _ := protocol.Encode(protocol.TypeClipboardEvent, ev)
	if err := m.conn.Write(context.Background(), websocket.MessageText, b); err != nil {
		m.t.Fatal(err)
	}
	return ev
}

func (m *mobileClient) resend(ev protocol.ClipboardEvent) {
	b, _ := protocol.Encode(protocol.TypeClipboardEvent, ev)
	_ = m.conn.Write(context.Background(), websocket.MessageText, b)
}

func (m *mobileClient) ack(id string) (protocol.Ack, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	a, ok := m.acks[id]
	return a, ok
}

func (m *mobileClient) received() []protocol.ClipboardEvent {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]protocol.ClipboardEvent(nil), m.events...)
}
