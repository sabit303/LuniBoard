package protocol

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/santhosh-tekuri/jsonschema/v6"
)

// These tests keep the Go types, the JSON Schemas in protocol/schemas/v1 and
// the examples in protocol/messages in sync. Other clients (Android, iOS)
// implement against the schemas.

const schemaBase = "https://schemas.universal-clipboard.org/v1/"

func repoPath(parts ...string) string {
	return filepath.Join(append([]string{"..", "..", ".."}, parts...)...)
}

func compileSchemas(t *testing.T) map[string]*jsonschema.Schema {
	t.Helper()
	dir := repoPath("protocol", "schemas", "v1")
	files, err := filepath.Glob(filepath.Join(dir, "*.schema.json"))
	if err != nil || len(files) == 0 {
		t.Fatalf("no schemas found in %s: %v", dir, err)
	}
	c := jsonschema.NewCompiler()
	for _, f := range files {
		b, err := os.ReadFile(f)
		if err != nil {
			t.Fatal(err)
		}
		doc, err := jsonschema.UnmarshalJSON(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("%s: %v", f, err)
		}
		if err := c.AddResource(schemaBase+filepath.Base(f), doc); err != nil {
			t.Fatal(err)
		}
	}
	out := map[string]*jsonschema.Schema{}
	for _, f := range files {
		name := strings.TrimSuffix(filepath.Base(f), ".schema.json")
		s, err := c.Compile(schemaBase + filepath.Base(f))
		if err != nil {
			t.Fatalf("compile %s: %v", f, err)
		}
		out[name] = s
	}
	return out
}

func validate(t *testing.T, s *jsonschema.Schema, raw []byte) {
	t.Helper()
	inst, err := jsonschema.UnmarshalJSON(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if err := s.Validate(inst); err != nil {
		t.Fatalf("schema validation failed: %v\n%s", err, raw)
	}
}

func TestGoMessagesMatchSchemas(t *testing.T) {
	s := compileSchemas(t)
	dev := DeviceInfo{ID: NewID(), Name: "Arch Desktop", Platform: PlatformLinux, AppVersion: "0.1.0"}
	ev := NewTextEvent(NewID(), dev.ID, "https://example.com/a", 1793730000000)
	for _, tc := range []struct {
		typ MessageType
		v   any
	}{
		{TypeHello, Hello{Device: dev, MinProtocolVersion: 1, MaxProtocolVersion: 1, Time: 1, Capabilities: []string{CapabilitySend}, MaxPayloadBytes: 1024, ListenPort: 47823}},
		{TypeClipboardEvent, ev},
		{TypeAck, Ack{EventID: ev.EventID, Status: AckStale, Reason: "newer clipboard content present"}},
		{TypeError, ErrorMessage{Code: ErrCodeBadRequest, Message: "x"}},
	} {
		b, err := Encode(tc.typ, tc.v)
		if err != nil {
			t.Fatal(err)
		}
		validate(t, s["envelope"], b)
	}
	for name, v := range map[string]any{
		"pair-request":  PairRequest{ProtocolVersion: 1, Device: dev, Code: "ABCD-EFGH", ListenPort: 0},
		"pair-response": PairResponse{ProtocolVersion: 1, Status: PairApproved, Device: dev, ListenPort: 47823},
		"http-error":    HTTPError{Error: ErrorMessage{Code: ErrCodePairingClosed, Message: "closed"}},
	} {
		b, _ := json.Marshal(v)
		validate(t, s[name], b)
	}
}

func TestSchemaRejectsInvalidEvent(t *testing.T) {
	s := compileSchemas(t)
	bad := `{"type":"clipboard.event","protocol_version":1,"data":{"event_id":"has space","origin_device_id":"a","content_type":"image","timestamp":0,"protocol_version":1,"payload":{}}}`
	inst, _ := jsonschema.UnmarshalJSON(strings.NewReader(bad))
	if s["envelope"].Validate(inst) == nil {
		t.Fatal("schema accepted an invalid event")
	}
}

func TestExampleMessages(t *testing.T) {
	s := compileSchemas(t)
	read := func(name string) []byte {
		b, err := os.ReadFile(repoPath("protocol", "messages", name))
		if err != nil {
			t.Fatal(err)
		}
		return b
	}

	for _, name := range []string{"clipboard-event.json", "acknowledgement.json", "hello.json"} {
		raw := read(name)
		validate(t, s["envelope"], raw)
		env, err := DecodeEnvelope(raw)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if env.Type == TypeClipboardEvent {
			var ev ClipboardEvent
			if err := env.DecodeData(&ev); err != nil {
				t.Fatal(err)
			}
			if err := ValidateEvent(ev, 1<<20); err != nil {
				t.Fatalf("example event fails Go validation: %v", err)
			}
		}
	}

	var pairing struct {
		Request  json.RawMessage `json:"request"`
		Response json.RawMessage `json:"response"`
	}
	if err := json.Unmarshal(read("pairing.json"), &pairing); err != nil {
		t.Fatal(err)
	}
	validate(t, s["pair-request"], pairing.Request)
	validate(t, s["pair-response"], pairing.Response)

	var dev struct {
		DeviceInfo json.RawMessage   `json:"device_info"`
		PairingURI string            `json:"pairing_uri"`
		TXT        map[string]string `json:"dns_sd_txt"`
	}
	if err := json.Unmarshal(read("device.json"), &dev); err != nil {
		t.Fatal(err)
	}
	validate(t, s["device-info"], dev.DeviceInfo)
	inv, err := ParsePairingURI(dev.PairingURI)
	if err != nil {
		t.Fatalf("example pairing URI: %v", err)
	}
	if inv.Fingerprint != dev.TXT["fp"] || inv.Device.ID != dev.TXT["id"] {
		t.Fatal("example URI and TXT record disagree")
	}
}
