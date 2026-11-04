package wayland

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"testing"

	"github.com/luniboard/luniboard/daemon/adapters/clipboard"
	"github.com/luniboard/luniboard/daemon/adapters/clipboard/cmdutil"
)

func TestReadTextPicksUTF8AndDetectsSecrets(t *testing.T) {
	f := &cmdutil.FakeRunner{}
	f.Set("wl-paste --list-types", cmdutil.FakeResult{Stdout: "text/html\ntext/plain;charset=utf-8\nx-kde-passwordManagerHint\n"})
	f.Set("wl-paste --no-newline --type text/plain;charset=utf-8", cmdutil.FakeResult{Stdout: "s3cret"})
	c, err := New(f, 1024).ReadText(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if c.Text != "s3cret" || !c.Sensitive {
		t.Fatalf("got %+v", c)
	}
}

func TestReadTextNoText(t *testing.T) {
	f := &cmdutil.FakeRunner{}
	f.Set("wl-paste --list-types", cmdutil.FakeResult{Stdout: "image/png\n"})
	if _, err := New(f, 1024).ReadText(context.Background()); !errors.Is(err, clipboard.ErrNoText) {
		t.Fatalf("expected ErrNoText, got %v", err)
	}
	f.Set("wl-paste --list-types", cmdutil.FakeResult{Err: &cmdutil.ExitError{Cmd: "wl-paste", Err: errors.New("exit 1"), Stderr: "Nothing is copied"}})
	if _, err := New(f, 1024).ReadText(context.Background()); !errors.Is(err, clipboard.ErrNoText) {
		t.Fatalf("expected ErrNoText, got %v", err)
	}
	f.Set("wl-paste --list-types", cmdutil.FakeResult{Err: &cmdutil.ExitError{Cmd: "wl-paste", Err: errors.New("exit 1"), Stderr: "Failed to connect to a Wayland server"}})
	if _, err := New(f, 1024).ReadText(context.Background()); !errors.Is(err, clipboard.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable, got %v", err)
	}
}

func TestReadTextRejectsBinaryAndOversize(t *testing.T) {
	f := &cmdutil.FakeRunner{}
	f.Set("wl-paste --list-types", cmdutil.FakeResult{Stdout: "text/plain\n"})
	f.Set("wl-paste --no-newline --type text/plain", cmdutil.FakeResult{Stdout: "\xff\xfe"})
	if _, err := New(f, 1024).ReadText(context.Background()); !errors.Is(err, clipboard.ErrNoText) {
		t.Fatalf("expected ErrNoText for invalid UTF-8, got %v", err)
	}
	f.Set("wl-paste --no-newline --type text/plain", cmdutil.FakeResult{Stdout: "0123456789"})
	if _, err := New(f, 5).ReadText(context.Background()); !errors.Is(err, cmdutil.ErrOutputTooLarge) {
		t.Fatalf("expected ErrOutputTooLarge, got %v", err)
	}
}

func TestWriteTextVerifies(t *testing.T) {
	f := &cmdutil.FakeRunner{}
	f.Set("wl-paste --list-types", cmdutil.FakeResult{Stdout: "text/plain;charset=utf-8\n"})
	f.OnInput = func(cl string, stdin []byte) error {
		if cl != "wl-copy --type text/plain;charset=utf-8" {
			t.Errorf("unexpected writer %q", cl)
		}
		f.Set("wl-paste --no-newline --type text/plain;charset=utf-8", cmdutil.FakeResult{Stdout: string(stdin)})
		return nil
	}
	if err := New(f, 1024).WriteText(context.Background(), "héllo\n"); err != nil {
		t.Fatal(err)
	}
}

func TestWatchNotifies(t *testing.T) {
	f := &cmdutil.FakeRunner{}
	f.Set("wl-paste --watch echo changed", cmdutil.FakeResult{Stdout: "changed\nchanged\n"})
	n := 0
	err := New(f, 1024).Watch(context.Background(), func() { n++ })
	if n != 2 || err == nil {
		t.Fatalf("notifications=%d err=%v (exit without cancellation must be an error)", n, err)
	}
}

// TestRealWaylandClipboard exercises wl-clipboard against the live session.
// It overwrites the user's clipboard, so it only runs when explicitly enabled.
func TestRealWaylandClipboard(t *testing.T) {
	if os.Getenv("LUNIBOARD_TEST_REAL_CLIPBOARD") != "1" || os.Getenv("WAYLAND_DISPLAY") == "" {
		t.Skip("set LUNIBOARD_TEST_REAL_CLIPBOARD=1 in a Wayland session to run")
	}
	for _, tool := range Tools {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " not installed")
		}
	}
	a := New(cmdutil.ExecRunner{}, 1<<20)
	want := "luniboard wayland test ✓\nsecond line"
	if err := a.WriteText(context.Background(), want); err != nil {
		t.Fatal(err)
	}
	got, err := a.ReadText(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if got.Text != want {
		t.Fatalf("read %q, want %q", got.Text, want)
	}
}
