package windows

import (
	"bytes"
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/luniboard/luniboard/daemon/adapters/clipboard"
	"github.com/luniboard/luniboard/daemon/adapters/clipboard/cmdutil"
)

const (
	fakeHWND     uintptr = 0x1234
	fakeHGLOBAL  uintptr = 0x5678
	exclude      uint32  = 0xc001
	viewerIgnore uint32  = 0xc002
	history      uint32  = 0xc003
	cloud        uint32  = 0xc004
)

type readRequest struct {
	format   uint32
	maxBytes int64
}

type fakeNative struct {
	t           *testing.T
	calls       []string
	formats     map[uint32][]byte
	failures    map[string]error
	readErrors  map[uint32]error
	reads       []readRequest
	openErrors  []error
	onOpen      func()
	onRead      func(uint32)
	owners      []uintptr
	allocated   []byte
	transferred bool
}

func newFakeNative(t *testing.T) *fakeNative {
	return &fakeNative{t: t, formats: make(map[uint32][]byte)}
}

func (f *fakeNative) call(name string) error {
	f.calls = append(f.calls, name)
	return f.failures[name]
}

func (f *fakeNative) createWindow() (uintptr, error) {
	if err := f.call("create"); err != nil {
		return 0, err
	}
	return fakeHWND, nil
}

func (f *fakeNative) destroyWindow(owner uintptr) error {
	if owner != fakeHWND {
		f.t.Errorf("destroyWindow owner = %#x, want %#x", owner, fakeHWND)
	}
	return f.call("destroy")
}

func (f *fakeNative) openClipboard(owner uintptr) error {
	f.owners = append(f.owners, owner)
	wantOwner := uintptr(0)
	if f.allocated != nil && !f.transferred {
		wantOwner = fakeHWND
	}
	if owner != wantOwner {
		f.t.Errorf("openClipboard owner = %#x, want %#x", owner, wantOwner)
	}
	err := f.call("open")
	if len(f.openErrors) != 0 {
		err, f.openErrors = f.openErrors[0], f.openErrors[1:]
	}
	if f.onOpen != nil {
		f.onOpen()
	}
	return err
}

func (f *fakeNative) closeClipboard() error { return f.call("close") }

func (f *fakeNative) formatAvailable(format uint32) bool {
	_ = f.call("available")
	_, ok := f.formats[format]
	return ok
}

func (f *fakeNative) readData(format uint32, maxBytes int64) ([]byte, error) {
	_ = f.call("read")
	f.reads = append(f.reads, readRequest{format, maxBytes})
	if f.onRead != nil {
		f.onRead(format)
	}
	if err := f.readErrors[format]; err != nil {
		return nil, err
	}
	data := f.formats[format]
	return bytes.Clone(data[:min(int64(len(data)), maxBytes)]), nil
}

func (f *fakeNative) allocData(data []byte) (uintptr, error) {
	if err := f.call("alloc"); err != nil {
		return 0, err
	}
	f.allocated = bytes.Clone(data)
	return fakeHGLOBAL, nil
}

func (f *fakeNative) freeData(memory uintptr) error {
	if memory != fakeHGLOBAL || f.allocated == nil || f.transferred {
		f.t.Errorf("freeData(%#x): invalid, already freed, or transferred allocation", memory)
	}
	f.allocated = nil
	return f.call("free")
}

func (f *fakeNative) emptyClipboard() error {
	if f.allocated == nil {
		f.t.Error("clipboard emptied before allocation")
	}
	if err := f.call("empty"); err != nil {
		return err
	}
	f.formats = make(map[uint32][]byte)
	return nil
}

func (f *fakeNative) setData(format uint32, memory uintptr) error {
	if format != 13 || memory != fakeHGLOBAL || f.allocated == nil || f.transferred {
		f.t.Errorf("setData(%d, %#x): invalid format or allocation", format, memory)
	}
	if err := f.call("set"); err != nil {
		return err
	}
	f.formats[format] = bytes.Clone(f.allocated)
	f.transferred = true
	return nil
}

