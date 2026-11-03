package api

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"time"

	"github.com/universal-clipboard/universal-clipboard/daemon/internal/history"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/pairing"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/synchronization"
)

// ErrDaemonNotRunning means the socket is missing or refuses connections.
var ErrDaemonNotRunning = errors.New("daemon is not running")

// Client talks to the local API.
type Client struct {
	http *http.Client
	path string
}

// NewClient returns a client for the socket at path.
func NewClient(path string) *Client {
	return &Client{
		path: path,
		http: &http.Client{
			Transport: &http.Transport{
				DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
					var d net.Dialer
					return d.DialContext(ctx, "unix", path)
				},
			},
		},
	}
}

// APIError is an error reported by the daemon.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return e.Message }

func (c *Client) do(ctx context.Context, method, path string, body, out any) error {
	var rd io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return err
		}
		rd = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, "http://daemon"+path, rd)
	if err != nil {
		return err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) && opErr.Op == "dial" {
			return fmt.Errorf("%w (socket %s)", ErrDaemonNotRunning, c.path)
		}
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return err
	}
	if resp.StatusCode >= 300 {
		var eb ErrorBody
		if json.Unmarshal(data, &eb) != nil || eb.Error == "" {
			eb.Error = resp.Status
		}
		return &APIError{Status: resp.StatusCode, Message: eb.Error}
	}
	if out == nil {
		return nil
	}
	return json.Unmarshal(data, out)
}

// Ping reports whether the daemon answers within timeout.
func (c *Client) Ping(timeout time.Duration) bool {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	_, err := c.Status(ctx)
	return err == nil
}

func (c *Client) Status(ctx context.Context) (StatusResponse, error) {
	var s StatusResponse
	return s, c.do(ctx, http.MethodGet, "/v1/status", nil, &s)
}

func (c *Client) Devices(ctx context.Context) ([]DeviceView, error) {
	var d []DeviceView
	return d, c.do(ctx, http.MethodGet, "/v1/devices", nil, &d)
}

func (c *Client) UpdateDevice(ctx context.Context, id string, req UpdateDeviceRequest) (DeviceView, error) {
	var d DeviceView
	return d, c.do(ctx, http.MethodPatch, "/v1/devices/"+url.PathEscape(id), req, &d)
}

func (c *Client) RemoveDevice(ctx context.Context, id string) error {
	return c.do(ctx, http.MethodDelete, "/v1/devices/"+url.PathEscape(id), nil, nil)
}

func (c *Client) Discovered(ctx context.Context) ([]DiscoveredView, error) {
	var d []DiscoveredView
	return d, c.do(ctx, http.MethodGet, "/v1/discovered", nil, &d)
}

func (c *Client) StartPairing(ctx context.Context) (pairing.Invitation, error) {
	var inv pairing.Invitation
	return inv, c.do(ctx, http.MethodPost, "/v1/pairing", nil, &inv)
}

func (c *Client) CancelPairing(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, "/v1/pairing", nil, nil)
}

func (c *Client) PairingState(ctx context.Context) (pairing.State, error) {
	var s pairing.State
	return s, c.do(ctx, http.MethodGet, "/v1/pairing", nil, &s)
}

func (c *Client) DecidePairing(ctx context.Context, requestID string, approve bool) error {
	action := "reject"
	if approve {
		action = "approve"
	}
	return c.do(ctx, http.MethodPost, "/v1/pairing/requests/"+url.PathEscape(requestID)+"/"+action, nil, nil)
}

func (c *Client) ConnectPairing(ctx context.Context, uri string) (ConnectResponse, error) {
	var r ConnectResponse
	return r, c.do(ctx, http.MethodPost, "/v1/pairing/connect", ConnectRequest{URI: uri}, &r)
}

func (c *Client) History(ctx context.Context, limit int) ([]history.Entry, error) {
	var h []history.Entry
	return h, c.do(ctx, http.MethodGet, "/v1/history?limit="+strconv.Itoa(limit), nil, &h)
}

func (c *Client) ClearHistory(ctx context.Context) error {
	return c.do(ctx, http.MethodDelete, "/v1/history", nil, nil)
}

func (c *Client) SetPaused(ctx context.Context, paused bool) error {
	p := "/v1/sync/resume"
	if paused {
		p = "/v1/sync/pause"
	}
	return c.do(ctx, http.MethodPost, p, nil, nil)
}

func (c *Client) Send(ctx context.Context, text string, devices []string) (synchronization.SendResult, error) {
	var r synchronization.SendResult
	return r, c.do(ctx, http.MethodPost, "/v1/send", SendRequest{Text: text, Devices: devices}, &r)
}

func (c *Client) ConfigTOML(ctx context.Context) (string, error) {
	var r map[string]string
	err := c.do(ctx, http.MethodGet, "/v1/config", nil, &r)
	return r["toml"], err
}

func (c *Client) Shutdown(ctx context.Context) error {
	return c.do(ctx, http.MethodPost, "/v1/shutdown", nil, nil)
}
