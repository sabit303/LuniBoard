package protocol

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"unicode"
	"unicode/utf8"
)

// Limits shared by all implementations of protocol version 1.
const (
	MaxIDLength         = 64
	MaxDeviceNameRunes  = 64
	MaxAppVersionLength = 32
	MaxMimeTypeLength   = 128
	MaxReasonLength     = 256
	// HardMaxPayloadBytes is an absolute upper bound for payload.text regardless of configuration.
	HardMaxPayloadBytes = 16 << 20
	// EnvelopeOverheadBytes is headroom on top of the payload limit for JSON framing.
	EnvelopeOverheadBytes = 16 << 10
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)

// ValidationError describes why a message failed validation.
type ValidationError struct {
	Field  string
	Reason string
}

func (e *ValidationError) Error() string { return fmt.Sprintf("invalid %s: %s", e.Field, e.Reason) }

func invalid(field, reason string) error { return &ValidationError{Field: field, Reason: reason} }

// ErrTooLarge is returned when a payload exceeds the configured size limit.
var ErrTooLarge = errors.New("payload exceeds size limit")

// ValidID reports whether s is a syntactically valid device or event identifier.
func ValidID(s string) bool { return idPattern.MatchString(s) }

// ValidateDeviceInfo checks a DeviceInfo received from a peer.
func ValidateDeviceInfo(d DeviceInfo) error {
	if !ValidID(d.ID) {
		return invalid("device.id", "must match [A-Za-z0-9._-]{1,64}")
	}
	if err := validateDisplayString(d.Name, MaxDeviceNameRunes); err != nil {
		return invalid("device.name", err.Error())
	}
	if !d.Platform.Valid() {
		return invalid("device.platform", "unknown platform")
	}
	if len(d.AppVersion) > MaxAppVersionLength {
		return invalid("device.app_version", "too long")
	}
	return nil
}

func validateDisplayString(s string, maxRunes int) error {
	if s == "" {
		return errors.New("must not be empty")
	}
	if !utf8.ValidString(s) {
		return errors.New("must be valid UTF-8")
	}
	if utf8.RuneCountInString(s) > maxRunes {
		return fmt.Errorf("must be at most %d characters", maxRunes)
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return errors.New("must not contain control characters")
		}
	}
	return nil
}

// SupportsVersion reports whether this implementation can process messages of version v.
func SupportsVersion(v int) bool { return v >= MinSupportedVersion && v <= Version }

// ValidateEvent checks structural validity, integrity and size of a clipboard event.
// maxPayloadBytes is the receiver's configured limit (capped by HardMaxPayloadBytes).
func ValidateEvent(ev ClipboardEvent, maxPayloadBytes int64) error {
	if !SupportsVersion(ev.ProtocolVersion) {
		return invalid("protocol_version", fmt.Sprintf("unsupported version %d", ev.ProtocolVersion))
	}
	if !ValidID(ev.EventID) {
		return invalid("event_id", "must match [A-Za-z0-9._-]{1,64}")
	}
	if !ValidID(ev.OriginDeviceID) {
		return invalid("origin_device_id", "must match [A-Za-z0-9._-]{1,64}")
	}
	if !ev.ContentType.Valid() {
		return invalid("content_type", fmt.Sprintf("unsupported content type %q", ev.ContentType))
	}
	if ev.Timestamp <= 0 {
		return invalid("timestamp", "must be positive Unix milliseconds")
	}
	if maxPayloadBytes <= 0 || maxPayloadBytes > HardMaxPayloadBytes {
		maxPayloadBytes = HardMaxPayloadBytes
	}
	p := ev.Payload
	if int64(len(p.Text)) > maxPayloadBytes || p.Size > maxPayloadBytes {
		return ErrTooLarge
	}
	if p.Size != int64(len(p.Text)) {
		return invalid("payload.size", "does not match text length")
	}
	if p.Text == "" {
		return invalid("payload.text", "must not be empty")
	}
	if !utf8.ValidString(p.Text) {
		return invalid("payload.text", "must be valid UTF-8")
	}
	if p.MimeType == "" || len(p.MimeType) > MaxMimeTypeLength || !strings.HasPrefix(p.MimeType, "text/") {
		return invalid("payload.mime_type", "must be a text/* MIME type")
	}
	if p.SHA256 != HashText(p.Text) {
		return invalid("payload.sha256", "digest mismatch")
	}
	if ev.ContentType == ContentURL && ClassifyText(p.Text) != ContentURL {
		return invalid("payload.text", "content_type is url but text is not an http(s) URL")
	}
	return nil
}