func TestAdapterReadTextPrivacy(t *testing.T) {
	for _, tc := range []struct {
		name      string
		formats   map[uint32][]byte
		sensitive bool
	}{
		{"unmarked", nil, false},
		{"exclude_presence", map[uint32][]byte{exclude: nil}, true},
		{"viewer_ignore_presence", map[uint32][]byte{viewerIgnore: {1}}, true},
		{"history_zero", map[uint32][]byte{history: {0, 0, 0, 0}}, true},
		{"cloud_zero", map[uint32][]byte{cloud: {0, 0, 0, 0}}, true},
		{"allowed", map[uint32][]byte{history: {1, 0, 0, 0}, cloud: {0, 0, 0, 0x80}}, false},
		{"empty_dword", map[uint32][]byte{history: nil}, true},
		{"short_dword", map[uint32][]byte{cloud: {1, 0, 0}}, true},
		{"allowed_with_padding", map[uint32][]byte{history: {1, 0, 0, 0, 0xff}}, false},
		{"restriction_not_cleared", map[uint32][]byte{history: {0, 0, 0, 0}, cloud: {1, 0, 0, 0}}, true},
		{"marker_not_cleared", map[uint32][]byte{exclude: nil, history: {1, 0, 0, 0}, cloud: {1, 0, 0, 0}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeNative(t)
			api.formats[unicodeText] = utf16LE(0x4e2d, '\r', '\n', ' ', 0)
			for format, data := range tc.formats {
				api.formats[format] = data
			}
			a := &Adapter{api: api, maxBytes: 32, sensitiveFormats: []uint32{exclude, viewerIgnore}, privacyFormats: []uint32{history, cloud}}
			got, err := a.ReadText(context.Background())
			want := clipboard.Content{Text: "\u4e2d\r\n ", Sensitive: tc.sensitive}
			if err != nil || got != want {
				t.Fatalf("ReadText = %+v, %v; want %+v", got, err, want)
			}
			wantReads := []readRequest{{unicodeText, 66}}
			for _, format := range []uint32{history, cloud} {
				if _, ok := tc.formats[format]; ok {
					wantReads = append(wantReads, readRequest{format, 4})
				}
			}
			if !slices.Equal(api.reads, wantReads) {
				t.Fatalf("readData requests = %v, want %v", api.reads, wantReads)
			}
			if api.calls[len(api.calls)-1] != "close" {
				t.Fatalf("clipboard not closed: %v", api.calls)
			}
		})
	}
}

func TestAdapterReadTextErrors(t *testing.T) {
	nativeErr := errors.New("native read failed")
	closeErr := errors.New("close failed")
	for _, tc := range []struct {
		name        string
		formats     map[uint32][]byte
		readErrors  map[uint32]error
		closeErr    error
		wantErr     error
		unavailable bool
		wantContent clipboard.Content
	}{
		{name: "image_only", formats: map[uint32][]byte{8: {1}}, wantErr: clipboard.ErrNoText},
		{name: "empty", formats: map[uint32][]byte{unicodeText: utf16LE(0)}, wantErr: clipboard.ErrNoText},
		{name: "malformed", formats: map[uint32][]byte{unicodeText: utf16LE(0xd800, 0)}, wantErr: clipboard.ErrNoText},
		{name: "bounded_oversize_ascii", formats: map[uint32][]byte{unicodeText: utf16LE('a', 'b', 'c', 'd', 0)}, wantErr: cmdutil.ErrOutputTooLarge},
		{name: "oversize_utf8", formats: map[uint32][]byte{unicodeText: utf16LE(0x4e2d, 0)}, wantErr: cmdutil.ErrOutputTooLarge},
		{name: "read", formats: map[uint32][]byte{unicodeText: utf16LE('x', 0)}, readErrors: map[uint32]error{unicodeText: nativeErr}, wantErr: nativeErr, unavailable: true},
		{name: "privacy_read", formats: map[uint32][]byte{unicodeText: utf16LE('x', 0), history: nil}, readErrors: map[uint32]error{history: nativeErr}, wantErr: nativeErr, unavailable: true},
		{name: "close", formats: map[uint32][]byte{unicodeText: utf16LE('x', 0)}, closeErr: closeErr, wantErr: closeErr, unavailable: true, wantContent: clipboard.Content{Text: "x"}},
		{name: "no_text_and_close", closeErr: closeErr, wantErr: clipboard.ErrNoText},
		{name: "read_and_close", formats: map[uint32][]byte{unicodeText: utf16LE('x', 0)}, readErrors: map[uint32]error{unicodeText: nativeErr}, closeErr: closeErr, wantErr: nativeErr, unavailable: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeNative(t)
			api.formats, api.readErrors = tc.formats, tc.readErrors
			api.failures = map[string]error{"close": tc.closeErr}
			a := &Adapter{api: api, maxBytes: 2, privacyFormats: []uint32{history}}
			got, err := a.ReadText(context.Background())
			if !errors.Is(err, tc.wantErr) || errors.Is(err, clipboard.ErrUnavailable) != tc.unavailable || got != tc.wantContent {
				t.Fatalf("ReadText = %+v, %v; want %+v, %v (unavailable=%v)", got, err, tc.wantContent, tc.wantErr, tc.unavailable)
			}
			if tc.wantErr != closeErr && errors.Is(err, closeErr) {
				t.Fatalf("cleanup error replaced primary error: %v", err)
			}
			wantCalls := "open available"
			if _, ok := tc.formats[unicodeText]; ok {
				wantCalls += " read"
			}
			if tc.name == "privacy_read" {
				wantCalls += " available read"
			} else if tc.name == "close" {
				wantCalls += " available"
			}
			wantCalls += " close"
			if got := strings.Join(api.calls, " "); got != wantCalls {
				t.Fatalf("native calls = %s, want %s", got, wantCalls)
			}
		})
	}
}

