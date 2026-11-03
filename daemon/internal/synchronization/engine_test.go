package synchronization

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	cb "github.com/universal-clipboard/universal-clipboard/daemon/adapters/clipboard"
	"github.com/universal-clipboard/universal-clipboard/daemon/adapters/clipboard/mock"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/device"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/history"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/logging"
	"github.com/universal-clipboard/universal-clipboard/daemon/pkg/protocol"
)

// ---- in-memory harness -------------------------------------------------------

type clock struct {
	mu sync.Mutex
	t  time.Time
}

func (c *clock) Now() time.Time { c.mu.Lock(); defer c.mu.Unlock(); return c.t }
func (c *clock) Advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.t = c.t.Add(d)
}

type node struct {
	id      string
	e       *Engine
	clip    *mock.Clipboard
	hist    *history.Store
	devices *device.Registry
	h       *hub
}

// echo simulates the clipboard monitor noticing the current clipboard content.
func (n *node) echo() {
	_ = n.e.ObserveLocal(func() (Content, error) {
		c, err := n.clip.ReadText(context.Background())
		if err != nil {
			return Content{}, ErrNoContent
		}
		return Content{Text: c.Text, Sensitive: c.Sensitive}, nil
	})
}

// copy simulates the user copying text on this node, followed by the monitor firing.
func (n *node) copy(text string) {
	n.clip.Copy(contentOf(text))
	n.echo()
}

type hub struct {
	t       *testing.T
	clock   *clock
	mu      sync.Mutex
	nodes   map[string]*node
	links   map[string]map[string]bool
	offsets map[string]int64 // receiver-observed clock offset per sender
}

func newHub(t *testing.T) *hub {
	return &hub{
		t:       t,
		clock:   &clock{t: time.Unix(1_800_000_000, 0)},
		nodes:   make(map[string]*node),
		links:   make(map[string]map[string]bool),
		offsets: make(map[string]int64),
	}
}

func defaultSettings() Settings {
	return Settings{
		AutoSend:          true,
		Receive:           true,
		SkipSensitive:     true,
		MaxPayloadBytes:   1024,
		MaxEventAge:       2 * time.Minute,
		OrderingTolerance: 2 * time.Second,
	}
}

func (h *hub) add(id string, mut func(*Settings)) *node {
	s := defaultSettings()
	if mut != nil {
		mut(&s)
	}
	reg, _ := device.Open("")
	hist, _ := history.Open(history.Options{Enabled: true, MaxEntries: 100})
	n := &node{id: id, clip: mock.New(), hist: hist, devices: reg, h: h}
	n.e = NewEngine(Options{
		Self:      protocol.DeviceInfo{ID: id, Name: id, Platform: protocol.PlatformLinux},
		Settings:  s,
		Clipboard: n.clip,
		Peers:     hubPeers{h: h, self: id},
		Devices:   reg,
		History:   hist,
		Logger:    logging.Discard(),
		Now:       h.clock.Now,
	})
	h.nodes[id] = n
	h.links[id] = make(map[string]bool)
	return n
}

// pair trusts both nodes mutually and connects them.
func (h *hub) pair(a, b *node) {
	for _, pr := range [][2]*node{{a, b}, {b, a}} {
		if err := pr[0].devices.Add(device.Device{
			ID: pr[1].id, Name: pr[1].id, Platform: protocol.PlatformLinux,
			Fingerprint: protocol.CertificateFingerprint([]byte(pr[1].id)),
			Permissions: device.Permissions{Send: true, Receive: true},
		}); err != nil {
			h.t.Fatal(err)
		}
	}
	h.connect(a, b)
}

func (h *hub) connect(a, b *node) {
	h.mu.Lock()
	h.links[a.id][b.id] = true
	h.links[b.id][a.id] = true
	h.mu.Unlock()
}

func (h *hub) disconnect(a, b *node) {
	h.mu.Lock()
	delete(h.links[a.id], b.id)
	delete(h.links[b.id], a.id)
	h.mu.Unlock()
}

type hubPeers struct {
	h    *hub
	self string
}

func (p hubPeers) Connected() []PeerInfo {
	p.h.mu.Lock()
	defer p.h.mu.Unlock()
	var out []PeerInfo
	for id := range p.h.links[p.self] {
		out = append(out, PeerInfo{ID: id, CanReceive: true, MaxPayloadBytes: 1 << 20})
	}
	return out
}

