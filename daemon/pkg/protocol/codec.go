package protocol

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"
)

// Envelope wraps every WebSocket message.
type Envelope struct {
	Type            MessageType     `json:"type"`
	ProtocolVersion int             `json:"protocol_version"`
	Data            json.RawMessage `json:"data"`
}

// Encode marshals v into an envelope of the given type.
func Encode(t MessageType, v any) ([]byte, error) {
	data, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode %s: %w", t, err)
	}
	return json.Marshal(Envelope{Type: t, ProtocolVersion: Version, Data: data})
}

// DecodeEnvelope parses an envelope. Unknown fields are ignored for forward compatibility.
func DecodeEnvelope(b []byte) (Envelope, error) {
	var env Envelope
	if err := json.Unmarshal(b, &env); err != nil {
		return Envelope{}, invalid("envelope", "malformed JSON")
	}
	if env.Type == "" {
		return Envelope{}, invalid("type", "missing")
	}
	if !SupportsVersion(env.ProtocolVersion) {
		return Envelope{}, invalid("protocol_version", fmt.Sprintf("unsupported version %d", env.ProtocolVersion))
	}
	if len(env.Data) == 0 {
		return Envelope{}, invalid("data", "missing")
	}
	return env, nil
}

// DecodeData unmarshals the envelope data into v.
func (e Envelope) DecodeData(v any) error {
	if err := json.Unmarshal(e.Data, v); err != nil {
		return invalid("data", "malformed "+string(e.Type))
	}
	return nil
}

// NewID returns a random RFC 4122 version 4 UUID string, suitable for event IDs.
func NewID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	h := hex.EncodeToString(b[:])
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:32]
}

// CertificateFingerprint returns the lowercase hex SHA-256 digest of a DER-encoded certificate.
// It is the identity that pairing pins.
func CertificateFingerprint(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// ValidFingerprint reports whether s looks like a CertificateFingerprint value.
func ValidFingerprint(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

// VerificationCode derives the 6-digit short authentication string both
// sides display during pairing. It is symmetric in its arguments.
func VerificationCode(fingerprintA, fingerprintB string) string {
	a, b := fingerprintA, fingerprintB
	if b < a {
		a, b = b, a
	}
	sum := sha256.Sum256([]byte("uclip-sas-v1:" + a + ":" + b))
	n := binary.BigEndian.Uint32(sum[:4]) % 1_000_000
	s := fmt.Sprintf("%06d", n)
	return s[:3] + " " + s[3:]
}

// PairingCodeAlphabet excludes visually ambiguous characters (0/O, 1/I).
const PairingCodeAlphabet = "23456789ABCDEFGHJKLMNPQRSTUVWXYZ"

// PairingCodeLength is the number of significant characters in a pairing code (40 bits).
const PairingCodeLength = 8

// NewPairingCode returns a random pairing code formatted as XXXX-XXXX.
func NewPairingCode() string {
	var b [PairingCodeLength]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(fmt.Sprintf("crypto/rand failed: %v", err))
	}
	out := make([]byte, 0, PairingCodeLength+1)
	for i, v := range b {
		if i == PairingCodeLength/2 {
			out = append(out, '-')
		}
		out = append(out, PairingCodeAlphabet[int(v)%len(PairingCodeAlphabet)])
	}
	return string(out)
}

// NormalizePairingCode uppercases a user-entered code and strips separators.
func NormalizePairingCode(s string) string {
	var sb strings.Builder
	for _, r := range strings.ToUpper(s) {
		if r == '-' || r == ' ' {
			continue
		}
		sb.WriteRune(r)
	}
	return sb.String()
}

// PairingInvitation is the information encoded in a pairing QR code.
type PairingInvitation struct {
	Device      DeviceInfo
	Fingerprint string
	// Addresses are host:port pairs at which the inviting device listens.
	Addresses []string
	Code      string
}

// URI encodes the invitation as uclip://pair?...
func (p PairingInvitation) URI() string {
	q := url.Values{}
	q.Set("v", strconv.Itoa(Version))
	q.Set("id", p.Device.ID)
	q.Set("name", p.Device.Name)
	q.Set("os", string(p.Device.Platform))
	q.Set("fp", p.Fingerprint)
	q.Set("code", p.Code)
	for _, a := range p.Addresses {
		q.Add("addr", a)
	}
	return PairingURIScheme + "://pair?" + q.Encode()
}

// ParsePairingURI decodes and validates a pairing URI.
func ParsePairingURI(s string) (PairingInvitation, error) {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil || u.Scheme != PairingURIScheme || u.Host != "pair" {
		return PairingInvitation{}, errors.New("not a uclip://pair URI")
	}
	q := u.Query()
	v, err := strconv.Atoi(q.Get("v"))
	if err != nil || !SupportsVersion(v) {
		return PairingInvitation{}, fmt.Errorf("unsupported pairing URI version %q", q.Get("v"))
	}
	inv := PairingInvitation{
		Device: DeviceInfo{
			ID:       q.Get("id"),
			Name:     q.Get("name"),
			Platform: Platform(q.Get("os")),
		},
		Fingerprint: strings.ToLower(q.Get("fp")),
		Code:        q.Get("code"),
		Addresses:   q["addr"],
	}
	if err := ValidateDeviceInfo(inv.Device); err != nil {
		return PairingInvitation{}, err
	}
	if !ValidFingerprint(inv.Fingerprint) {
		return PairingInvitation{}, invalid("fp", "must be 64 lowercase hex characters")
	}
	if len(NormalizePairingCode(inv.Code)) != PairingCodeLength {
		return PairingInvitation{}, invalid("code", "wrong length")
	}
	if len(inv.Addresses) == 0 || len(inv.Addresses) > 16 {
		return PairingInvitation{}, invalid("addr", "must contain 1-16 addresses")
	}
	for _, a := range inv.Addresses {
		if _, port, err := net.SplitHostPort(a); err != nil || port == "" {
			return PairingInvitation{}, invalid("addr", fmt.Sprintf("%q is not host:port", a))
		}
	}
	return inv, nil
}
