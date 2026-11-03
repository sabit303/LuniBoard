package transport

import (
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"strings"

	"github.com/coder/websocket"

	"github.com/universal-clipboard/universal-clipboard/daemon/internal/pairing"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/security"
	"github.com/universal-clipboard/universal-clipboard/daemon/pkg/protocol"
)

const maxPairBodyBytes = 16 << 10

func writeError(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(protocol.HTTPError{Error: protocol.ErrorMessage{Code: code, Message: msg}})
}

// rateKey returns the rate-limiting key for a request: the IPv4 address, or
// the /64 prefix for IPv6 so a single host cannot evade limits (or exhaust
// the limiter table) by rotating through addresses in its prefix.
func rateKey(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		host = r.RemoteAddr
	}
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return host
	}
	if v4 := ip.To4(); v4 != nil {
		return v4.String()
	}
	return ip.Mask(net.CIDRMask(64, 128)).String() + "/64"
}

// handlePair accepts pairing requests from devices that are not (yet)
// trusted. It never grants clipboard access by itself: the pairing manager
// requires an open window, the correct code and explicit local approval.
func (m *Manager) handlePair(w http.ResponseWriter, r *http.Request) {
	if !m.pairLimiter.Allow(rateKey(r)) {
		writeError(w, http.StatusTooManyRequests, protocol.ErrCodeRateLimited, "too many pairing attempts")
		return
	}
	fp := security.PeerFingerprint(*r.TLS)
	if fp == "" {
		writeError(w, http.StatusUnauthorized, protocol.ErrCodeUnauthorized, "client certificate required")
		return
	}
	var req protocol.PairRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPairBodyBytes)).Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, protocol.ErrCodeBadRequest, "malformed pairing request")
		return
	}
	m.log.Info("pairing request received", "device", req.Device.Name, "platform", req.Device.Platform, "remote", r.RemoteAddr)
	resp, err := m.opts.Pairer.Submit(r.Context(), req, fp, r.RemoteAddr)
	if err != nil {
		var verr *protocol.ValidationError
		switch {
		case errors.Is(err, pairing.ErrClosed):
			writeError(w, http.StatusForbidden, protocol.ErrCodePairingClosed, "pairing is not enabled on this device")
		case errors.Is(err, pairing.ErrInvalidCode):
			writeError(w, http.StatusForbidden, protocol.ErrCodeInvalidCode, "wrong pairing code")
		case errors.Is(err, pairing.ErrBusy):
			writeError(w, http.StatusTooManyRequests, protocol.ErrCodeRateLimited, err.Error())
		case errors.Is(err, pairing.ErrUnsupported):
			writeError(w, http.StatusBadRequest, protocol.ErrCodeUnsupportedVersion, err.Error())
		case errors.Is(err, pairing.ErrSelf), errors.As(err, &verr):
			writeError(w, http.StatusBadRequest, protocol.ErrCodeBadRequest, err.Error())
		case r.Context().Err() != nil:
			// Client went away; nothing to write.
		default:
			m.log.Error("pairing failed", "error", err)
			writeError(w, http.StatusInternalServerError, protocol.ErrCodeInternal, "pairing failed")
		}
		return
	}
	m.log.Info("pairing request finished", "device", req.Device.Name, "status", resp.Status)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleWebSocket upgrades connections from paired devices only.
func (m *Manager) handleWebSocket(w http.ResponseWriter, r *http.Request) {
	fp := security.PeerFingerprint(*r.TLS)
	d, ok := m.opts.Devices.ByFingerprint(fp)
	if fp == "" || !ok {
		m.log.Warn("rejected connection from unpaired device", "remote", r.RemoteAddr)
		writeError(w, http.StatusUnauthorized, protocol.ErrCodeUnauthorized, "device is not paired")
		return
	}
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{Subprotocols: []string{protocol.WebSocketSubprotocol}})
	if err != nil {
		m.log.Debug("websocket upgrade failed", "remote", r.RemoteAddr, "error", err)
		return
	}
	if conn.Subprotocol() != protocol.WebSocketSubprotocol {
		_ = conn.Close(statusPolicyViolation, "subprotocol "+protocol.WebSocketSubprotocol+" required")
		return
	}
	m.runSession(conn, d, r.RemoteAddr, false, "")
}
