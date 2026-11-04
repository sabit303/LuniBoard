// Package api implements the daemon's local control API: HTTP/JSON over a
// Unix domain socket that only the owning user can access. The CLI and any
// future GUI use it; it never exposes clipboard sync logic of its own.
//
// The API is documented in docs/local-api.md.
package api

import (
	"errors"
	"time"

	"github.com/luniboard/luniboard/daemon/internal/clipboard"
	"github.com/luniboard/luniboard/daemon/internal/device"
	"github.com/luniboard/luniboard/daemon/internal/discovery"
	"github.com/luniboard/luniboard/daemon/internal/synchronization"
	"github.com/luniboard/luniboard/daemon/internal/transport"
	"github.com/luniboard/luniboard/daemon/pkg/protocol"
)

// Errors that backends wrap to select HTTP status codes.
var (
	ErrNotFound   = errors.New("not found")
	ErrBadRequest = errors.New("bad request")
	ErrConflict   = errors.New("conflict")
)

// StatusResponse is returned by GET /v1/status.
type StatusResponse struct {
	Version          string                `json:"version"`
	Device           protocol.DeviceInfo   `json:"device"`
	Fingerprint      string                `json:"fingerprint"`
	ListenPort       int                   `json:"listen_port"`
	Addresses        []string              `json:"addresses"`
	StartedAt        time.Time             `json:"started_at"`
	Paused           bool                  `json:"paused"`
	Clipboard        clipboard.Status      `json:"clipboard"`
	DiscoveryEnabled bool                  `json:"discovery_enabled"`
	PairingOpen      bool                  `json:"pairing_open"`
	PairedDevices    int                   `json:"paired_devices"`
	ConnectedDevices int                   `json:"connected_devices"`
	Stats            synchronization.Stats `json:"stats"`
	ConfigFile       string                `json:"config_file"`
	LogFile          string                `json:"log_file"`
}

// DeviceView is a paired device plus its live connection state.
type DeviceView struct {
	device.Device
	Connected bool                   `json:"connected"`
	Session   *transport.SessionInfo `json:"session,omitempty"`
}

// UpdateDeviceRequest is the body of PATCH /v1/devices/{id}. Nil fields are unchanged.
type UpdateDeviceRequest struct {
	Send    *bool `json:"send,omitempty"`
	Receive *bool `json:"receive,omitempty"`
}

// DiscoveredView is a device seen on the network.
type DiscoveredView struct {
	discovery.Peer
	Paired bool `json:"paired"`
}

// ConnectRequest is the body of POST /v1/pairing/connect.
type ConnectRequest struct {
	URI string `json:"uri"`
}

// ConnectResponse reports the result of pairing with another device.
type ConnectResponse struct {
	Status           protocol.PairStatus `json:"status"`
	Device           *DeviceView         `json:"device,omitempty"`
	VerificationCode string              `json:"verification_code"`
}

// SendRequest is the body of POST /v1/send.
type SendRequest struct {
	Text    string   `json:"text"`
	Devices []string `json:"devices,omitempty"`
}

// ErrorBody is the JSON body of error responses.
type ErrorBody struct {
	Error string `json:"error"`
}