func TestAdapterWriteTextRejectsInvalidInput(t *testing.T) {
	for _, tc := range []struct {
		name    string
		text    string
		wantErr error
	}{
		{"empty", "", clipboard.ErrNoText},
		{"invalid_utf8", "\xff", nil},
		{"utf8_surrogate", "\xed\xa0\x80", nil},
		{"nul", "a\x00b", nil},
		{"oversize_utf8", "\u4e2d", cmdutil.ErrOutputTooLarge},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeNative(t)
			limit := int64(8)
			if tc.name == "oversize_utf8" {
				limit = 2
			}
			err := (&Adapter{api: api, maxBytes: limit}).WriteText(context.Background(), tc.text)
			if err == nil || (tc.wantErr != nil && !errors.Is(err, tc.wantErr)) || errors.Is(err, clipboard.ErrUnavailable) {
				t.Fatalf("WriteText(%q) = %v, want validation error %v", tc.text, err, tc.wantErr)
			}
			if len(api.calls) != 0 {
				t.Fatalf("invalid input touched native clipboard: %v", api.calls)
			}
		})
	}
}

func TestAdapterWriteTextCleanup(t *testing.T) {
	nativeErr := errors.New("native operation failed")
	cleanupErr := errors.New("cleanup failed")
	old, written := utf16LE('o', 'l', 'd', 0), utf16LE('x', 0)
	for _, tc := range []struct {
		name         string
		failures     map[string]error
		cancelOnOpen bool
		wantErr      error
		calls        string
		data         []byte
		transferred  bool
	}{
		{"create", map[string]error{"create": nativeErr}, false, nativeErr, "create", old, false},
		{"alloc", map[string]error{"alloc": nativeErr}, false, nativeErr, "create alloc destroy", old, false},
		{"empty", map[string]error{"empty": nativeErr}, false, nativeErr, "create alloc open empty close free destroy", old, false},
		{"set", map[string]error{"set": nativeErr}, false, nativeErr, "create alloc open empty set close free destroy", nil, false},
		{"close", map[string]error{"close": nativeErr}, false, nativeErr, "create alloc open empty set close destroy", written, true},
		{"destroy", map[string]error{"destroy": nativeErr}, false, nativeErr, "create alloc open empty set close destroy", written, true},
		{"cleanup_errors", map[string]error{"set": nativeErr, "close": cleanupErr, "free": cleanupErr, "destroy": cleanupErr}, false, nativeErr, "create alloc open empty set close free destroy", nil, false},
		{"cancel_after_open", nil, true, context.Canceled, "create alloc open close free destroy", old, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeNative(t)
			api.formats[unicodeText], api.failures = old, tc.failures
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.cancelOnOpen {
				api.onOpen = cancel
			}
			err := (&Adapter{api: api, maxBytes: 16}).WriteText(ctx, "x")
			if !errors.Is(err, tc.wantErr) || errors.Is(err, clipboard.ErrUnavailable) != !tc.cancelOnOpen || errors.Is(err, cleanupErr) {
				t.Fatalf("WriteText = %v, want primary error %v", err, tc.wantErr)
			}
			if got := strings.Join(api.calls, " "); got != tc.calls {
				t.Fatalf("native calls = %s, want %s", got, tc.calls)
			}
			if !bytes.Equal(api.formats[unicodeText], tc.data) || api.transferred != tc.transferred {
				t.Fatalf("clipboard = %x, transferred=%v; want %x, %v", api.formats[unicodeText], api.transferred, tc.data, tc.transferred)
			}
		})
	}
}