func (p hubPeers) Send(to string, msg []byte) error {
	p.h.mu.Lock()
	linked := p.h.links[p.self][to]
	dst, src := p.h.nodes[to], p.h.nodes[p.self]
	offset := p.h.offsets[p.self]
	p.h.mu.Unlock()
	if !linked {
		return errors.New("not connected")
	}
	env, err := protocol.DecodeEnvelope(msg)
	if err != nil {
		p.h.t.Fatalf("engine produced invalid envelope: %v", err)
	}
	var ev protocol.ClipboardEvent
	if err := env.DecodeData(&ev); err != nil {
		p.h.t.Fatal(err)
	}
	ack := dst.e.HandleIncoming(context.Background(), p.self, offset, ev)
	src.e.HandleAck(to, ack)
	return nil
}

type clipboardContent = cb.Content

func contentOf(text string) clipboardContent { return clipboardContent{Text: text} }

// ---- tests -------------------------------------------------------------------

func TestBroadcastToAllPeers(t *testing.T) {
	h := newHub(t)
	a, b, c := h.add("a", nil), h.add("b", nil), h.add("c", nil)
	h.pair(a, b)
	h.pair(a, c)

	a.copy("hello")
	if b.clip.Text() != "hello" || c.clip.Text() != "hello" {
		t.Fatalf("b=%q c=%q", b.clip.Text(), c.clip.Text())
	}
	e := a.hist.List(0)[0]
	if e.Direction != history.Outgoing || e.Deliveries["b"] != "applied" || e.Deliveries["c"] != "applied" {
		t.Fatalf("unexpected history entry %+v", e)
	}
	if got := b.hist.List(0)[0]; got.Direction != history.Incoming || got.OriginDeviceID != "a" {
		t.Fatalf("unexpected incoming entry %+v", got)
	}
}

func TestBidirectionalWithoutLoops(t *testing.T) {
	h := newHub(t)
	a, b := h.add("a", nil), h.add("b", nil)
	h.pair(a, b)

	a.copy("from a")
	b.echo() // B's monitor sees the write performed by the engine
	if b.e.Stats().Sent != 0 {
		t.Fatal("echo of a remote write was re-broadcast (loop)")
	}
	h.clock.Advance(time.Second)
	b.copy("from b")
	a.echo()
	if a.clip.Text() != "from b" {
		t.Fatalf("a = %q", a.clip.Text())
	}
	if a.e.Stats().Sent != 1 || b.e.Stats().Sent != 1 {
		t.Fatalf("unexpected sends a=%v b=%v", a.e.Stats(), b.e.Stats())
	}
	if a.clip.Writes() != 1 || b.clip.Writes() != 1 {
		t.Fatalf("unexpected writes a=%d b=%d", a.clip.Writes(), b.clip.Writes())
	}
}

func TestRepeatedNotificationNotResent(t *testing.T) {
	h := newHub(t)
	a, b := h.add("a", nil), h.add("b", nil)
	h.pair(a, b)
	a.copy("same")
	a.echo()
	a.echo()
	if a.e.Stats().Sent != 1 {
		t.Fatalf("sent %d times", a.e.Stats().Sent)
	}
}

func TestInitialContentNotBroadcast(t *testing.T) {
	h := newHub(t)
	a, b := h.add("a", nil), h.add("b", nil)
	h.pair(a, b)
	a.clip.Copy(contentOf("pre-existing"))
	a.e.ObserveInitial("pre-existing")
	a.echo()
	if b.clip.Writes() != 0 {
		t.Fatal("startup content was broadcast")
	}
}

func incoming(h *hub, origin, text string, ts time.Time) protocol.ClipboardEvent {
	return protocol.NewTextEvent(protocol.NewID(), origin, text, ts.UnixMilli())
}

func TestOwnOriginAndDuplicates(t *testing.T) {
	h := newHub(t)
	a, b := h.add("a", nil), h.add("b", nil)
	h.pair(a, b)
	ctx := context.Background()

	if ack := a.e.HandleIncoming(ctx, "b", 0, incoming(h, "a", "x", h.clock.Now())); ack.Status != protocol.AckIgnored {
		t.Fatalf("own origin: %+v", ack)
	}
	ev := incoming(h, "b", "x", h.clock.Now())
	if ack := a.e.HandleIncoming(ctx, "b", 0, ev); ack.Status != protocol.AckApplied {
		t.Fatalf("first: %+v", ack)
	}
	if ack := a.e.HandleIncoming(ctx, "b", 0, ev); ack.Status != protocol.AckDuplicate {
		t.Fatalf("second: %+v", ack)
	}
	if a.clip.Writes() != 1 {
		t.Fatalf("writes = %d", a.clip.Writes())
	}
}

