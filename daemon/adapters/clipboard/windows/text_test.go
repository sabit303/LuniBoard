package windows

import (
	"bytes"
	"encoding/binary"
	"errors"
	"testing"

	"github.com/luniboard/luniboard/daemon/adapters/clipboard"
	"github.com/luniboard/luniboard/daemon/adapters/clipboard/cmdutil"
)

func utf16LE(units ...uint16) []byte {
	data := make([]byte, 2*len(units))
	for i, unit := range units {
		binary.LittleEndian.PutUint16(data[2*i:], unit)
	}
	return data
}

func TestTextExactEncoding(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		data []byte
	}{
		{"ascii", "abc", utf16LE('a', 'b', 'c', 0)},
		{"unicode", "\u00e9e\u0301\u4e2d\U0001f642\ufffd", utf16LE(0x00e9, 'e', 0x0301, 0x4e2d, 0xd83d, 0xde42, 0xfffd, 0)},
		{"surrogate_boundaries", "\ud7ff\ue000\U00010000\U0010ffff", utf16LE(0xd7ff, 0xe000, 0xd800, 0xdc00, 0xdbff, 0xdfff, 0)},
		{"lf", "a\nb\n\n", utf16LE('a', '\n', 'b', '\n', '\n', 0)},
		{"crlf", "a\r\nb\r\n\r\n", utf16LE('a', '\r', '\n', 'b', '\r', '\n', '\r', '\n', 0)},
		{"lone_cr", "\ra\rb\r", utf16LE('\r', 'a', '\r', 'b', '\r', 0)},
		{"whitespace_only", " \t\n\r\u00a0 ", utf16LE(' ', '\t', '\n', '\r', 0x00a0, ' ', 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			limit := int64(len(tc.text))
			encoded, err := encodeText(tc.text, limit)
			if err != nil || !bytes.Equal(encoded, tc.data) {
				t.Fatalf("encodeText(%q) = %x, %v; want %x", tc.text, encoded, err, tc.data)
			}
			decoded, err := decodeText(tc.data, limit)
			if err != nil || decoded != tc.text {
				t.Fatalf("decodeText(%x) = %q, %v; want %q", tc.data, decoded, err, tc.text)
			}
		})
	}
}

func TestTextUTF8ByteLimits(t *testing.T) {
	for _, tc := range []struct {
		name string
		text string
		data []byte
	}{
		{"ascii", "ab", utf16LE('a', 'b', 0)},
		{"bmp", "\u4e2d", utf16LE(0x4e2d, 0)},
		{"supplementary", "\U0001f642", utf16LE(0xd83d, 0xde42, 0)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			for _, limit := range []int64{int64(len(tc.text)), int64(len(tc.text) - 1)} {
				var wantErr error
				if limit < int64(len(tc.text)) {
					wantErr = cmdutil.ErrOutputTooLarge
				}
				encoded, err := encodeText(tc.text, limit)
				if !errors.Is(err, wantErr) {
					t.Fatalf("encodeText limit %d: got %v, want %v", limit, err, wantErr)
				}
				decoded, err := decodeText(tc.data, limit)
				if !errors.Is(err, wantErr) {
					t.Fatalf("decodeText limit %d: got %v, want %v", limit, err, wantErr)
				}
				if wantErr != nil {
					if encoded != nil || decoded != "" {
						t.Fatalf("over-limit text returned partial data: %x, %q", encoded, decoded)
					}
				} else if !bytes.Equal(encoded, tc.data) || decoded != tc.text {
					t.Fatalf("exact limit changed text: %x, %q", encoded, decoded)
				}
			}
		})
	}
}

func TestDecodeTextRejectsMalformedData(t *testing.T) {
	for _, tc := range []struct {
		name string
		data []byte
	}{
		{"missing_data", nil},
		{"empty", utf16LE(0)},
		{"empty_with_padding", utf16LE(0, 'a', 0)},
		{"odd_byte", []byte{'a'}},
		{"partial_terminator", []byte{'a', 0, 0}},
		{"unterminated", utf16LE('a', 'b')},
		{"unpaired_low", utf16LE('a', 0xdc00, 0)},
		{"truncated_high", utf16LE(0xd800)},
		{"high_then_terminator", utf16LE(0xd800, 0)},
		{"high_then_text", utf16LE(0xd800, 'a', 0)},
		{"two_high_surrogates", utf16LE(0xd800, 0xdbff, 0)},
		{"unterminated_pair", utf16LE(0xd83d, 0xde42)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			text, err := decodeText(tc.data, 64)
			if text != "" || !errors.Is(err, clipboard.ErrNoText) {
				t.Fatalf("decodeText(%x) = %q, %v; want empty text and ErrNoText", tc.data, text, err)
			}
		})
	}
}

func TestDecodeTextIgnoresDataAfterTerminator(t *testing.T) {
	for _, tc := range []struct {
		name    string
		padding []byte
	}{
		{"subsequent_text", utf16LE('b', 'c', 0xd800)},
		{"allocation_padding", bytes.Repeat([]byte{0xff}, 65)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			data := append(utf16LE('a', 0), tc.padding...)
			text, err := decodeText(data, 1)
			if err != nil || text != "a" {
				t.Fatalf("decodeText with padding = %q, %v; want a", text, err)
			}
		})
	}
}
