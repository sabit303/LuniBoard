// Package transport implements the authenticated LAN transport: an HTTPS
// listener with mutual TLS, the pairing endpoint, WebSocket sessions with
// paired devices, and the outbound reconnect loop.
package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"sync"
	"time"

	"github.com/luniboard/luniboard/daemon/internal/device"
	"github.com/luniboard/luniboard/daemon/internal/security"
	"github.com/luniboard/luniboard/daemon/pkg/protocol"
)

// Handler receives protocol messages from sessions.
type Handler interface {
	HandleIncoming(ctx context.Context, fromPeer string, clockOffsetMillis int64, ev protocol.ClipboardEvent) protocol.Ack
	HandleAck(fromPeer string, a protocol.Ack)
	OnPeerConnected(peerID string)
}

// Devices is the subset of the device registry used by the transport.
type Devices interface {
	Get(id string) (device.Device, bool)
	ByFingerprint(fp string) (device.Device, bool)
	List() []device.Device
	RecordSeen(id, listenAddr string, t time.Time) error
	SetInfo(info protocol.DeviceInfo) error
}

// Pairer handles pairing requests.
type Pairer interface {
	Submit(ctx context.Context, req protocol.PairRequest, peerFP, remoteAddr string) (protocol.PairResponse, error)
}

// AddressSource returns candidate host:port addresses for a paired device,
// best first (e.g. fresh mDNS results before remembered addresses).
type AddressSource func(deviceID string) []string

// Options configures a Manager.
type Options struct {
	Identity        *security.Identity
	Self            func() protocol.DeviceInfo
	ListenAddress   string
	Port            int
	MaxConnections  int
	MaxPayloadBytes int64
	EventsPerSecond float64
	EventBurst      int
	Devices         Devices
	Pairer          Pairer
	Addresses       AddressSource
	Logger          *slog.Logger

	// Timings; zero values select production defaults.
	HandshakeTimeout time.Duration
	PingInterval     time.Duration
	WriteTimeout     time.Duration
	DialInterval     time.Duration
	MaxBackoff       time.Duration
}

func (o *Options) defaults() {
	if o.HandshakeTimeout == 0 {
		o.HandshakeTimeout = 10 * time.Second
	}
	if o.PingInterval == 0 {
		o.PingInterval = 30 * time.Second
	}
	if o.WriteTimeout == 0 {
		o.WriteTimeout = 10 * time.Second
	}
	if o.DialInterval == 0 {
		o.DialInterval = 5 * time.Second
	}
	if o.MaxBackoff == 0 {
		o.MaxBackoff = time.Minute
	}
	if o.MaxConnections == 0 {
		o.MaxConnections = 64
	}
	if o.EventsPerSecond == 0 {
		o.EventsPerSecond = 5
	}
	if o.EventBurst == 0 {
		o.EventBurst = 20
	}
	if o.Logger == nil {
		o.Logger = slog.Default()
	}
	if o.Addresses == nil {
		o.Addresses = func(string) []string { return nil }
	}
}

// Manager owns the listener, all peer sessions and the dialer.
type Manager struct {
	opts    Options
	log     *slog.Logger
	handler Handler

	pairLimiter *security.KeyedLimiter

	ln       net.Listener
	srv      *http.Server
	baseCtx  context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup
	trigger  chan string
	mu       sync.Mutex
	sessions map[string]*session
	dialing  map[string]bool
	backoff  map[string]*backoffState
	closed   bool
}

// New creates a transport manager. SetHandler must be called before Start.
func New(opts Options) *Manager {
	opts.defaults()
	ctx, cancel := context.WithCancel(context.Background())
	return &Manager{
		opts:        opts,
		log:         opts.Logger,
		pairLimiter: security.NewKeyedLimiter(0.5, 5, 1024),
		baseCtx:     ctx,
		cancel:      cancel,
		trigger:     make(chan string, 16),
		sessions:    make(map[string]*session),
		dialing:     make(map[string]bool),
		backoff:     make(map[string]*backoffState),
	}
}

// SetHandler sets the receiver of protocol messages.
func (m *Manager) SetHandler(h Handler) { m.handler = h }

// Listen binds the LAN listener.
func (m *Manager) Listen() error {
	addr := net.JoinHostPort(m.opts.ListenAddress, strconv.Itoa(m.opts.Port))
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", addr, err)
	}
	m.ln = newLimitListener(ln, m.opts.MaxConnections)
	return nil
}

// Port returns the bound TCP port (useful when configured with port 0).
func (m *Manager) Port() int {
	if m.ln == nil {
		return 0
	}
	return m.ln.Addr().(*net.TCPAddr).Port
}

// Start serves the listener and runs the dialer until Close is called.
func (m *Manager) Start() error {
	if m.handler == nil {
		return errors.New("transport: handler not set")
	}
	if m.ln == nil {
		if err := m.Listen(); err != nil {
			return err
		}
	}
	mux := http.NewServeMux()
	mux.HandleFunc("POST "+protocol.PathPair, m.handlePair)
	mux.HandleFunc("GET "+protocol.PathWebSocket, m.handleWebSocket)
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		writeError(w, http.StatusNotFound, protocol.ErrCodeBadRequest, "not found")
	})
	m.srv = &http.Server{
		Handler:           mux,
		TLSConfig:         security.ServerTLSConfig(m.opts.Identity),
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    16 << 10,
		ErrorLog:          slog.NewLogLogger(m.log.Handler(), slog.LevelDebug),
		BaseContext:       func(net.Listener) context.Context { return m.baseCtx },
	}
	tlsLn := tls.NewListener(m.ln, m.srv.TLSConfig)
	m.wg.Add(2)
	go func() {
		defer m.wg.Done()
		if err := m.srv.Serve(tlsLn); err != nil && !errors.Is(err, http.ErrServerClosed) {
			m.log.Error("LAN listener stopped", "error", err)
		}
	}()
	go func() {
		defer m.wg.Done()
		m.dialLoop(m.baseCtx)
	}()
	m.log.Info("LAN listener started", "address", m.ln.Addr().String(), "fingerprint", m.opts.Identity.Fingerprint)
	return nil
}

