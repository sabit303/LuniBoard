package transport

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/universal-clipboard/universal-clipboard/daemon/internal/device"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/logging"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/pairing"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/security"
	"github.com/universal-clipboard/universal-clipboard/daemon/pkg/protocol"
)

type recordingHandler struct {
	mu        sync.Mutex
	events    []protocol.ClipboardEvent
	acks      []protocol.Ack
	connected []string
}

func (h *recordingHandler) HandleIncoming(_ context.Context, _ string, _ int64, ev protocol.ClipboardEvent) protocol.Ack {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, ev)
	if err := protocol.ValidateEvent(ev, 1<<20); err != nil {
		return protocol.Ack{EventID: ev.EventID, Status: protocol.AckRejected, Reason: "invalid"}
	}
	return protocol.Ack{EventID: ev.EventID, Status: protocol.AckApplied}
}

func (h *recordingHandler) HandleAck(_ string, a protocol.Ack) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.acks = append(h.acks, a)
}

func (h *recordingHandler) OnPeerConnected(id string) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.connected = append(h.connected, id)
}

func (h *recordingHandler) counts() (events, acks int) {
	h.mu.Lock()
	defer h.mu.Unlock()
	return len(h.events), len(h.acks)
}

type peer struct {
	name    string
	id      *security.Identity
	reg     *device.Registry
	pair    *pairing.Manager
	tm      *Manager
	handler *recordingHandler
	port    int
}

func (p *peer) info() protocol.DeviceInfo {
	return protocol.DeviceInfo{ID: p.id.DeviceID, Name: p.name, Platform: protocol.PlatformLinux}
}

func newPeer(t *testing.T, name string, port int, mut func(*Options)) *peer {
	t.Helper()
	id, err := security.LoadOrCreateIdentity(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return startPeer(t, name, id, nil, port, mut)
}

func startPeer(t *testing.T, name string, id *security.Identity, reg *device.Registry, port int, mut func(*Options)) *peer {
	t.Helper()
	if reg == nil {
		reg, _ = device.Open("")
	}
	p := &peer{name: name, id: id, reg: reg, handler: &recordingHandler{}}
	p.pair = pairing.NewManager(pairing.Options{
		Self:        p.info,
		Fingerprint: id.Fingerprint,
		ListenPort:  func() int { return p.port },
		Addresses:   func() []string { return []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(p.port))} },
		Registrar:   reg,
		Window:      time.Minute,
		MaxAttempts: 5,
	})
	opts := Options{
		Identity:        id,
		Self:            p.info,
		ListenAddress:   "127.0.0.1",
		Port:            port,
		MaxPayloadBytes: 64 << 10,
		Devices:         reg,
		Pairer:          p.pair,
		Logger:          logging.Discard(),
		Addresses: func(devID string) []string {
			d, _ := reg.Get(devID)
			return d.Addresses
		},
		DialInterval: 100 * time.Millisecond,
		MaxBackoff:   200 * time.Millisecond,
		PingInterval: time.Second,
	}
	if mut != nil {
		mut(&opts)
	}
	p.tm = New(opts)
	p.tm.SetHandler(p.handler)
	reg.Subscribe(func(c device.Change) {
		if c.Kind == device.Removed {
			p.tm.Disconnect(c.Device.ID, "revoked")
		}
	})
	if err := p.tm.Listen(); err != nil {
		t.Fatal(err)
	}
	p.port = p.tm.Port()
	if err := p.tm.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = p.tm.Close() })
	return p
}

func (p *peer) addr() string { return net.JoinHostPort("127.0.0.1", strconv.Itoa(p.port)) }

