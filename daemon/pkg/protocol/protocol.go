// Package protocol defines the versioned Universal Clipboard wire protocol.
//
// The authoritative human-readable specification lives in protocol/protocol.md
// at the repository root; JSON Schemas live in protocol/schemas/v1. Any change
// to the types in this package must be reflected in both.
package protocol

const (
	// Version is the protocol version spoken by this implementation.
	Version = 1
	// MinSupportedVersion is the oldest protocol version this implementation accepts.
	MinSupportedVersion = 1

	// ServiceType is the DNS-SD service type advertised by listening devices.
	ServiceType = "_uclip._tcp"

	// PairingURIScheme is the URI scheme used in pairing QR codes.
	PairingURIScheme = "uclip"

	// HTTP paths served by a listening device.
	PathPair      = "/v1/pair"
	PathWebSocket = "/v1/ws"

	// WebSocketSubprotocol is negotiated on PathWebSocket.
	WebSocketSubprotocol = "uclip.v1"
)

// Platform identifies the operating system family of a device.
type Platform string

const (
	PlatformLinux   Platform = "linux"
	PlatformWindows Platform = "windows"
	PlatformMacOS   Platform = "macos"
	PlatformAndroid Platform = "android"
	PlatformIOS     Platform = "ios"
	PlatformOther   Platform = "other"
)

// Valid reports whether p is a known platform.
func (p Platform) Valid() bool {
	switch p {
	case PlatformLinux, PlatformWindows, PlatformMacOS, PlatformAndroid, PlatformIOS, PlatformOther:
		return true
	}
	return false
}

// ContentType identifies the kind of clipboard content carried by an event.
type ContentType string

const (
	ContentText ContentType = "text"
	ContentURL  ContentType = "url"
)

// Valid reports whether c is a content type supported by this protocol version.
func (c ContentType) Valid() bool {
	return c == ContentText || c == ContentURL
}

// DeviceInfo describes a device. It is exchanged during pairing and in hello messages.
type DeviceInfo struct {
	ID         string   `json:"id"`
	Name       string   `json:"name"`
	Platform   Platform `json:"platform"`
	AppVersion string   `json:"app_version,omitempty"`
}

// Payload carries the clipboard content and its integrity metadata.
type Payload struct {
	Text     string `json:"text"`
	MimeType string `json:"mime_type"`
	// Size is the length of Text in bytes (UTF-8).
	Size int64 `json:"size"`
	// SHA256 is the lowercase hex SHA-256 digest of Text's UTF-8 bytes.
	SHA256 string `json:"sha256"`
}

// ClipboardEvent is a single clipboard change propagated between devices.
type ClipboardEvent struct {
	EventID        string      `json:"event_id"`
	OriginDeviceID string      `json:"origin_device_id"`
	ContentType    ContentType `json:"content_type"`
	// Timestamp is the origin device's wall clock at creation, in Unix milliseconds.
	Timestamp       int64   `json:"timestamp"`
	ProtocolVersion int     `json:"protocol_version"`
	Payload         Payload `json:"payload"`
}

// MessageType discriminates WebSocket envelopes.
type MessageType string

const (
	TypeHello          MessageType = "hello"
	TypeClipboardEvent MessageType = "clipboard.event"
	TypeAck            MessageType = "clipboard.ack"
	TypeError          MessageType = "error"
)

// Hello is the first message each side sends after the WebSocket is established.
type Hello struct {
	Device             DeviceInfo `json:"device"`
	MinProtocolVersion int        `json:"min_protocol_version"`
	MaxProtocolVersion int        `json:"max_protocol_version"`
	// Time is the sender's wall clock in Unix milliseconds; used to estimate clock offset.
	Time int64 `json:"time"`
	// Capabilities lists optional features, e.g. "clipboard.send", "clipboard.receive".
	Capabilities []string `json:"capabilities,omitempty"`
	// MaxPayloadBytes is the largest payload.text the sender is willing to receive.
	MaxPayloadBytes int64 `json:"max_payload_bytes"`
	// ListenPort is the sender's LAN listener port, or 0 if it does not accept connections.
	ListenPort int `json:"listen_port"`
}

// Capability identifiers used in Hello.Capabilities.
const (
	CapabilitySend    = "clipboard.send"
	CapabilityReceive = "clipboard.receive"
)

// AckStatus is the outcome of processing a clipboard event at the receiver.
type AckStatus string

const (
	AckApplied   AckStatus = "applied"   // written to the receiver's clipboard (or stored, if headless)
	AckDuplicate AckStatus = "duplicate" // event_id already processed
	AckStale     AckStatus = "stale"     // newer clipboard content already present
	AckExpired   AckStatus = "expired"   // older than the receiver's max event age
	AckIgnored   AckStatus = "ignored"   // receiver policy (paused, excluded type, own origin)
	AckRejected  AckStatus = "rejected"  // invalid, too large, not permitted, or rate limited
)

// Ack acknowledges a clipboard event.
type Ack struct {
	EventID string    `json:"event_id"`
	Status  AckStatus `json:"status"`
	Reason  string    `json:"reason,omitempty"`
}

// ErrorMessage reports a protocol-level error before the connection is closed.
type ErrorMessage struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Protocol error codes used in ErrorMessage and HTTP error bodies.
const (
	ErrCodeBadRequest          = "bad_request"
	ErrCodeUnauthorized        = "unauthorized"
	ErrCodeUnsupportedVersion  = "unsupported_version"
	ErrCodePairingClosed       = "pairing_closed"
	ErrCodeInvalidCode         = "invalid_code"
	ErrCodeRateLimited         = "rate_limited"
	ErrCodeTooLarge            = "too_large"
	ErrCodeInternal            = "internal"
	ErrCodeIdentityMismatch    = "identity_mismatch"
	ErrCodeDuplicateConnection = "duplicate_connection"
)

// PairRequest is sent by a device that wants to pair with a listening device.
type PairRequest struct {
	ProtocolVersion int        `json:"protocol_version"`
	Device          DeviceInfo `json:"device"`
	Code            string     `json:"code"`
	// ListenPort is the requester's own listening port, or 0 if it does not accept connections (mobile).
	ListenPort int `json:"listen_port"`
}

// PairStatus is the result of a pairing request.
type PairStatus string

const (
	PairApproved PairStatus = "approved"
	PairRejected PairStatus = "rejected"
	PairExpired  PairStatus = "expired"
)

// PairResponse is returned for a PairRequest once the user has decided.
type PairResponse struct {
	ProtocolVersion int        `json:"protocol_version"`
	Status          PairStatus `json:"status"`
	Device          DeviceInfo `json:"device"`
	ListenPort      int        `json:"listen_port"`
}

// HTTPError is the JSON body of non-2xx HTTP responses.
type HTTPError struct {
	Error ErrorMessage `json:"error"`
}
