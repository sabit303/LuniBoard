package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/coder/websocket"
	"golang.org/x/time/rate"

	"github.com/luniboard/luniboard/daemon/internal/device"
	"github.com/luniboard/luniboard/daemon/pkg/protocol"
)

const (
	statusGoingAway       = websocket.StatusGoingAway
	statusPolicyViolation = websocket.StatusPolicyViolation
	statusProtocolError   = websocket.StatusProtocolError
	// statusDuplicate closes a connection that lost the duplicate-connection tie-break.
	statusDuplicate websocket.StatusCode = 4001
	// statusRateLimited closes a connection that floods messages.
	statusRateLimited websocket.StatusCode = 4029

	maxInvalidMessages = 20
)

type session struct {
	m           *Manager
	conn        *websocket.Conn
	ctx         context.Context
	cancel      context.CancelFunc
	peerID      string
	initiator   string
	outbound    bool
	remoteAddr  string
	connectedAt time.Time

	hello   protocol.Hello
	version int
	offset  int64

	eventLimiter *rate.Limiter
	msgLimiter   *rate.Limiter
	closeOnce    sync.Once
}

func (s *session) info() SessionInfo {
	return SessionInfo{
		DeviceID:        s.peerID,
		RemoteAddr:      s.remoteAddr,
		Outbound:        s.outbound,
		ConnectedAt:     s.connectedAt,
		ProtocolVersion: s.version,
		ClockOffsetMs:   s.offset,
		CanReceive:      s.canReceive(),
		MaxPayloadBytes: s.hello.MaxPayloadBytes,
	}
}

func (s *session) canReceive() bool {
	for _, c := range s.hello.Capabilities {
		if c == protocol.CapabilityReceive {
			return true
		}
	}
	return false
}

func (s *session) write(msg []byte) error {
	ctx, cancel := context.WithTimeout(s.ctx, s.m.opts.WriteTimeout)
	defer cancel()
	return s.conn.Write(ctx, websocket.MessageText, msg)
}

func (s *session) writeError(code, msg string) {
	b, err := protocol.Encode(protocol.TypeError, protocol.ErrorMessage{Code: code, Message: msg})
	if err == nil {
		_ = s.write(b)
	}
}

// close performs the WebSocket close handshake asynchronously so callers
// (e.g. registry change notifications) never block on the network.
func (s *session) close(code websocket.StatusCode, reason string) {
	s.closeOnce.Do(func() {
		go func() {
			_ = s.conn.Close(code, reason)
			s.cancel()
		}()
	})
}

// acquireSessionSlot registers a session goroutine unless the manager is closing.
func (m *Manager) acquireSessionSlot() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.wg.Add(1)
	return true
}

// runSession performs the hello handshake and serves the session until it ends.
// remoteAddr is the TCP peer; dialAddr is the address we dialed (outbound only).
func (m *Manager) runSession(conn *websocket.Conn, d device.Device, remoteAddr string, outbound bool, dialAddr string) {
	if !m.acquireSessionSlot() {
		_ = conn.Close(statusGoingAway, "shutting down")
		return
	}
	defer m.wg.Done()

	conn.SetReadLimit(m.readLimit())
	ctx, cancel := context.WithCancel(m.baseCtx)
	defer cancel()
	s := &session{
		m:            m,
		conn:         conn,
		ctx:          ctx,
		cancel:       cancel,
		peerID:       d.ID,
		outbound:     outbound,
		remoteAddr:   remoteAddr,
		connectedAt:  time.Now().UTC(),
		eventLimiter: rate.NewLimiter(rate.Limit(m.opts.EventsPerSecond), m.opts.EventBurst),
		msgLimiter:   rate.NewLimiter(rate.Limit(10*m.opts.EventsPerSecond+10), 10*m.opts.EventBurst+20),
	}
	if outbound {
		s.initiator = m.opts.Identity.DeviceID
	} else {
		s.initiator = d.ID
	}
	log := m.log.With("device_id", d.ID, "device", d.Name, "remote", remoteAddr, "outbound", outbound)

	if code, err := s.handshake(d); err != nil {
		log.Warn("session handshake failed", "error", err)
		s.writeError(code, err.Error())
		_ = conn.Close(statusPolicyViolation, truncateReason(err.Error()))
		return
	}
	if !m.register(s) {
		log.Debug("duplicate connection rejected")
		_ = conn.Close(statusDuplicate, "duplicate connection")
		return
	}
	defer m.unregister(s)
	// The device may have been unpaired while this session was being set up,
	// after Disconnect already ran; re-check now that the session is visible.
	if cur, ok := m.opts.Devices.Get(d.ID); !ok || cur.Fingerprint != d.Fingerprint {
		log.Info("device was unpaired during connection setup")
		_ = conn.Close(statusPolicyViolation, "device unpaired")
		return
	}

	listenAddr := dialAddr
	if !outbound && s.hello.ListenPort > 0 {
		if host, _, err := net.SplitHostPort(remoteAddr); err == nil {
			listenAddr = net.JoinHostPort(host, strconv.Itoa(s.hello.ListenPort))
		}
	}
	_ = m.opts.Devices.SetInfo(s.hello.Device)
	_ = m.opts.Devices.RecordSeen(d.ID, listenAddr, time.Now())
	log.Info("device connected", "protocol_version", s.version, "clock_offset_ms", s.offset)

	go s.pingLoop()
	go m.handler.OnPeerConnected(d.ID)

	err := s.readLoop()
	_ = m.opts.Devices.RecordSeen(d.ID, "", time.Now())
	status := websocket.CloseStatus(err)
	if status == -1 && ctx.Err() == nil {
		log.Info("device disconnected", "error", err)
	} else {
		log.Info("device disconnected", "status", status)
	}
}

