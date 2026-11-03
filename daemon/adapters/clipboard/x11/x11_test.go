package x11

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/universal-clipboard/universal-clipboard/daemon/adapters/clipboard"
	"github.com/universal-clipboard/universal-clipboard/daemon/adapters/clipboard/cmdutil"
)

func TestXClipRead(t *testing.T) {
	f := &cmdutil.FakeRunner{}
	f.Set("xclip -selection clipboard -o -t TARGETS", cmdutil.FakeResult{Stdout: "TARGETS\nSTRING\nUTF8_STRING\n"})
	f.Set("xclip -selection clipboard -o -t UTF8_STRING", cmdutil.FakeResult{Stdout: "hi"})
	c, err := New(f, XClip, 1024).ReadText(context.Background())
	if err != nil || c.Text != "hi" || c.Sensitive {
		t.Fatalf("got %+v %v", c, err)
	}
}

func TestXClipEmpty(t *testing.T) {
	f := &cmdutil.FakeRunner{}
	f.Set("xclip -selection clipboard -o -t TARGETS", cmdutil.FakeResult{Err: &cmdutil.ExitError{Cmd: "xclip", Err: errors.New("exit 1"), Stderr: "Error: target TARGETS not available"}})
	if _, err := New(f, XClip, 1024).ReadText(context.Background()); !errors.Is(err, clipboard.ErrNoText) {
		t.Fatalf("expected ErrNoText, got %v", err)
	}
	f.Set("xclip -selection clipboard -o -t TARGETS", cmdutil.FakeResult{Err: &cmdutil.ExitError{Cmd: "xclip", Err: errors.New("exit 1"), Stderr: "Error: Can't open display: (null)"}})
	if _, err := New(f, XClip, 1024).ReadText(context.Background()); !errors.Is(err, clipboard.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestXSelWrite(t *testing.T) {
	f := &cmdutil.FakeRunner{}
	f.OnInput = func(cl string, stdin []byte) error {
		if cl != "xsel --clipboard --input" {
			t.Errorf("unexpected writer %q", cl)
		}
		f.Set("xsel --clipboard --output", cmdutil.FakeResult{Stdout: string(stdin)})
		return nil
	}
	if err := New(f, XSel, 1024).WriteText(context.Background(), "x"); err != nil {
		t.Fatal(err)
	}
}

// TestRealX11Clipboard exercises xclip against a live X server (or XWayland).
// It overwrites the clipboard, so it only runs when explicitly enabled.
func TestRealX11Clipboard(t *testing.T) {
	if os.Getenv("UCLIP_TEST_REAL_CLIPBOARD") != "1" || os.Getenv("DISPLAY") == "" {
		t.Skip("set UCLIP_TEST_REAL_CLIPBOARD=1 with DISPLAY to run")
	}
	for _, tool := range []Tool{XClip, XSel} {
		t.Run(string(tool), func(t *testing.T) {
			if _, err := exec.LookPath(string(tool)); err != nil {
				t.Skip(string(tool) + " not installed")
			}
			a := New(cmdutil.ExecRunner{}, tool, 1<<20)
			want := "universal-clipboard x11 test ✓ " + string(tool)
			if err := a.WriteText(context.Background(), want); err != nil {
				t.Fatal(err)
			}
			got, err := a.ReadText(context.Background())
			if err != nil || got.Text != want {
				t.Fatalf("read %q (%v), want %q", got.Text, err, want)
			}
		})
	}
}