// ValidateAck checks an acknowledgement received from a peer.
func ValidateAck(a Ack) error {
	if !ValidID(a.EventID) {
		return invalid("event_id", "must match [A-Za-z0-9._-]{1,64}")
	}
	switch a.Status {
	case AckApplied, AckDuplicate, AckStale, AckExpired, AckIgnored, AckRejected:
	default:
		return invalid("status", "unknown status")
	}
	if len(a.Reason) > MaxReasonLength {
		return invalid("reason", "too long")
	}
	return nil
}

// ValidateHello checks a hello message received from a peer.
func ValidateHello(h Hello) error {
	if err := ValidateDeviceInfo(h.Device); err != nil {
		return err
	}
	if h.MinProtocolVersion <= 0 || h.MaxProtocolVersion < h.MinProtocolVersion {
		return invalid("protocol_version", "invalid version range")
	}
	if h.Time <= 0 {
		return invalid("time", "must be positive Unix milliseconds")
	}
	if len(h.Capabilities) > 32 {
		return invalid("capabilities", "too many entries")
	}
	if h.MaxPayloadBytes < 0 {
		return invalid("max_payload_bytes", "must not be negative")
	}
	if h.ListenPort < 0 || h.ListenPort > 65535 {
		return invalid("listen_port", "must be 0-65535")
	}
	return nil
}

// NegotiateVersion returns the highest protocol version supported by both sides.
func NegotiateVersion(h Hello) (int, bool) {
	hi := min(h.MaxProtocolVersion, Version)
	lo := max(h.MinProtocolVersion, MinSupportedVersion)
	if hi < lo {
		return 0, false
	}
	return hi, true
}

// HashText returns the lowercase hex SHA-256 digest of text.
func HashText(text string) string {
	sum := sha256.Sum256([]byte(text))
	return hex.EncodeToString(sum[:])
}

// ClassifyText returns ContentURL when text is a single absolute http(s) URL, otherwise ContentText.
func ClassifyText(text string) ContentType {
	t := strings.TrimSpace(text)
	if t == "" || len(t) > 8192 || strings.ContainsFunc(t, unicode.IsSpace) {
		return ContentText
	}
	u, err := url.Parse(t)
	if err != nil || u.Host == "" {
		return ContentText
	}
	if s := strings.ToLower(u.Scheme); s == "http" || s == "https" {
		return ContentURL
	}
	return ContentText
}

// MimeTypeFor returns the canonical MIME type for a content type.
func MimeTypeFor(c ContentType) string {
	if c == ContentURL {
		return "text/uri-list"
	}
	return "text/plain"
}

// NewTextEvent builds a clipboard event for text content. The content type is
// derived from the text.
func NewTextEvent(eventID, originID, text string, timestampMillis int64) ClipboardEvent {
	ct := ClassifyText(text)
	return ClipboardEvent{
		EventID:         eventID,
		OriginDeviceID:  originID,
		ContentType:     ct,
		Timestamp:       timestampMillis,
		ProtocolVersion: Version,
		Payload: Payload{
			Text:     text,
			MimeType: MimeTypeFor(ct),
			Size:     int64(len(text)),
			SHA256:   HashText(text),
		},
	}
}
