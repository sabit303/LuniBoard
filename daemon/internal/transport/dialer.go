package transport

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"net"
	"net/http"
	"time"

	"github.com/coder/websocket"

	"github.com/luniboard/luniboard/daemon/internal/device"
	"github.com/luniboard/luniboard/daemon/internal/security"
	"github.com/luniboard/luniboard/daemon/pkg/protocol"
)

type backoffState struct {
	failures int
	next     time.Time
}

// dialLoop keeps outbound sessions to paired devices that listen on the LAN.
// Both ends of a desktop pair dial each other; register() resolves the
// resulting duplicate deterministically. Mobile devices (no addresses) are
// never dialed: they connect to us.
func (m *Manager) dialLoop(ctx context.Context) {
	t := time.NewTicker(m.opts.DialInterval)
	defer t.Stop()
	m.dialAll(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.dialAll(ctx)
		case id := <-m.trigger:
			if d, ok := m.opts.Devices.Get(id); ok {
				m.maybeDial(ctx, d)
			}
		}
	}
}

func (m *Manager) dialAll(ctx context.Context) {
	for _, d := range m.opts.Devices.List() {
		m.maybeDial(ctx, d)
	}
}

func (m *Manager) maybeDial(ctx context.Context, d device.Device) {
	m.mu.Lock()
	if m.closed || m.dialing[d.ID] || m.sessions[d.ID] != nil {
		m.mu.Unlock()
		return
	}
	if b := m.backoff[d.ID]; b != nil && time.Now().Before(b.next) {
		m.mu.Unlock()
		return
	}
	m.mu.Unlock()

	addrs := m.opts.Addresses(d.ID)
	if len(addrs) == 0 {
		return
	}

	m.mu.Lock()
	if m.closed || m.dialing[d.ID] {
		m.mu.Unlock()
		return
	}
	m.dialing[d.ID] = true
	m.wg.Add(1)
	m.mu.Unlock()

	go func() {
		defer m.wg.Done()
		err := m.dial(ctx, d, addrs)
		m.mu.Lock()
		delete(m.dialing, d.ID)
		if err != nil {
			b := m.backoff[d.ID]
			if b == nil {
				b = &backoffState{}
				m.backoff[d.ID] = b
			}
			b.failures++
			b.next = time.Now().Add(m.backoffDelay(b.failures))
		}
		m.mu.Unlock()
		if err != nil && ctx.Err() == nil {
			m.log.Debug("dial failed", "device_id", d.ID, "error", err)
		}
	}()
}

func (m *Manager) backoffDelay(failures int) time.Duration {
	d := time.Second << min(failures, 10)
	if d > m.opts.MaxBackoff {
		d = m.opts.MaxBackoff
	}
	// ±20% jitter avoids synchronized retries from many devices.
	jitter := time.Duration(rand.Int64N(int64(d)/5+1)) - d/10
	return d + jitter
}

// dial tries each address and, on success, serves the session until it ends.
func (m *Manager) dial(ctx context.Context, d device.Device, addrs []string) error {
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:     security.ClientTLSConfig(m.opts.Identity, d.Fingerprint),
			DialContext:         (&net.Dialer{Timeout: 5 * time.Second}).DialContext,
			TLSHandshakeTimeout: m.opts.HandshakeTimeout,
			// LAN traffic must never be sent through an HTTP proxy.
			Proxy: nil,
		},
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}
	defer client.CloseIdleConnections()

	var errs []error
	for _, addr := range addrs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		dctx, cancel := context.WithTimeout(ctx, m.opts.HandshakeTimeout)
		conn, resp, err := websocket.Dial(dctx, "wss://"+addr+protocol.PathWebSocket, &websocket.DialOptions{
			HTTPClient:   client,
			Subprotocols: []string{protocol.WebSocketSubprotocol},
		})
		cancel()
		if err != nil {
			var mismatch *security.FingerprintMismatchError
			switch {
			case errors.As(err, &mismatch):
				m.log.Warn("address answered with a different certificate", "device_id", d.ID, "address", addr)
			case resp != nil && resp.StatusCode == http.StatusUnauthorized:
				m.log.Warn("peer no longer trusts this device; re-pair to reconnect", "device_id", d.ID, "address", addr)
			}
			errs = append(errs, fmt.Errorf("%s: %w", addr, err))
			continue
		}
		m.runSession(conn, d, addr, true, addr)
		return nil
	}
	return errors.Join(errs...)
}
