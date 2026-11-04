package protocol

import (
	"errors"
	"strings"
	"testing"
)

func validEvent() ClipboardEvent {
	return NewTextEvent(NewID(), "linux-desktop-1", "Hello from Android", 1793730000000)
}

func TestNewTextEventIsValid(t *testing.T) {
	ev := validEvent()
	if err := ValidateEvent(ev, 1024); err != nil {
		t.Fatalf("expected valid event, got %v", err)
	}
	if ev.ContentType != ContentText || ev.Payload.MimeType != "text/plain" {
		t.Fatalf("unexpected classification: %+v", ev)
	}
}

func TestClassifyText(t *testing.T) {
	cases := map[string]ContentType{
		"https://example.com/a?b=c": ContentURL,
		"  http://example.com  ":    ContentURL,
		"HTTPS://EXAMPLE.COM":       ContentURL,
		"example.com":               ContentText,
		"ftp://example.com":         ContentText,
		"see https://example.com":   ContentText,
		"javascript:alert(1)":       ContentText,
		"https://":                  ContentText,
		"":                          ContentText,
	}
	for in, want := range cases {
		if got := ClassifyText(in); got != want {
			t.Errorf("ClassifyText(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestValidateEventRejects(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*ClipboardEvent)
	}{
		{"bad version", func(e *ClipboardEvent) { e.ProtocolVersion = 99 }},
		{"bad event id", func(e *ClipboardEvent) { e.EventID = "has space" }},
		{"long event id", func(e *ClipboardEvent) { e.EventID = strings.Repeat("a", 65) }},
		{"bad origin", func(e *ClipboardEvent) { e.OriginDeviceID = "" }},
		{"bad content type", func(e *ClipboardEvent) { e.ContentType = "image" }},
		{"zero timestamp", func(e *ClipboardEvent) { e.Timestamp = 0 }},
		{"size mismatch", func(e *ClipboardEvent) { e.Payload.Size = 1 }},
		{"hash mismatch", func(e *ClipboardEvent) { e.Payload.SHA256 = HashText("other") }},
		{"empty text", func(e *ClipboardEvent) {
			e.Payload.Text, e.Payload.Size, e.Payload.SHA256 = "", 0, HashText("")
		}},
		{"invalid utf8", func(e *ClipboardEvent) {
			e.Payload.Text = "\xff\xfe"
			e.Payload.Size, e.Payload.SHA256 = 2, HashText("\xff\xfe")
		}},
		{"non-text mime", func(e *ClipboardEvent) { e.Payload.MimeType = "image/png" }},
		{"url mismatch", func(e *ClipboardEvent) { e.ContentType = ContentURL }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ev := validEvent()
			tc.mutate(&ev)
			if err := ValidateEvent(ev, 1024); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestValidateEventTooLarge(t *testing.T) {
	ev := NewTextEvent(NewID(), "dev", strings.Repeat("x", 2048), 1)
	if err := ValidateEvent(ev, 1024); !errors.Is(err, ErrTooLarge) {
		t.Fatalf("expected ErrTooLarge, got %v", err)
	}
}

func TestEnvelopeRoundTrip(t *testing.T) {
	ev := validEvent()
	b, err := Encode(TypeClipboardEvent, ev)
	if err != nil {
		t.Fatal(err)
	}
	env, err := DecodeEnvelope(b)
	if err != nil {
		t.Fatal(err)
	}
	if env.Type != TypeClipboardEvent {
		t.Fatalf("type = %q", env.Type)
	}
	var got ClipboardEvent
	if err := env.DecodeData(&got); err != nil {
		t.Fatal(err)
	}
	if got != ev {
		t.Fatalf("round trip mismatch: %+v != %+v", got, ev)
	}
}

func TestDecodeEnvelopeRejectsGarbage(t *testing.T) {
	for _, in := range []string{
		``, `{`, `[]`, `{"type":"hello"}`,
		`{"type":"hello","protocol_version":2,"data":{}}`,
		`{"protocol_version":1,"data":{}}`,
	} {
		if _, err := DecodeEnvelope([]byte(in)); err == nil {
			t.Errorf("DecodeEnvelope(%q) succeeded", in)
		}
	}
}

func TestNegotiateVersion(t *testing.T) {
	if v, ok := NegotiateVersion(Hello{MinProtocolVersion: 1, MaxProtocolVersion: 5}); !ok || v != Version {
		t.Fatalf("got %d %v", v, ok)
	}
	if _, ok := NegotiateVersion(Hello{MinProtocolVersion: 2, MaxProtocolVersion: 3}); ok {
		t.Fatal("expected no common version")
	}
}

func TestVerificationCodeSymmetric(t *testing.T) {
	a := CertificateFingerprint([]byte("a"))
	b := CertificateFingerprint([]byte("b"))
	if VerificationCode(a, b) != VerificationCode(b, a) {
		t.Fatal("verification code not symmetric")
	}
	if len(VerificationCode(a, b)) != 7 {
		t.Fatalf("unexpected format %q", VerificationCode(a, b))
	}
	if VerificationCode(a, b) == VerificationCode(a, a) {
		t.Fatal("expected different codes for different pairs")
	}
}

func TestPairingCode(t *testing.T) {
	c := NewPairingCode()
	n := NormalizePairingCode(c)
	if len(n) != PairingCodeLength {
		t.Fatalf("code %q normalized to %q", c, n)
	}
	for _, r := range n {
		if !strings.ContainsRune(PairingCodeAlphabet, r) {
			t.Fatalf("unexpected rune %q", r)
		}
	}
	if NormalizePairingCode("ab cd-ef gh") != "ABCDEFGH" {
		t.Fatal("normalization failed")
	}
}

func TestPairingURIRoundTrip(t *testing.T) {
	inv := PairingInvitation{
		Device:      DeviceInfo{ID: "dev-1", Name: "My Laptop & Co", Platform: PlatformLinux},
		Fingerprint: CertificateFingerprint([]byte("cert")),
		Addresses:   []string{"192.168.1.10:47823", "[fe80::1%eth0]:47823"},
		Code:        "ABCD-EFGH",
	}
	got, err := ParsePairingURI(inv.URI())
	if err != nil {
		t.Fatal(err)
	}
	if got.Device != inv.Device || got.Fingerprint != inv.Fingerprint || got.Code != inv.Code ||
		strings.Join(got.Addresses, ",") != strings.Join(inv.Addresses, ",") {
		t.Fatalf("mismatch: %+v", got)
	}
}

func TestParsePairingURIRejects(t *testing.T) {
	good := PairingInvitation{
		Device:      DeviceInfo{ID: "dev-1", Name: "x", Platform: PlatformLinux},
		Fingerprint: CertificateFingerprint([]byte("cert")),
		Addresses:   []string{"10.0.0.1:1"},
		Code:        "ABCD-EFGH",
	}.URI()
	for _, in := range []string{
		"https://pair?x=1",
		"luniboard://other?v=1",
		strings.Replace(good, "v=1", "v=9", 1),
		strings.Replace(good, "fp=", "fp=zz", 1),
		strings.Replace(good, "addr=10.0.0.1%3A1", "addr=10.0.0.1", 1),
		strings.Replace(good, "code=ABCD-EFGH", "code=AB", 1),
	} {
		if _, err := ParsePairingURI(in); err == nil {
			t.Errorf("ParsePairingURI(%q) succeeded", in)
		}
	}
}

func TestNewIDFormat(t *testing.T) {
	id := NewID()
	if !ValidID(id) || len(id) != 36 || id[14] != '4' {
		t.Fatalf("bad id %q", id)
	}
}