// pairPeers pairs client with server over the network, approving on the server.
func pairPeers(t *testing.T, client, server *peer) {
	t.Helper()
	inv := server.pair.Start()
	parsed, err := protocol.ParsePairingURI(inv.URI)
	if err != nil {
		t.Fatal(err)
	}
	go func() {
		for i := 0; i < 400; i++ {
			if s := server.pair.State(); len(s.Pending) == 1 {
				_ = server.pair.Decide(s.Pending[0].ID, true)
				return
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	res, err := pairing.RequestPairing(context.Background(), client.id, protocol.PairRequest{
		ProtocolVersion: protocol.Version,
		Device:          client.info(),
		Code:            parsed.Code,
		ListenPort:      client.port,
	}, parsed)
	if err != nil {
		t.Fatal(err)
	}
	if res.Response.Status != protocol.PairApproved {
		t.Fatalf("pairing status %s", res.Response.Status)
	}
	if err := client.reg.Add(device.Device{
		ID: res.Response.Device.ID, Name: res.Response.Device.Name, Platform: res.Response.Device.Platform,
		Fingerprint: parsed.Fingerprint, Addresses: []string{res.Address},
		Permissions: device.Permissions{Send: true, Receive: true},
	}); err != nil {
		t.Fatal(err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func connectedTo(p *peer, id string) bool {
	for _, s := range p.tm.Sessions() {
		if s.DeviceID == id {
			return true
		}
	}
	return false
}

// rawDial opens a WebSocket as an arbitrary identity, bypassing the manager.
func rawDial(t *testing.T, id *security.Identity, addr, fp string) (*websocket.Conn, *http.Response, error) {
	t.Helper()
	client := &http.Client{Transport: &http.Transport{TLSClientConfig: security.ClientTLSConfig(id, fp)}}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	return websocket.Dial(ctx, "wss://"+addr+protocol.PathWebSocket, &websocket.DialOptions{
		HTTPClient:   client,
		Subprotocols: []string{protocol.WebSocketSubprotocol},
	})
}

func sendHello(t *testing.T, c *websocket.Conn, info protocol.DeviceInfo) {
	t.Helper()
	msg, _ := protocol.Encode(protocol.TypeHello, protocol.Hello{
		Device: info, MinProtocolVersion: 1, MaxProtocolVersion: 1, Time: time.Now().UnixMilli(),
		Capabilities: []string{protocol.CapabilityReceive, protocol.CapabilitySend}, MaxPayloadBytes: 1 << 20,
	})
	if err := c.Write(context.Background(), websocket.MessageText, msg); err != nil {
		t.Fatal(err)
	}
}

func readEnvelope(t *testing.T, c *websocket.Conn) protocol.Envelope {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, data, err := c.Read(ctx)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	env, err := protocol.DecodeEnvelope(data)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// ---- tests -------------------------------------------------------------------

func TestPairConnectAndExchange(t *testing.T) {
	a := newPeer(t, "A", 0, nil)
	b := newPeer(t, "B", 0, nil)
	pairPeers(t, a, b)

	if _, ok := b.reg.ByFingerprint(a.id.Fingerprint); !ok {
		t.Fatal("server did not register client")
	}
	waitFor(t, "session", func() bool { return connectedTo(a, b.id.DeviceID) && connectedTo(b, a.id.DeviceID) })

	ev := protocol.NewTextEvent(protocol.NewID(), a.id.DeviceID, "hello over TLS", time.Now().UnixMilli())
	msg, _ := protocol.Encode(protocol.TypeClipboardEvent, ev)
	if err := a.tm.Send(b.id.DeviceID, msg); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "event and ack", func() bool {
		e, _ := b.handler.counts()
		_, acks := a.handler.counts()
		return e == 1 && acks == 1
	})
	if b.handler.events[0] != ev {
		t.Fatal("event altered in transit")
	}
	if a.handler.acks[0].Status != protocol.AckApplied {
		t.Fatalf("ack %+v", a.handler.acks[0])
	}
	waitFor(t, "OnPeerConnected", func() bool {
		a.handler.mu.Lock()
		defer a.handler.mu.Unlock()
		return len(a.handler.connected) >= 1
	})
}

func TestDuplicateConnectionsConverge(t *testing.T) {
	a := newPeer(t, "A", 0, nil)
	b := newPeer(t, "B", 0, nil)
	pairPeers(t, a, b) // both know each other's address, so both dial
	waitFor(t, "sessions", func() bool { return connectedTo(a, b.id.DeviceID) && connectedTo(b, a.id.DeviceID) })
	time.Sleep(500 * time.Millisecond)
	sa, sb := a.tm.Sessions(), b.tm.Sessions()
	if len(sa) != 1 || len(sb) != 1 {
		t.Fatalf("expected one session per side, got %d and %d", len(sa), len(sb))
	}
	if sa[0].Outbound == sb[0].Outbound {
		t.Fatal("both sides kept sessions with the same direction; tie-break inconsistent")
	}
	smaller := min(a.id.DeviceID, b.id.DeviceID)
	if (smaller == a.id.DeviceID) != sa[0].Outbound {
		t.Fatal("session initiated by the larger device id survived")
	}
}

func TestUnpairedDeviceRejected(t *testing.T) {
	b := newPeer(t, "B", 0, nil)
	stranger, _ := security.LoadOrCreateIdentity(t.TempDir())
	_, resp, err := rawDial(t, stranger, b.addr(), b.id.Fingerprint)
	if err == nil {
		t.Fatal("unpaired device obtained a WebSocket")
	}
	if resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %v", resp)
	}
}

func TestPairingRequiresOpenWindowAndPinnedServer(t *testing.T) {
	a := newPeer(t, "A", 0, nil)
	b := newPeer(t, "B", 0, nil)
	inv := protocol.PairingInvitation{
		Device: b.info(), Fingerprint: b.id.Fingerprint, Addresses: []string{b.addr()}, Code: "ABCD-EFGH",
	}
	req := protocol.PairRequest{ProtocolVersion: 1, Device: a.info(), Code: inv.Code}
	_, err := pairing.RequestPairing(context.Background(), a.id, req, inv)
	var remote *pairing.RemoteError
	if !asRemote(err, &remote) || remote.Code != protocol.ErrCodePairingClosed {
		t.Fatalf("expected pairing_closed, got %v", err)
	}

	b.pair.Start()
	inv.Fingerprint = a.id.Fingerprint // attacker-controlled / wrong pin
	if _, err := pairing.RequestPairing(context.Background(), a.id, req, inv); err == nil ||
		!strings.Contains(err.Error(), "does not match pinned") {
		t.Fatalf("expected pin mismatch, got %v", err)
	}
}

func asRemote(err error, target **pairing.RemoteError) bool {
	r, ok := err.(*pairing.RemoteError)
	if ok {
		*target = r
	}
	return ok
}

func TestRevocationDisconnectsAndBlocks(t *testing.T) {
	a := newPeer(t, "A", 0, nil)
	b := newPeer(t, "B", 0, nil)
	pairPeers(t, a, b)
	waitFor(t, "session", func() bool { return connectedTo(b, a.id.DeviceID) })

	if _, err := b.reg.Remove(a.id.DeviceID); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "disconnect", func() bool { return !connectedTo(b, a.id.DeviceID) && !connectedTo(a, b.id.DeviceID) })
	time.Sleep(500 * time.Millisecond) // a keeps redialing
	if connectedTo(b, a.id.DeviceID) {
		t.Fatal("revoked device reconnected")
	}
	if _, resp, err := rawDial(t, a.id, b.addr(), b.id.Fingerprint); err == nil || resp == nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("revoked device should get 401, got %v", err)
	}
}

func TestReconnectAfterRestart(t *testing.T) {
	a := newPeer(t, "A", 0, nil)
	b := newPeer(t, "B", 0, nil)
	pairPeers(t, a, b)
	waitFor(t, "session", func() bool { return connectedTo(a, b.id.DeviceID) })

	port := b.port
	_ = b.tm.Close()
	waitFor(t, "disconnect", func() bool { return !connectedTo(a, b.id.DeviceID) })

	b2 := startPeer(t, "B", b.id, b.reg, port, nil)
	waitFor(t, "reconnect", func() bool { return connectedTo(a, b.id.DeviceID) && connectedTo(b2, a.id.DeviceID) })
}

// pairedRawClient pairs a raw identity (like a mobile client) with b and returns an open, handshaken connection.
func pairedRawClient(t *testing.T, b *peer) (*websocket.Conn, protocol.DeviceInfo) {
	t.Helper()
	id, _ := security.LoadOrCreateIdentity(t.TempDir())
	info := protocol.DeviceInfo{ID: id.DeviceID, Name: "Phone", Platform: protocol.PlatformAndroid}
	if err := b.reg.Add(device.Device{ID: info.ID, Name: info.Name, Platform: info.Platform, Fingerprint: id.Fingerprint,
		Permissions: device.Permissions{Send: true, Receive: true}}); err != nil {
		t.Fatal(err)
	}
	c, _, err := rawDial(t, id, b.addr(), b.id.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { c.CloseNow() })
	if env := readEnvelope(t, c); env.Type != protocol.TypeHello {
		t.Fatalf("expected hello, got %s", env.Type)
	}
	sendHello(t, c, info)
	waitFor(t, "registration", func() bool { return connectedTo(b, info.ID) })
	return c, info
}

func TestIdentityMismatchInHello(t *testing.T) {
	b := newPeer(t, "B", 0, nil)
	id, _ := security.LoadOrCreateIdentity(t.TempDir())
	_ = b.reg.Add(device.Device{ID: id.DeviceID, Name: "Phone", Platform: protocol.PlatformAndroid, Fingerprint: id.Fingerprint})
	c, _, err := rawDial(t, id, b.addr(), b.id.Fingerprint)
	if err != nil {
		t.Fatal(err)
	}
	defer c.CloseNow()
	readEnvelope(t, c)
	sendHello(t, c, protocol.DeviceInfo{ID: "someone-else", Name: "Phone", Platform: protocol.PlatformAndroid})
	env := readEnvelope(t, c)
	var e protocol.ErrorMessage
	_ = env.DecodeData(&e)
	if env.Type != protocol.TypeError || e.Code != protocol.ErrCodeIdentityMismatch {
		t.Fatalf("expected identity_mismatch, got %s %+v", env.Type, e)
	}
}

func TestInvalidMessagesAndOversize(t *testing.T) {
	b := newPeer(t, "B", 0, nil)
	c, info := pairedRawClient(t, b)
	ctx := context.Background()

	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"clipboard.event","protocol_version":1,"data":{"event_id":`))
	env := readEnvelope(t, c)
	if env.Type != protocol.TypeError {
		t.Fatalf("expected error for malformed JSON, got %s", env.Type)
	}
	// Unknown types are ignored and the session stays usable.
	_ = c.Write(ctx, websocket.MessageText, []byte(`{"type":"future.thing","protocol_version":1,"data":{}}`))
	ev := protocol.NewTextEvent(protocol.NewID(), info.ID, "still works", time.Now().UnixMilli())
	msg, _ := protocol.Encode(protocol.TypeClipboardEvent, ev)
	_ = c.Write(ctx, websocket.MessageText, msg)
	env = readEnvelope(t, c)
	var ack protocol.Ack
	_ = env.DecodeData(&ack)
	if env.Type != protocol.TypeAck || ack.EventID != ev.EventID || ack.Status != protocol.AckApplied {
		t.Fatalf("unexpected %s %+v", env.Type, ack)
	}

	// A frame beyond the read limit terminates the connection.
	huge := make([]byte, b.tm.readLimit()+1)
	_ = c.Write(ctx, websocket.MessageText, huge)
	rctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	_, _, err := c.Read(rctx)
	if websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
		t.Fatalf("expected StatusMessageTooBig, got %v", err)
	}
}

func TestEventRateLimit(t *testing.T) {
	b := newPeer(t, "B", 0, func(o *Options) { o.EventsPerSecond = 1; o.EventBurst = 2 })
	c, info := pairedRawClient(t, b)
	ctx := context.Background()
	for i := 0; i < 5; i++ {
		ev := protocol.NewTextEvent(protocol.NewID(), info.ID, "flood "+strconv.Itoa(i), time.Now().UnixMilli())
		msg, _ := protocol.Encode(protocol.TypeClipboardEvent, ev)
		_ = c.Write(ctx, websocket.MessageText, msg)
	}
	limited := 0
	for i := 0; i < 5; i++ {
		var ack protocol.Ack
		env := readEnvelope(t, c)
		_ = json.Unmarshal(env.Data, &ack)
		if ack.Status == protocol.AckRejected && ack.Reason == protocol.ErrCodeRateLimited {
			limited++
		}
	}
	if limited != 3 {
		t.Fatalf("expected 3 rate-limited acks, got %d", limited)
	}
}
