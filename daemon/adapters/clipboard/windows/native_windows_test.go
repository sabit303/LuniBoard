package windows

import (
	"context"
	"errors"
	"os"
	"runtime"
	"testing"
	"time"

	"github.com/luniboard/luniboard/daemon/adapters/clipboard"
	"github.com/luniboard/luniboard/daemon/adapters/clipboard/cmdutil"
)

func TestNewRejectsInvalidLimit(t *testing.T) {
	for _, limit := range []int64{-1, 0, 1<<63 - 1} {
		if _, err := New(limit); err == nil {
			t.Fatalf("New(%d) succeeded", limit)
		}
	}
}

func realAdapter(t *testing.T, maxBytes int64) *Adapter {
	t.Helper()
	if os.Getenv("LUNIBOARD_TEST_REAL_CLIPBOARD") != "1" {
		t.Skip("set LUNIBOARD_TEST_REAL_CLIPBOARD=1 to test the Windows clipboard (overwrites it)")
	}
	a, err := New(maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	return a
}

func TestRealClipboardRoundTrip(t *testing.T) {
	a := realAdapter(t, 1<<20)
	for _, text := range []string{
		"LuniBoard Windows clipboard",
		"caf\u00e9 \u65e5\u672c \U0001d11e e\u0301",
		"LF\nCRLF\r\nCR\rtrailing\n",
		" \t\n ",
	} {
		if err := a.WriteText(context.Background(), text); err != nil {
			t.Fatal(err)
		}
		// The write's owner HWND is already destroyed; the data must persist.
		got, err := a.ReadText(context.Background())
		if err != nil || got.Text != text || got.Sensitive {
			t.Fatalf("read after write: got %+v, %v; want %q", got, err, text)
		}
	}
	if err := a.WriteText(context.Background(), "before\x00after"); err == nil {
		t.Fatal("NUL-containing write succeeded")
	}
	got, err := a.ReadText(context.Background())
	if err != nil || got.Text != " \t\n " {
		t.Fatalf("invalid write changed clipboard: %+v, %v", got, err)
	}
}

func TestRealClipboardFormatsAndLimits(t *testing.T) {
	a := realAdapter(t, 1<<20)
	format, err := registerFormat("LuniBoard.Test.NonText")
	if err != nil {
		t.Fatal(err)
	}
	setRealClipboard(t, a, []realFormat{{format, []byte("not text")}})
	if _, err := a.ReadText(context.Background()); !errors.Is(err, clipboard.ErrNoText) {
		t.Fatalf("non-text clipboard: %v", err)
	}
	// Windows synthesizes Unicode for legacy applications that publish CF_TEXT.
	setRealClipboard(t, a, []realFormat{{1, []byte("legacy ASCII\r\n\x00")}})
	got, err := a.ReadText(context.Background())
	if err != nil || got.Text != "legacy ASCII\r\n" {
		t.Fatalf("legacy clipboard text: %+v, %v", got, err)
	}
	if err := a.WriteText(context.Background(), "\u00e9\u00e9\u00e9"); err != nil {
		t.Fatal(err)
	}
	small := realAdapter(t, 5)
	if _, err := small.ReadText(context.Background()); !errors.Is(err, cmdutil.ErrOutputTooLarge) {
		t.Fatalf("UTF-8 byte limit: %v", err)
	}
	exact := realAdapter(t, 6)
	got, err = exact.ReadText(context.Background())
	if err != nil || got.Text != "\u00e9\u00e9\u00e9" {
		t.Fatalf("exact UTF-8 byte limit: %+v, %v", got, err)
	}
}

func TestRealClipboardSensitiveFormats(t *testing.T) {
	a := realAdapter(t, 1<<20)
	text, err := encodeText("marked content", a.maxBytes)
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name      string
		data      []byte
		sensitive bool
	}{
		{"ExcludeClipboardContentFromMonitorProcessing", []byte{0}, true},
		{"Clipboard Viewer Ignore", []byte{0}, true},
		{"CanIncludeInClipboardHistory", []byte{0, 0, 0, 0}, true},
		{"CanIncludeInClipboardHistory", []byte{1, 0, 0, 0}, false},
		{"CanUploadToCloudClipboard", []byte{0, 0, 0, 0}, true},
		{"CanUploadToCloudClipboard", []byte{1, 0, 0, 0}, false},
	} {
		format, err := registerFormat(test.name)
		if err != nil {
			t.Fatal(err)
		}
		setRealClipboard(t, a, []realFormat{{unicodeText, text}, {format, test.data}})
		got, err := a.ReadText(context.Background())
		if err != nil || got.Text != "marked content" || got.Sensitive != test.sensitive {
			t.Fatalf("format %s=%v: %+v, %v", test.name, test.data, got, err)
		}
	}
}

func TestRealClipboardContentionCancellation(t *testing.T) {
	a := realAdapter(t, 1<<20)
	if err := a.WriteText(context.Background(), "keep while busy"); err != nil {
		t.Fatal(err)
	}
	opened := make(chan error, 1)
	release := make(chan struct{})
	done := make(chan struct{})
	go func() {
		runtime.LockOSThread()
		defer runtime.UnlockOSThread()
		defer close(done)
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := a.open(ctx, 0)
		opened <- err
		if err == nil {
			defer a.api.closeClipboard()
			<-release
		}
	}()
	if err := <-opened; err != nil {
		t.Fatal(err)
	}
	defer func() {
		close(release)
		<-done
		got, err := a.ReadText(context.Background())
		if err != nil || got.Text != "keep while busy" {
			t.Errorf("cancelled write changed clipboard: %+v, %v", got, err)
		}
	}()
	for _, write := range []bool{false, true} {
		ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
		var err error
		if write {
			err = a.WriteText(ctx, "must not replace busy clipboard")
		} else {
			_, err = a.ReadText(ctx)
		}
		cancel()
		if !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, clipboard.ErrUnavailable) {
			t.Fatalf("contention (write=%v): %v", write, err)
		}
	}
}

type realFormat struct {
	format uint32
	data   []byte
}

func setRealClipboard(t *testing.T, a *Adapter, formats []realFormat) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	owner, err := a.api.createWindow()
	if err != nil {
		t.Fatal(err)
	}
	defer a.api.destroyWindow(owner)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := a.open(ctx, owner); err != nil {
		t.Fatal(err)
	}
	defer a.api.closeClipboard()
	if err := a.api.emptyClipboard(); err != nil {
		t.Fatal(err)
	}
	for _, item := range formats {
		memory, err := a.api.allocData(item.data)
		if err != nil {
			t.Fatal(err)
		}
		if err := a.api.setData(item.format, memory); err != nil {
			_ = a.api.freeData(memory)
			t.Fatal(err)
		}
	}
}