// Close stops the listener, closes every session and waits for goroutines.
func (m *Manager) Close() error {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return nil
	}
	m.closed = true
	sessions := make([]*session, 0, len(m.sessions))
	for _, s := range m.sessions {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	m.cancel()
	for _, s := range sessions {
		s.close(statusGoingAway, "shutting down")
	}
	var err error
	if m.srv != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		err = m.srv.Shutdown(ctx)
		cancel()
	} else if m.ln != nil {
		err = m.ln.Close()
	}
	m.wg.Wait()
	return err
}

// SessionInfo describes a live session for status output.
type SessionInfo struct {
	DeviceID        string    `json:"device_id"`
	RemoteAddr      string    `json:"remote_addr"`
	Outbound        bool      `json:"outbound"`
	ConnectedAt     time.Time `json:"connected_at"`
	ProtocolVersion int       `json:"protocol_version"`
	ClockOffsetMs   int64     `json:"clock_offset_ms"`
	CanReceive      bool      `json:"can_receive"`
	MaxPayloadBytes int64     `json:"max_payload_bytes"`
}

// Sessions returns all live sessions.
func (m *Manager) Sessions() []SessionInfo {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]SessionInfo, 0, len(m.sessions))
	for _, s := range m.sessions {
		out = append(out, s.info())
	}
	return out
}

// Send writes an encoded envelope to a connected peer.
func (m *Manager) Send(peerID string, msg []byte) error {
	m.mu.Lock()
	s := m.sessions[peerID]
	m.mu.Unlock()
	if s == nil {
		return errors.New("peer not connected")
	}
	return s.write(msg)
}

// Disconnect closes the session with a peer, e.g. after revocation.
func (m *Manager) Disconnect(peerID, reason string) {
	m.mu.Lock()
	s := m.sessions[peerID]
	delete(m.backoff, peerID)
	m.mu.Unlock()
	if s != nil {
		s.close(statusPolicyViolation, reason)
	}
}

// Trigger asks the dialer to try connecting to a device immediately.
func (m *Manager) Trigger(deviceID string) {
	m.mu.Lock()
	if b := m.backoff[deviceID]; b != nil {
		b.next = time.Time{}
	}
	m.mu.Unlock()
	select {
	case m.trigger <- deviceID:
	default:
	}
}

// register installs a handshaken session, resolving duplicate connections
// deterministically: when two sessions exist for the same peer, the one
// initiated by the device with the lexicographically smaller ID wins; a new
// session from the same initiator replaces the old one.
func (m *Manager) register(s *session) bool {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		return false
	}
	old := m.sessions[s.peerID]
	keep := true
	if old != nil && old.initiator != s.initiator {
		preferred := min(m.opts.Identity.DeviceID, s.peerID)
		keep = s.initiator == preferred
	}
	if keep {
		m.sessions[s.peerID] = s
		delete(m.backoff, s.peerID)
	}
	m.mu.Unlock()
	if !keep {
		return false
	}
	if old != nil {
		old.close(statusDuplicate, "replaced by a newer connection")
	}
	return true
}

func (m *Manager) unregister(s *session) {
	m.mu.Lock()
	if m.sessions[s.peerID] == s {
		delete(m.sessions, s.peerID)
	}
	m.mu.Unlock()
}

func (m *Manager) isConnected(id string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	_, ok := m.sessions[id]
	return ok
}

func (m *Manager) hello() protocol.Hello {
	return protocol.Hello{
		Device:             m.opts.Self(),
		MinProtocolVersion: protocol.MinSupportedVersion,
		MaxProtocolVersion: protocol.Version,
		Time:               time.Now().UnixMilli(),
		Capabilities:       []string{protocol.CapabilitySend, protocol.CapabilityReceive},
		MaxPayloadBytes:    m.opts.MaxPayloadBytes,
		ListenPort:         m.Port(),
	}
}

// readLimit bounds a single WebSocket message. JSON escaping can expand text
// up to six times (\u00XX), so the limit is derived from the payload limit.
func (m *Manager) readLimit() int64 {
	return 6*m.opts.MaxPayloadBytes + protocol.EnvelopeOverheadBytes
}

// limitListener bounds the number of simultaneously accepted connections.
type limitListener struct {
	net.Listener
	sem chan struct{}
}

func newLimitListener(l net.Listener, n int) net.Listener {
	return &limitListener{Listener: l, sem: make(chan struct{}, n)}
}

func (l *limitListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		select {
		case l.sem <- struct{}{}:
			return &limitConn{Conn: c, release: func() { <-l.sem }}, nil
		default:
			// Over the limit: refuse instead of queueing to keep resource use bounded.
			_ = c.Close()
		}
	}
}

type limitConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *limitConn) Close() error {
	err := c.Conn.Close()
	c.once.Do(c.release)
	return err
}