func TestExpiryStaleAndFuture(t *testing.T) {
	h := newHub(t)
	a, b := h.add("a", nil), h.add("b", nil)
	h.pair(a, b)
	ctx := context.Background()
	now := h.clock.Now()

	if ack := a.e.HandleIncoming(ctx, "b", 0, incoming(h, "b", "old", now.Add(-3*time.Minute))); ack.Status != protocol.AckExpired {
		t.Fatalf("expired: %+v", ack)
	}
	if ack := a.e.HandleIncoming(ctx, "b", 0, incoming(h, "b", "future", now.Add(time.Hour))); ack.Status != protocol.AckRejected {
		t.Fatalf("future: %+v", ack)
	}

	a.copy("newer local content")
	// Delayed event created 10s before the local copy must not overwrite it.
	if ack := a.e.HandleIncoming(ctx, "b", 0, incoming(h, "b", "delayed", now.Add(-10*time.Second))); ack.Status != protocol.AckStale {
		t.Fatalf("stale: %+v", ack)
	}
	// Within the ordering tolerance (clock jitter) the event is still applied.
	if ack := a.e.HandleIncoming(ctx, "b", 0, incoming(h, "b", "jitter", now.Add(-time.Second))); ack.Status != protocol.AckApplied {
		t.Fatalf("tolerance: %+v", ack)
	}
	if a.clip.Text() != "jitter" {
		t.Fatalf("clipboard = %q", a.clip.Text())
	}
}

func TestClockOffsetCorrection(t *testing.T) {
	h := newHub(t)
	a, b := h.add("a", nil), h.add("b", nil)
	h.pair(a, b)
	// b's clock runs 10 minutes ahead of a's.
	offset := (10 * time.Minute).Milliseconds()
	ev := incoming(h, "b", "skewed", h.clock.Now().Add(10*time.Minute))
	if ack := a.e.HandleIncoming(context.Background(), "b", offset, ev); ack.Status != protocol.AckApplied {
		t.Fatalf("expected skew-corrected event to apply: %+v", ack)
	}
}

