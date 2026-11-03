// Package synchronization implements the platform-independent clipboard
// synchronization policy: event creation, fan-out, duplicate detection, loop
// prevention, expiry/ordering and acknowledgements.
//
// The engine knows nothing about sockets, operating systems or clipboard
// tools; it talks to them through the small interfaces defined below.
package synchronization

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/universal-clipboard/universal-clipboard/daemon/internal/device"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/history"
	"github.com/universal-clipboard/universal-clipboard/daemon/pkg/protocol"
)

// Clipboard writes text to the local system clipboard.
type Clipboard interface {
	WriteText(ctx context.Context, text string) error
}

// PeerInfo describes a currently connected peer.
type PeerInfo struct {
	ID              string
	CanReceive      bool
	MaxPayloadBytes int64
}

// Peers sends encoded envelopes to connected peers.
type Peers interface {
	Connected() []PeerInfo
	Send(peerID string, msg []byte) error
}

// Devices looks up trusted devices and their permissions.
type Devices interface {
	Get(id string) (device.Device, bool)
}

// History records clipboard events.
type History interface {
	Add(history.Entry)
	UpdateDelivery(eventID, deviceID string, status history.DeliveryStatus)
}

// Settings are the policy knobs of the engine.
type Settings struct {
	AutoSend          bool
	Receive           bool
	SkipSensitive     bool
	Relay             bool
	MaxPayloadBytes   int64
	MaxEventAge       time.Duration
	OrderingTolerance time.Duration
	// MaxFutureSkew rejects events stamped further in the future than this.
	MaxFutureSkew        time.Duration
	ExcludedContentTypes []protocol.ContentType
	// SeenTTL and SeenMax bound the duplicate-detection cache.
	SeenTTL time.Duration
	SeenMax int
}

// Content is a local clipboard observation.
type Content struct {
	Text string
	// Sensitive is set when the source application flagged the content as secret.
	Sensitive bool
}

// Options wires an Engine.
type Options struct {
	Self      protocol.DeviceInfo
	Settings  Settings
	Clipboard Clipboard // nil when running headless
	Peers     Peers
	Devices   Devices
	History   History
	Logger    *slog.Logger
	Now       func() time.Time
	Paused    bool
}

// Engine is the synchronization core. It is safe for concurrent use.
type Engine struct {
	self     protocol.DeviceInfo
	settings Settings
	clip     Clipboard
	peers    Peers
	devices  Devices
	hist     History
	log      *slog.Logger
	now      func() time.Time
	paused   atomic.Bool

	// writeMu serializes clipboard writes so the echo-suppression hash always
	// corresponds to the most recent write.
	writeMu sync.Mutex

	mu              sync.Mutex
	seen            *seenCache
	lastKnownHash   string
	latestMillis    int64
	latest          *protocol.ClipboardEvent
	latestDelivered map[string]bool
	waiters         map[string]*ackWaiter
	stats           Stats
}

// Stats are counters exposed through the local API.
type Stats struct {
	Sent       uint64 `json:"sent"`
	Received   uint64 `json:"received"`
	Applied    uint64 `json:"applied"`
	Duplicates uint64 `json:"duplicates"`
	Rejected   uint64 `json:"rejected"`
}

type ackWaiter struct {
	pending map[string]bool
	results map[string]protocol.Ack
	done    chan struct{}
	// sending is true while targets are still being added.
	sending bool
	closed  bool
}

func (w *ackWaiter) maybeCloseLocked() {
	if !w.sending && !w.closed && len(w.pending) == 0 {
		w.closed = true
		close(w.done)
	}
}

