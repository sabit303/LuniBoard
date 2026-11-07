package windows

import (
	"encoding/binary"
	"errors"
	"strings"
	"unicode/utf16"
	"unicode/utf8"

	"github.com/luniboard/luniboard/daemon/adapters/clipboard"
	"github.com/luniboard/luniboard/daemon/adapters/clipboard/cmdutil"
)

func encodeText(text string, maxBytes int64) ([]byte, error) {
	if text == "" {
		return nil, clipboard.ErrNoText
	}
	if int64(len(text)) > maxBytes {
		return nil, cmdutil.ErrOutputTooLarge
	}
	if !utf8.ValidString(text) || strings.ContainsRune(text, 0) {
		return nil, errors.New("windows clipboard text must be valid UTF-8 without NUL characters")
	}
	units := utf16.Encode([]rune(text))
	data := make([]byte, 2*(len(units)+1))
	for i, unit := range units {
		binary.LittleEndian.PutUint16(data[2*i:], unit)
	}
	return data, nil
}

func decodeText(data []byte, maxBytes int64) (string, error) {
	var text strings.Builder
	for i := 0; i+1 < len(data); i += 2 {
		unit := binary.LittleEndian.Uint16(data[i:])
		if unit == 0 {
			if text.Len() == 0 {
				return "", clipboard.ErrNoText
			}
			return text.String(), nil
		}
		r := rune(unit)
		if utf16.IsSurrogate(r) {
			if unit < 0xd800 || unit > 0xdbff || i+3 >= len(data) {
				return "", clipboard.ErrNoText
			}
			low := binary.LittleEndian.Uint16(data[i+2:])
			if low < 0xdc00 || low > 0xdfff {
				return "", clipboard.ErrNoText
			}
			r = utf16.DecodeRune(r, rune(low))
			i += 2
		}
		if int64(text.Len()+utf8.RuneLen(r)) > maxBytes {
			return "", cmdutil.ErrOutputTooLarge
		}
		text.WriteRune(r)
	}
	return "", clipboard.ErrNoText // malformed or missing NUL terminator
}