func TestAdapterCancellation(t *testing.T) {
	for _, operation := range []string{"read", "write"} {
		for _, phase := range []string{"before", "contended"} {
			t.Run(operation+"/"+phase, func(t *testing.T) {
				api := newFakeNative(t)
				old := utf16LE('o', 'l', 'd', 0)
				api.formats[unicodeText] = old
				ctx, cancel := context.WithCancel(context.Background())
				defer cancel()
				wantCalls := ""
				if phase == "before" {
					cancel()
				} else {
					api.failures = map[string]error{"open": errors.New("clipboard busy")}
					api.onOpen = cancel
					wantCalls = "open"
					if operation == "write" {
						wantCalls = "create alloc open free destroy"
					}
				}
				a := &Adapter{api: api, maxBytes: 16}
				var err error
				if operation == "read" {
					_, err = a.ReadText(ctx)
				} else {
					err = a.WriteText(ctx, "x")
				}
				wantUnavailable := operation == "read" || phase == "contended"
				if !errors.Is(err, context.Canceled) || errors.Is(err, clipboard.ErrUnavailable) != wantUnavailable {
					t.Fatalf("canceled %s = %v", operation, err)
				}
				if got := strings.Join(api.calls, " "); got != wantCalls {
					t.Fatalf("native calls = %s, want %s", got, wantCalls)
				}
				if !bytes.Equal(api.formats[unicodeText], old) {
					t.Fatal("cancellation changed existing clipboard")
				}
			})
		}
	}
}

func TestAdapterWriteVisibleAfterContention(t *testing.T) {
	api := newFakeNative(t)
	busy := errors.New("clipboard busy")
	api.openErrors = []error{busy, busy}
	text := " \u4e2d\U0001f642\r\n\n"
	data := utf16LE(' ', 0x4e2d, 0xd83d, 0xde42, '\r', '\n', '\n', 0)
	a := &Adapter{api: api, maxBytes: int64(len(text))}
	if a.Name() != "windows" {
		t.Fatalf("Name = %q, want windows", a.Name())
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if err := a.WriteText(ctx, text); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(api.allocated, data) || !api.transferred {
		t.Fatalf("allocation = %x, transferred=%v; want %x, true", api.allocated, api.transferred, data)
	}
	api.openErrors = []error{busy, busy}
	got, err := a.ReadText(ctx)
	if err != nil || got != (clipboard.Content{Text: text}) {
		t.Fatalf("ReadText after WriteText = %+v, %v; want %q", got, err, text)
	}
	wantCalls := "create alloc open open open empty set close destroy open open open available read close"
	if got := strings.Join(api.calls, " "); got != wantCalls {
		t.Fatalf("native calls = %s, want %s", got, wantCalls)
	}
	if !slices.Equal(api.owners, []uintptr{fakeHWND, fakeHWND, fakeHWND, 0, 0, 0}) {
		t.Fatalf("openClipboard owners = %v", api.owners)
	}
	if !slices.Equal(api.reads, []readRequest{{unicodeText, 2*int64(len(text)) + 2}}) {
		t.Fatalf("readData requests = %v", api.reads)
	}
}

func TestAdapterReadTextCancelledDuringNativeCall(t *testing.T) {
	for _, tc := range []struct {
		name   string
		format uint32
		calls  string
		reads  []readRequest
	}{
		{"open", 0, "open close", nil},
		{"text", unicodeText, "open available read close", []readRequest{{unicodeText, 18}}},
		{"privacy", history, "open available read available read close", []readRequest{{unicodeText, 18}, {history, 4}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api := newFakeNative(t)
			api.formats = map[uint32][]byte{unicodeText: utf16LE('x', 0), history: {1, 0, 0, 0}, cloud: {1, 0, 0, 0}}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tc.format == 0 {
				api.onOpen = cancel
			} else {
				api.onRead = func(format uint32) {
					if format == tc.format {
						cancel()
					}
				}
			}
			a := &Adapter{api: api, maxBytes: 8, privacyFormats: []uint32{history, cloud}}
			got, err := a.ReadText(ctx)
			if got != (clipboard.Content{}) || !errors.Is(err, context.Canceled) || !errors.Is(err, clipboard.ErrUnavailable) {
				t.Errorf("ReadText after cancellation = %+v, %v; want no content and unavailable cancellation", got, err)
			}
			if got := strings.Join(api.calls, " "); got != tc.calls || !slices.Equal(api.reads, tc.reads) {
				t.Fatalf("native calls = %s, reads = %v; want %s, %v", got, api.reads, tc.calls, tc.reads)
			}
		})
	}
}