// handshake exchanges hello messages and binds the session to the device identity.
func (s *session) handshake(d device.Device) (string, error) {
	ctx, cancel := context.WithTimeout(s.ctx, s.m.opts.HandshakeTimeout)
	defer cancel()

	msg, err := protocol.Encode(protocol.TypeHello, s.m.hello())
	if err != nil {
		return protocol.ErrCodeInternal, err
	}
	if err := s.conn.Write(ctx, websocket.MessageText, msg); err != nil {
		return protocol.ErrCodeInternal, fmt.Errorf("send hello: %w", err)
	}
	typ, data, err := s.conn.Read(ctx)
	if err != nil {
		return protocol.ErrCodeBadRequest, fmt.Errorf("read hello: %w", err)
	}
	received := time.Now()
	if typ != websocket.MessageText {
		return protocol.ErrCodeBadRequest, errors.New("hello must be a text message")
	}
	env, err := protocol.DecodeEnvelope(data)
	if err != nil {
		return protocol.ErrCodeBadRequest, err
	}
	if env.Type != protocol.TypeHello {
		return protocol.ErrCodeBadRequest, fmt.Errorf("expected hello, got %q", env.Type)
	}
	var h protocol.Hello
	if err := env.DecodeData(&h); err != nil {
		return protocol.ErrCodeBadRequest, err
	}
	if err := protocol.ValidateHello(h); err != nil {
		return protocol.ErrCodeBadRequest, err
	}
	if h.Device.ID != d.ID {
		return protocol.ErrCodeIdentityMismatch, errors.New("device id does not match the paired certificate")
	}
	v, ok := protocol.NegotiateVersion(h)
	if !ok {
		return protocol.ErrCodeUnsupportedVersion, fmt.Errorf("no common protocol version (peer supports %d-%d)", h.MinProtocolVersion, h.MaxProtocolVersion)
	}
	s.hello, s.version = h, v
	s.offset = h.Time - received.UnixMilli()
	return "", nil
}

func (s *session) readLoop() error {
	invalid := 0
	for {
		typ, data, err := s.conn.Read(s.ctx)
		if err != nil {
			return err
		}
		if !s.msgLimiter.Allow() {
			s.close(statusRateLimited, "message rate exceeded")
			return errors.New("message rate exceeded")
		}
		if err := s.dispatch(typ, data); err != nil {
			invalid++
			s.m.log.Debug("invalid message", "device_id", s.peerID, "error", err)
			s.writeError(protocol.ErrCodeBadRequest, truncateReason(err.Error()))
			if invalid >= maxInvalidMessages {
				s.close(statusProtocolError, "too many invalid messages")
				return errors.New("too many invalid messages")
			}
		}
	}
}

func (s *session) dispatch(typ websocket.MessageType, data []byte) error {
	if typ != websocket.MessageText {
		return errors.New("binary messages are not supported on this channel")
	}
	env, err := protocol.DecodeEnvelope(data)
	if err != nil {
		return err
	}
	switch env.Type {
	case protocol.TypeClipboardEvent:
		var ev protocol.ClipboardEvent
		if err := env.DecodeData(&ev); err != nil {
			return err
		}
		var ack protocol.Ack
		if !s.eventLimiter.Allow() {
			ack = protocol.Ack{EventID: ev.EventID, Status: protocol.AckRejected, Reason: protocol.ErrCodeRateLimited}
			if !protocol.ValidID(ev.EventID) {
				ack.EventID = "invalid"
			}
		} else {
			ack = s.m.handler.HandleIncoming(s.ctx, s.peerID, s.offset, ev)
		}
		b, err := protocol.Encode(protocol.TypeAck, ack)
		if err != nil {
			return err
		}
		return s.write(b)
	case protocol.TypeAck:
		var a protocol.Ack
		if err := env.DecodeData(&a); err != nil {
			return err
		}
		s.m.handler.HandleAck(s.peerID, a)
	case protocol.TypeError:
		var e protocol.ErrorMessage
		if err := env.DecodeData(&e); err == nil {
			s.m.log.Warn("peer reported error", "device_id", s.peerID, "code", truncateReason(e.Code), "message", truncateReason(e.Message))
		}
	case protocol.TypeHello:
		return errors.New("unexpected hello after handshake")
	default:
		// Unknown message types are ignored for forward compatibility.
		s.m.log.Debug("ignoring unknown message type", "device_id", s.peerID, "type", truncateReason(string(env.Type)))
	}
	return nil
}

func (s *session) pingLoop() {
	t := time.NewTicker(s.m.opts.PingInterval)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			ctx, cancel := context.WithTimeout(s.ctx, s.m.opts.PingInterval/2+time.Second)
			err := s.conn.Ping(ctx)
			cancel()
			if err != nil {
				s.m.log.Info("peer unresponsive, closing", "device_id", s.peerID, "error", err)
				s.close(statusGoingAway, "ping timeout")
				return
			}
		}
	}
}

func truncateReason(s string) string {
	// WebSocket close reasons are limited to 123 bytes.
	if len(s) > 120 {
		return strings.ToValidUTF8(s[:120], "")
	}
	return s
}