// NewEngine creates an engine.
func NewEngine(o Options) *Engine {
	s := o.Settings
	if s.SeenTTL == 0 {
		s.SeenTTL = max(10*time.Minute, 2*s.MaxEventAge)
	}
	if s.SeenMax == 0 {
		s.SeenMax = 10000
	}
	if s.MaxFutureSkew == 0 {
		s.MaxFutureSkew = 5 * time.Minute
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	e := &Engine{
		self:            o.Self,
		settings:        s,
		clip:            o.Clipboard,
		peers:           o.Peers,
		devices:         o.Devices,
		hist:            o.History,
		log:             o.Logger,
		now:             o.Now,
		seen:            newSeenCache(s.SeenTTL, s.SeenMax),
		latestDelivered: make(map[string]bool),
		waiters:         make(map[string]*ackWaiter),
	}
	e.paused.Store(o.Paused)
	return e
}

// SetPaused pauses or resumes synchronization at runtime.
func (e *Engine) SetPaused(p bool) { e.paused.Store(p) }

// Paused reports whether synchronization is paused.
func (e *Engine) Paused() bool { return e.paused.Load() }

// Stats returns a snapshot of the counters.
func (e *Engine) Stats() Stats {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.stats
}

func (e *Engine) excluded(ct protocol.ContentType) bool {
	return slices.Contains(e.settings.ExcludedContentTypes, ct)
}

// ObserveInitial records the clipboard content present at startup so that it
// is not broadcast as if it were a fresh change.
func (e *Engine) ObserveInitial(text string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.lastKnownHash = protocol.HashText(text)
}

// ErrNoContent may be returned by a ReadFunc when the clipboard holds no text.
var ErrNoContent = errors.New("clipboard holds no text")

// ReadFunc reads the current local clipboard content.
type ReadFunc func() (Content, error)

// ObserveLocal reads the local clipboard and processes the result as a
// potential local change. The read runs under the same lock as clipboard
// writes, so a read can never observe a half-applied remote update and
// mistake the previous content for a new local copy.
func (e *Engine) ObserveLocal(read ReadFunc) error {
	e.writeMu.Lock()
	c, err := read()
	if err != nil {
		e.writeMu.Unlock()
		if errors.Is(err, ErrNoContent) {
			return nil
		}
		return err
	}
	ev := e.processLocal(c)
	e.writeMu.Unlock()
	if ev != nil {
		e.record(*ev, history.Outgoing, history.SourceClipboard, "")
		e.broadcast(*ev, nil)
	}
	return nil
}

// HandleLocalChange processes already-read local clipboard content.
func (e *Engine) HandleLocalChange(c Content) {
	_ = e.ObserveLocal(func() (Content, error) { return c, nil })
}

// processLocal applies local-change policy. Callers hold writeMu.
func (e *Engine) processLocal(c Content) *protocol.ClipboardEvent {
	if c.Text == "" {
		return nil
	}
	hash := protocol.HashText(c.Text)
	now := e.now()

	e.mu.Lock()
	if hash == e.lastKnownHash {
		// Echo of our own write, or a repeated notification for unchanged content.
		e.mu.Unlock()
		return nil
	}
	e.lastKnownHash = hash
	e.latestMillis = now.UnixMilli()
	e.latest = nil
	clear(e.latestDelivered)

	reason := ""
	ct := protocol.ClassifyText(c.Text)
	switch {
	case c.Sensitive && e.settings.SkipSensitive:
		reason = "sensitive content"
	case e.paused.Load():
		reason = "paused"
	case !e.settings.AutoSend:
		reason = "auto_send disabled"
	case int64(len(c.Text)) > e.settings.MaxPayloadBytes:
		reason = "too large"
	case e.excluded(ct):
		reason = "excluded content type"
	}
	if reason != "" {
		e.mu.Unlock()
		e.log.Debug("local clipboard change not synchronized", "reason", reason)
		return nil
	}
	ev := protocol.NewTextEvent(protocol.NewID(), e.self.ID, c.Text, now.UnixMilli())
	e.seen.Add(ev.EventID, now)
	e.latest = &ev
	e.mu.Unlock()
	return &ev
}

// SendResult reports the per-device outcome of a manual send.
type SendResult struct {
	EventID string                   `json:"event_id"`
	Results map[string]DeviceOutcome `json:"results"`
}

// DeviceOutcome is the delivery result for one device.
type DeviceOutcome struct {
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

// Errors returned by SendManual.
var (
	ErrEmpty    = errors.New("nothing to send")
	ErrTooLarge = errors.New("content exceeds max_payload_bytes")
	ErrExcluded = errors.New("content type is excluded from synchronization")
)

// SendManual sends text to the given devices (all connected, permitted devices
// when targets is empty) and waits up to ackTimeout for acknowledgements.
// Manual sends are explicit user actions and therefore work while paused.
func (e *Engine) SendManual(ctx context.Context, text string, targets []string, ackTimeout time.Duration) (SendResult, error) {
	if text == "" {
		return SendResult{}, ErrEmpty
	}
	if int64(len(text)) > e.settings.MaxPayloadBytes {
		return SendResult{}, ErrTooLarge
	}
	now := e.now()
	ev := protocol.NewTextEvent(protocol.NewID(), e.self.ID, text, now.UnixMilli())
	if e.excluded(ev.ContentType) {
		return SendResult{}, ErrExcluded
	}
	res := SendResult{EventID: ev.EventID, Results: make(map[string]DeviceOutcome)}

	connected := make(map[string]PeerInfo)
	for _, p := range e.peers.Connected() {
		connected[p.ID] = p
	}
	if len(targets) == 0 {
		for id := range connected {
			targets = append(targets, id)
		}
	}

	waiter := &ackWaiter{pending: make(map[string]bool), results: make(map[string]protocol.Ack), done: make(chan struct{}), sending: true}
	e.mu.Lock()
	e.seen.Add(ev.EventID, now)
	e.waiters[ev.EventID] = waiter
	e.mu.Unlock()
	defer func() {
		e.mu.Lock()
		delete(e.waiters, ev.EventID)
		e.mu.Unlock()
	}()

	e.record(ev, history.Outgoing, history.SourceManual, "")
	msg, err := protocol.Encode(protocol.TypeClipboardEvent, ev)
	if err != nil {
		return SendResult{}, err
	}
	for _, id := range targets {
		// Register before sending: a fast peer may acknowledge before Send returns.
		e.mu.Lock()
		waiter.pending[id] = true
		e.mu.Unlock()
		if out, ok := e.sendTo(id, connected, ev, msg); !ok {
			e.mu.Lock()
			delete(waiter.pending, id)
			e.mu.Unlock()
			res.Results[id] = out
			continue
		}
		res.Results[id] = DeviceOutcome{Status: string(history.DeliverySent)}
	}

	e.mu.Lock()
	waiter.sending = false
	waiter.maybeCloseLocked()
	e.mu.Unlock()

	if ackTimeout > 0 {
		timer := time.NewTimer(ackTimeout)
		defer timer.Stop()
		select {
		case <-waiter.done:
		case <-timer.C:
		case <-ctx.Done():
		}
	}
	e.mu.Lock()
	for id, ack := range waiter.results {
		res.Results[id] = DeviceOutcome{Status: string(ack.Status), Reason: ack.Reason}
	}
	e.mu.Unlock()
	return res, nil
}

// sendTo checks eligibility of one target and sends msg. It returns false with
// an outcome when the device was not sent to.
func (e *Engine) sendTo(id string, connected map[string]PeerInfo, ev protocol.ClipboardEvent, msg []byte) (DeviceOutcome, bool) {
	d, ok := e.devices.Get(id)
	if !ok {
		return DeviceOutcome{Status: "unknown_device"}, false
	}
	if !d.Permissions.Send {
		return DeviceOutcome{Status: "not_permitted", Reason: "sending to this device is disabled"}, false
	}
	p, ok := connected[id]
	if !ok {
		return DeviceOutcome{Status: "offline"}, false
	}
	if !p.CanReceive {
		return DeviceOutcome{Status: "not_supported", Reason: "device does not accept clipboard events"}, false
	}
	if p.MaxPayloadBytes > 0 && ev.Payload.Size > p.MaxPayloadBytes {
		return DeviceOutcome{Status: "too_large", Reason: "exceeds the device's payload limit"}, false
	}
	// Recorded before sending: the acknowledgement may arrive before Send returns.
	e.hist.UpdateDelivery(ev.EventID, id, history.DeliverySent)
	if err := e.peers.Send(id, msg); err != nil {
		e.hist.UpdateDelivery(ev.EventID, id, history.DeliveryFailed)
		return DeviceOutcome{Status: string(history.DeliveryFailed), Reason: err.Error()}, false
	}
	e.mu.Lock()
	e.stats.Sent++
	e.mu.Unlock()
	return DeviceOutcome{}, true
}

// broadcast sends ev to every eligible connected peer except the origin and those in exclude.
func (e *Engine) broadcast(ev protocol.ClipboardEvent, exclude []string) {
	msg, err := protocol.Encode(protocol.TypeClipboardEvent, ev)
	if err != nil {
		e.log.Error("encode event", "error", err)
		return
	}
	connected := make(map[string]PeerInfo)
	for _, p := range e.peers.Connected() {
		connected[p.ID] = p
	}
	for id := range connected {
		if id == ev.OriginDeviceID || slices.Contains(exclude, id) {
			continue
		}
		if out, ok := e.sendTo(id, connected, ev, msg); !ok {
			e.log.Debug("event not sent", "event_id", ev.EventID, "device_id", id, "status", out.Status)
			continue
		}
		e.markDelivered(ev.EventID, id)
	}
}

func (e *Engine) markDelivered(eventID, peerID string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.latest != nil && e.latest.EventID == eventID {
		e.latestDelivered[peerID] = true
	}
}

// HandleIncoming processes a clipboard event received from a directly
// connected peer. clockOffsetMillis is the estimated (peer - local) clock
// difference for that connection. It returns the acknowledgement to send back.
func (e *Engine) HandleIncoming(ctx context.Context, fromPeer string, clockOffsetMillis int64, ev protocol.ClipboardEvent) protocol.Ack {
	ack := e.handleIncoming(ctx, fromPeer, clockOffsetMillis, ev)
	e.mu.Lock()
	e.stats.Received++
	switch ack.Status {
	case protocol.AckApplied:
		e.stats.Applied++
	case protocol.AckDuplicate:
		e.stats.Duplicates++
	case protocol.AckRejected:
		e.stats.Rejected++
	}
	e.mu.Unlock()
	lvl := slog.LevelDebug
	if ack.Status == protocol.AckApplied || ack.Status == protocol.AckRejected {
		lvl = slog.LevelInfo
	}
	e.log.Log(ctx, lvl, "clipboard event processed", "event_id", ev.EventID, "from", fromPeer,
		"origin", ev.OriginDeviceID, "status", ack.Status, "reason", ack.Reason, "bytes", len(ev.Payload.Text))
	return ack
}

func (e *Engine) handleIncoming(ctx context.Context, fromPeer string, offset int64, ev protocol.ClipboardEvent) protocol.Ack {
	ack := func(s protocol.AckStatus, reason string) protocol.Ack {
		return protocol.Ack{EventID: ev.EventID, Status: s, Reason: reason}
	}
	if !protocol.ValidID(ev.EventID) {
		// Cannot even reference the event; respond with a sanitized id.
		return protocol.Ack{EventID: "invalid", Status: protocol.AckRejected, Reason: "invalid event_id"}
	}
	if err := protocol.ValidateEvent(ev, e.settings.MaxPayloadBytes); err != nil {
		if errors.Is(err, protocol.ErrTooLarge) {
			return ack(protocol.AckRejected, protocol.ErrCodeTooLarge)
		}
		return ack(protocol.AckRejected, truncate(err.Error(), protocol.MaxReasonLength))
	}
	if ev.OriginDeviceID == e.self.ID {
		return ack(protocol.AckIgnored, "own_origin")
	}
	now := e.now()

	e.mu.Lock()
	if e.seen.Seen(ev.EventID, now) {
		e.mu.Unlock()
		return ack(protocol.AckDuplicate, "")
	}
	e.seen.Add(ev.EventID, now)
	e.mu.Unlock()

	d, ok := e.devices.Get(fromPeer)
	if !ok || !d.Permissions.Receive {
		return ack(protocol.AckRejected, "not_permitted")
	}
	if !e.settings.Receive {
		return ack(protocol.AckIgnored, "receive_disabled")
	}
	if e.paused.Load() {
		return ack(protocol.AckIgnored, "paused")
	}
	if e.excluded(ev.ContentType) {
		return ack(protocol.AckIgnored, "excluded_content_type")
	}

	adjusted := ev.Timestamp
	if ev.OriginDeviceID == fromPeer {
		adjusted -= offset
	}
	nowMillis := now.UnixMilli()
	if adjusted > nowMillis+e.settings.MaxFutureSkew.Milliseconds() {
		return ack(protocol.AckRejected, "timestamp_in_future")
	}
	if nowMillis-adjusted > e.settings.MaxEventAge.Milliseconds() {
		return ack(protocol.AckExpired, "")
	}

	hash := ev.Payload.SHA256
	e.writeMu.Lock()
	e.mu.Lock()
	if adjusted < e.latestMillis-e.settings.OrderingTolerance.Milliseconds() {
		e.mu.Unlock()
		e.writeMu.Unlock()
		return ack(protocol.AckStale, "newer clipboard content present")
	}
	unchanged := hash == e.lastKnownHash
	prevHash := e.lastKnownHash
	// Set before writing so the clipboard monitor treats the resulting change as an echo.
	e.lastKnownHash = hash
	e.mu.Unlock()

	if !unchanged && e.clip != nil {
		if err := e.clip.WriteText(ctx, ev.Payload.Text); err != nil {
			e.mu.Lock()
			if e.lastKnownHash == hash {
				e.lastKnownHash = prevHash
			}
			e.mu.Unlock()
			e.writeMu.Unlock()
			e.log.Warn("write clipboard", "error", err)
			return ack(protocol.AckRejected, "clipboard_unavailable")
		}
	}
	e.mu.Lock()
	e.latestMillis = max(e.latestMillis, adjusted)
	evCopy := ev
	e.latest = &evCopy
	clear(e.latestDelivered)
	e.latestDelivered[fromPeer] = true
	e.mu.Unlock()
	e.writeMu.Unlock()

	e.record(ev, history.Incoming, history.SourcePeer, fromPeer)
	if e.settings.Relay {
		e.broadcast(ev, []string{fromPeer})
	}
	return ack(protocol.AckApplied, "")
}

// HandleAck records an acknowledgement from a peer.
func (e *Engine) HandleAck(fromPeer string, a protocol.Ack) {
	if err := protocol.ValidateAck(a); err != nil {
		e.log.Debug("invalid ack", "from", fromPeer, "error", err)
		return
	}
	e.hist.UpdateDelivery(a.EventID, fromPeer, history.DeliveryStatus(a.Status))
	e.mu.Lock()
	defer e.mu.Unlock()
	if w, ok := e.waiters[a.EventID]; ok && w.pending[fromPeer] {
		delete(w.pending, fromPeer)
		w.results[fromPeer] = a
		w.maybeCloseLocked()
	}
}

// OnPeerConnected offers the current clipboard event to a newly connected
// peer if it is still fresh and the peer has not received it yet. Older
// events are never replayed.
func (e *Engine) OnPeerConnected(peerID string) {
	e.mu.Lock()
	ev := e.latest
	if ev == nil || e.latestDelivered[peerID] || ev.OriginDeviceID == peerID {
		e.mu.Unlock()
		return
	}
	age := e.now().UnixMilli() - e.latestMillis
	evCopy := *ev
	e.mu.Unlock()

	if age > e.settings.MaxEventAge.Milliseconds() || e.paused.Load() {
		return
	}
	if evCopy.OriginDeviceID == e.self.ID && !e.settings.AutoSend {
		return
	}
	if evCopy.OriginDeviceID != e.self.ID && !e.settings.Relay {
		return
	}
	connected := make(map[string]PeerInfo)
	for _, p := range e.peers.Connected() {
		connected[p.ID] = p
	}
	msg, err := protocol.Encode(protocol.TypeClipboardEvent, evCopy)
	if err != nil {
		return
	}
	if _, ok := e.sendTo(peerID, connected, evCopy, msg); ok {
		e.markDelivered(evCopy.EventID, peerID)
		e.log.Debug("sent latest clipboard event to reconnected peer", "device_id", peerID, "event_id", evCopy.EventID)
	}
}

func (e *Engine) record(ev protocol.ClipboardEvent, dir history.Direction, src history.Source, from string) {
	e.hist.Add(history.Entry{
		EventID:        ev.EventID,
		OriginDeviceID: ev.OriginDeviceID,
		FromDeviceID:   from,
		Direction:      dir,
		Source:         src,
		ContentType:    ev.ContentType,
		Text:           ev.Payload.Text,
		Size:           ev.Payload.Size,
		Timestamp:      time.UnixMilli(ev.Timestamp).UTC(),
		RecordedAt:     e.now().UTC(),
	})
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

// String implements fmt.Stringer for logging.
func (s Stats) String() string {
	return fmt.Sprintf("sent=%d received=%d applied=%d duplicates=%d rejected=%d", s.Sent, s.Received, s.Applied, s.Duplicates, s.Rejected)
}
