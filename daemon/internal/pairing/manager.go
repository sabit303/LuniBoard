// Package pairing implements the trust-establishment flow between devices.
//
// A listening device opens a short pairing window with a one-time code. A
// requester proves knowledge of the code over a TLS connection on which both
// certificates are visible, and the local user must explicitly approve the
// request after comparing the verification code shown on both screens. Only
// then is the requester's certificate fingerprint added to the trusted registry.
package pairing

import (
	"context"
	"crypto/subtle"
	"errors"
	"net"
	"strconv"
	"sync"
	"time"

	"github.com/universal-clipboard/universal-clipboard/daemon/internal/device"
	"github.com/universal-clipboard/universal-clipboard/daemon/pkg/protocol"
)

var (
	ErrClosed          = errors.New("pairing window is not open")
	ErrInvalidCode     = errors.New("invalid pairing code")
	ErrBusy            = errors.New("another pairing request is awaiting approval")
	ErrSelf            = errors.New("cannot pair a device with itself")
	ErrRequestNotFound = errors.New("pairing request not found")
	ErrUnsupported     = errors.New("unsupported protocol version")
)

// Registrar stores newly trusted devices.
type Registrar interface {
	Add(device.Device) error
}

// Invitation is the information the user transfers to the other device.
type Invitation struct {
	Code              string    `json:"code"`
	URI               string    `json:"uri"`
	ExpiresAt         time.Time `json:"expires_at"`
	Fingerprint       string    `json:"fingerprint"`
	Addresses         []string  `json:"addresses"`
	AttemptsRemaining int       `json:"attempts_remaining"`
}

// Request is a pending pairing request awaiting a local decision.
type Request struct {
	ID               string              `json:"id"`
	Device           protocol.DeviceInfo `json:"device"`
	Fingerprint      string              `json:"fingerprint"`
	VerificationCode string              `json:"verification_code"`
	RemoteAddr       string              `json:"remote_addr"`
	ReceivedAt       time.Time           `json:"received_at"`
}

// State is a snapshot of the pairing manager for the local API.
type State struct {
	Open       bool        `json:"open"`
	Invitation *Invitation `json:"invitation,omitempty"`
	Pending    []Request   `json:"pending"`
}

// Options configures a Manager.
type Options struct {
	Self        func() protocol.DeviceInfo
	Fingerprint string
	ListenPort  func() int
	// Addresses returns the host:port pairs to embed in invitations.
	Addresses   func() []string
	Registrar   Registrar
	Window      time.Duration
	MaxAttempts int
	Now         func() time.Time
}

type window struct {
	code      string
	expiresAt time.Time
	attempts  int
	timer     *time.Timer
	closed    chan struct{}
}

type pending struct {
	req      Request
	decision chan bool
}

// Manager coordinates pairing windows and pending requests.
type Manager struct {
	opts Options

	mu      sync.Mutex
	win     *window
	pending *pending
}

// NewManager creates a pairing manager.
func NewManager(opts Options) *Manager {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return &Manager{opts: opts}
}

// Start opens (or reopens) a pairing window and returns a fresh invitation.
func (m *Manager) Start() Invitation {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeLocked()
	code := protocol.NewPairingCode()
	w := &window{
		code:      protocol.NormalizePairingCode(code),
		expiresAt: m.opts.Now().Add(m.opts.Window),
		closed:    make(chan struct{}),
	}
	w.timer = time.AfterFunc(m.opts.Window, func() {
		m.mu.Lock()
		defer m.mu.Unlock()
		if m.win == w {
			m.closeLocked()
		}
	})
	m.win = w
	return m.invitationLocked(code)
}

func (m *Manager) invitationLocked(displayCode string) Invitation {
	self := m.opts.Self()
	addrs := m.opts.Addresses()
	return Invitation{
		Code:              displayCode,
		ExpiresAt:         m.win.expiresAt,
		Fingerprint:       m.opts.Fingerprint,
		Addresses:         addrs,
		AttemptsRemaining: m.opts.MaxAttempts - m.win.attempts,
		URI: protocol.PairingInvitation{
			Device:      self,
			Fingerprint: m.opts.Fingerprint,
			Addresses:   addrs,
			Code:        displayCode,
		}.URI(),
	}
}

// Cancel closes the pairing window and rejects any pending request.
func (m *Manager) Cancel() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.closeLocked()
}

func (m *Manager) closeLocked() {
	if m.win == nil {
		return
	}
	m.win.timer.Stop()
	close(m.win.closed)
	m.win = nil
}

