// Package wayland implements the clipboard adapter for Wayland sessions using
// wl-clipboard (wl-copy / wl-paste). It works with any compositor; change
// notifications additionally require the wlr/ext data-control protocol
// (wlroots compositors such as Sway and Hyprland, KDE Plasma, ...). Without
// it, Watch fails and the core falls back to polling.
package wayland

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/universal-clipboard/universal-clipboard/daemon/adapters/clipboard"
	"github.com/universal-clipboard/universal-clipboard/daemon/adapters/clipboard/cmdutil"
)

// Tools are the executables this adapter needs.
var Tools = []string{"wl-copy", "wl-paste"}

// Adapter talks to the Wayland clipboard through wl-clipboard.
type Adapter struct {
	run      cmdutil.Runner
	maxBytes int64
}

// New creates an adapter. maxBytes bounds how much clipboard text is read.
func New(run cmdutil.Runner, maxBytes int64) *Adapter {
	return &Adapter{run: run, maxBytes: maxBytes}
}

// Name implements clipboard.Adapter.
func (a *Adapter) Name() string { return "wayland" }

// ReadText implements clipboard.Adapter.
func (a *Adapter) ReadText(ctx context.Context) (clipboard.Content, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := a.run.Output(ctx, 64<<10, "wl-paste", "--list-types")
	if err != nil {
		return clipboard.Content{}, classify(err)
	}
	types := strings.Split(strings.TrimSpace(string(out)), "\n")
	t := clipboard.PickTextType(types)
	if t == "" {
		return clipboard.Content{}, clipboard.ErrNoText
	}
	text, err := a.run.Output(ctx, a.maxBytes, "wl-paste", "--no-newline", "--type", t)
	if err != nil {
		return clipboard.Content{}, classify(err)
	}
	if len(text) == 0 || !utf8.Valid(text) {
		return clipboard.Content{}, clipboard.ErrNoText
	}
	return clipboard.Content{Text: string(text), Sensitive: slices.Contains(types, clipboard.PasswordManagerHint)}, nil
}

// WriteText implements clipboard.Adapter.
func (a *Adapter) WriteText(ctx context.Context, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := a.run.Input(ctx, []byte(text), "wl-copy", "--type", "text/plain;charset=utf-8"); err != nil {
		return classify(err)
	}
	return clipboard.WaitVisible(ctx, a.ReadText, text, 2*time.Second)
}

// Watch implements clipboard.Watcher using `wl-paste --watch`, which runs a
// command on every selection change. We run `echo` and treat each output line
// as a change notification; the content itself is read separately.
func (a *Adapter) Watch(ctx context.Context, notify func()) error {
	lines, wait, err := a.run.Lines(ctx, "wl-paste", "--watch", "echo", "changed")
	if err != nil {
		return fmt.Errorf("start wl-paste --watch: %w", err)
	}
	for range lines {
		notify()
	}
	err = wait()
	if ctx.Err() != nil {
		return nil
	}
	if err == nil {
		err = errors.New("wl-paste --watch exited")
	}
	return err
}

// classify maps tool failures to adapter errors.
func classify(err error) error {
	if errors.Is(err, cmdutil.ErrOutputTooLarge) {
		return err
	}
	var ee *cmdutil.ExitError
	if errors.As(err, &ee) {
		msg := strings.ToLower(ee.Stderr)
		if strings.Contains(msg, "nothing is copied") || strings.Contains(msg, "no selection") ||
			strings.Contains(msg, "no suitable type") {
			return clipboard.ErrNoText
		}
	}
	return fmt.Errorf("%w: %v", clipboard.ErrUnavailable, err)
}