func TestPausedAndManualSend(t *testing.T) {
	h := newHub(t)
	a, b := h.add("a", nil), h.add("b", nil)
	h.pair(a, b)
	a.e.SetPaused(true)
	b.e.SetPaused(true)

	a.copy("not synced")
	if b.clip.Writes() != 0 {
		t.Fatal("paused sender broadcast")
	}
	res, err := a.e.SendManual(context.Background(), "manual", nil, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Results["b"]; got.Status != "ignored" || got.Reason != "paused" {
		t.Fatalf("paused receiver should ignore: %+v", res)
	}
	b.e.SetPaused(false)
	res, _ = a.e.SendManual(context.Background(), "manual 2", nil, time.Second)
	if res.Results["b"].Status != "applied" || b.clip.Text() != "manual 2" {
		t.Fatalf("manual send while paused should work: %+v", res)
	}
	if a.clip.Text() != "not synced" {
		t.Fatal("manual send must not modify the local clipboard")
	}
}

func TestSelectiveManualSend(t *testing.T) {
	h := newHub(t)
	a, b, c := h.add("a", nil), h.add("b", nil), h.add("c", nil)
	h.pair(a, b)
	h.pair(a, c)
	res, err := a.e.SendManual(context.Background(), "only c", []string{"c", "nope"}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if b.clip.Writes() != 0 || c.clip.Text() != "only c" {
		t.Fatal("selective send reached wrong devices")
	}
	if res.Results["c"].Status != "applied" || res.Results["nope"].Status != "unknown_device" {
		t.Fatalf("results %+v", res.Results)
	}
	h.disconnect(a, c)
	res, _ = a.e.SendManual(context.Background(), "offline", []string{"c"}, 0)
	if res.Results["c"].Status != "offline" {
		t.Fatalf("results %+v", res.Results)
	}
}

func TestPermissions(t *testing.T) {
	h := newHub(t)
	a, b, c := h.add("a", nil), h.add("b", nil), h.add("c", nil)
	h.pair(a, b)
	h.pair(a, c)
	// a excludes c from sending; b refuses events from a.
	if _, err := a.devices.SetPermissions("c", device.Permissions{Send: false, Receive: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := b.devices.SetPermissions("a", device.Permissions{Send: true, Receive: false}); err != nil {
		t.Fatal(err)
	}
	a.copy("secret-ish")
	if c.clip.Writes() != 0 {
		t.Fatal("excluded device received content")
	}
	if b.clip.Writes() != 0 {
		t.Fatal("device with receive disabled applied content")
	}
	if got := a.hist.List(0)[0].Deliveries["b"]; got != "rejected" {
		t.Fatalf("b delivery = %q", got)
	}
}

func TestExcludedTypesSensitiveAndSize(t *testing.T) {
	h := newHub(t)
	a := h.add("a", func(s *Settings) { s.ExcludedContentTypes = []protocol.ContentType{protocol.ContentURL} })
	b := h.add("b", nil)
	h.pair(a, b)

	a.copy("https://example.com/private")
	a.clip.Copy(clipboardContent{Text: "hunter2", Sensitive: true})
	a.echo()
	a.copy(strings.Repeat("x", 2048))
	if b.clip.Writes() != 0 {
		t.Fatalf("b received %q", b.clip.Text())
	}
	if _, err := a.e.SendManual(context.Background(), "https://example.com", nil, 0); !errors.Is(err, ErrExcluded) {
		t.Fatalf("expected ErrExcluded, got %v", err)
	}
	if _, err := a.e.SendManual(context.Background(), strings.Repeat("x", 2048), nil, 0); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}

	big := protocol.NewTextEvent(protocol.NewID(), "a", strings.Repeat("y", 4096), h.clock.Now().UnixMilli())
	if ack := b.e.HandleIncoming(context.Background(), "a", 0, big); ack.Status != protocol.AckRejected || ack.Reason != protocol.ErrCodeTooLarge {
		t.Fatalf("large payload: %+v", ack)
	}
	bad := incoming(h, "a", "tampered", h.clock.Now())
	bad.Payload.Text = "TAMPERED"
	if ack := b.e.HandleIncoming(context.Background(), "a", 0, bad); ack.Status != protocol.AckRejected {
		t.Fatalf("tampered payload: %+v", ack)
	}
}

func TestReconnectSendsOnlyFreshLatest(t *testing.T) {
	h := newHub(t)
	a, b, c := h.add("a", nil), h.add("b", nil), h.add("c", nil)
	h.pair(a, b)
	h.pair(a, c)
	h.disconnect(a, b)
	h.disconnect(a, c)

	a.copy("while offline")
	h.clock.Advance(30 * time.Second)
	h.connect(a, b)
	a.e.OnPeerConnected("b")
	if b.clip.Text() != "while offline" {
		t.Fatal("fresh latest event not delivered on reconnect")
	}
	a.e.OnPeerConnected("b")
	if b.clip.Writes() != 1 {
		t.Fatal("latest event re-sent to a peer that already has it")
	}

	h.clock.Advance(5 * time.Minute)
	h.connect(a, c)
	a.e.OnPeerConnected("c")
	if c.clip.Writes() != 0 {
		t.Fatal("stale event replayed on reconnect")
	}
}

func TestRelay(t *testing.T) {
	for _, relay := range []bool{false, true} {
		h := newHub(t)
		a := h.add("a", nil)
		b := h.add("b", func(s *Settings) { s.Relay = relay })
		c := h.add("c", nil)
		h.pair(a, b)
		h.pair(b, c)
		a.copy("hop")
		if got := c.clip.Text() == "hop"; got != relay {
			t.Fatalf("relay=%v: c received=%v", relay, got)
		}
	}
}

func TestRelayMeshAppliesOnce(t *testing.T) {
	h := newHub(t)
	relay := func(s *Settings) { s.Relay = true }
	a, b, c := h.add("a", relay), h.add("b", relay), h.add("c", relay)
	h.pair(a, b)
	h.pair(b, c)
	h.pair(c, a)
	a.copy("mesh")
	if b.clip.Writes() != 1 || c.clip.Writes() != 1 || a.clip.Writes() != 0 {
		t.Fatalf("writes a=%d b=%d c=%d", a.clip.Writes(), b.clip.Writes(), c.clip.Writes())
	}
}

func TestClipboardWriteFailure(t *testing.T) {
	h := newHub(t)
	a, b := h.add("a", nil), h.add("b", nil)
	h.pair(a, b)
	b.clip.FailWrites(errors.New("no display"))
	a.copy("x")
	if got := a.hist.List(0)[0].Deliveries["b"]; got != "rejected" {
		t.Fatalf("delivery = %q", got)
	}
	// After recovery the user copying the same text on b must still be detected.
	b.clip.FailWrites(nil)
	h.clock.Advance(time.Second)
	b.copy("x")
	if b.e.Stats().Sent != 1 {
		t.Fatal("hash was not restored after failed write")
	}
}

func TestSeenCacheBounds(t *testing.T) {
	c := newSeenCache(time.Minute, 3)
	now := time.Unix(0, 0)
	for _, id := range []string{"a", "b", "c", "d"} {
		c.Add(id, now)
	}
	if c.Len() != 3 || c.Seen("a", now) || !c.Seen("d", now) {
		t.Fatal("count bound not enforced")
	}
	later := now.Add(2 * time.Minute)
	if c.Seen("d", later) {
		t.Fatal("ttl not enforced")
	}
	c.Add("e", later)
	if c.Len() != 1 {
		t.Fatalf("expired entries not pruned: %d", c.Len())
	}
}