// State returns a snapshot for display.
func (m *Manager) State() State {
	m.mu.Lock()
	defer m.mu.Unlock()
	s := State{Pending: []Request{}}
	if m.win != nil {
		s.Open = true
		inv := m.invitationLocked(formatCode(m.win.code))
		s.Invitation = &inv
	}
	if m.pending != nil {
		s.Pending = append(s.Pending, m.pending.req)
	}
	return s
}

func formatCode(normalized string) string {
	if len(normalized) != protocol.PairingCodeLength {
		return normalized
	}
	return normalized[:4] + "-" + normalized[4:]
}

// Decide approves or rejects a pending request.
func (m *Manager) Decide(requestID string, approve bool) error {
	m.mu.Lock()
	p := m.pending
	m.mu.Unlock()
	if p == nil || p.req.ID != requestID {
		return ErrRequestNotFound
	}
	select {
	case p.decision <- approve:
		return nil
	default:
		return ErrRequestNotFound
	}
}

// Submit handles an incoming pairing request. peerFP is the fingerprint of the
// TLS client certificate and remoteAddr the TCP peer address. It blocks until
// the local user decides, the window closes, or ctx is cancelled.
func (m *Manager) Submit(ctx context.Context, req protocol.PairRequest, peerFP, remoteAddr string) (protocol.PairResponse, error) {
	if !protocol.SupportsVersion(req.ProtocolVersion) {
		return protocol.PairResponse{}, ErrUnsupported
	}
	if err := protocol.ValidateDeviceInfo(req.Device); err != nil {
		return protocol.PairResponse{}, err
	}
	if req.ListenPort < 0 || req.ListenPort > 65535 {
		return protocol.PairResponse{}, errors.New("invalid listen_port")
	}
	self := m.opts.Self()
	if peerFP == m.opts.Fingerprint || req.Device.ID == self.ID {
		return protocol.PairResponse{}, ErrSelf
	}

	m.mu.Lock()
	w := m.win
	if w == nil || !m.opts.Now().Before(w.expiresAt) {
		m.mu.Unlock()
		return protocol.PairResponse{}, ErrClosed
	}
	got := protocol.NormalizePairingCode(req.Code)
	if subtle.ConstantTimeCompare([]byte(got), []byte(w.code)) != 1 {
		w.attempts++
		if w.attempts >= m.opts.MaxAttempts {
			m.closeLocked()
		}
		m.mu.Unlock()
		return protocol.PairResponse{}, ErrInvalidCode
	}
	if m.pending != nil {
		m.mu.Unlock()
		return protocol.PairResponse{}, ErrBusy
	}
	p := &pending{
		req: Request{
			ID:               protocol.NewID(),
			Device:           req.Device,
			Fingerprint:      peerFP,
			VerificationCode: protocol.VerificationCode(m.opts.Fingerprint, peerFP),
			RemoteAddr:       remoteAddr,
			ReceivedAt:       m.opts.Now().UTC(),
		},
		decision: make(chan bool),
	}
	m.pending = p
	m.mu.Unlock()

	defer func() {
		m.mu.Lock()
		if m.pending == p {
			m.pending = nil
		}
		m.mu.Unlock()
	}()

	resp := protocol.PairResponse{ProtocolVersion: protocol.Version, Device: self, ListenPort: m.opts.ListenPort()}
	select {
	case <-ctx.Done():
		return protocol.PairResponse{}, ctx.Err()
	case <-w.closed:
		resp.Status = protocol.PairExpired
		return resp, nil
	case approved := <-p.decision:
		if !approved {
			resp.Status = protocol.PairRejected
			return resp, nil
		}
	}

	d := device.Device{
		ID:          req.Device.ID,
		Name:        req.Device.Name,
		Platform:    req.Device.Platform,
		AppVersion:  req.Device.AppVersion,
		Fingerprint: peerFP,
		PairedAt:    m.opts.Now().UTC(),
		Permissions: device.Permissions{Send: true, Receive: true},
	}
	if req.ListenPort > 0 {
		if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
			d.Addresses = []string{net.JoinHostPort(host, strconv.Itoa(req.ListenPort))}
		}
	}
	if err := m.opts.Registrar.Add(d); err != nil {
		return protocol.PairResponse{}, err
	}
	// A window is single use: close it after a successful pairing.
	m.mu.Lock()
	if m.win == w {
		m.closeLocked()
	}
	m.mu.Unlock()
	resp.Status = protocol.PairApproved
	return resp, nil
}
