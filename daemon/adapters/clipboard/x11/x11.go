// Package x11 implements the clipboard adapter for X11 sessions using xclip
// (preferred) or xsel. X11 tools offer no change notifications, so the core
// polls this adapter.
package x11

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

// Tool selects the helper executable.
type Tool string

const (
	XClip Tool = "xclip"
	XSel  Tool = "xsel"
)

// Adapter talks to the X11 CLIPBOARD selection.
type Adapter struct {
	run      cmdutil.Runner
	tool     Tool
	maxBytes int64
}

// New creates an adapter using tool.
func New(run cmdutil.Runner, tool Tool, maxBytes int64) *Adapter {
	return &Adapter{run: run, tool: tool, maxBytes: maxBytes}
}

// Name implements clipboard.Adapter.
func (a *Adapter) Name() string { return "x11 (" + string(a.tool) + ")" }

// ReadText implements clipboard.Adapter.
func (a *Adapter) ReadText(ctx context.Context) (clipboard.Content, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var (
		text      []byte
		sensitive bool
		err       error
	)
	switch a.tool {
	case XSel:
		text, err = a.run.Output(ctx, a.maxBytes, "xsel", "--clipboard", "--output")
	default:
		var targets []byte
		targets, err = a.run.Output(ctx, 64<<10, "xclip", "-selection", "clipboard", "-o", "-t", "TARGETS")
		if err != nil {
			return clipboard.Content{}, classify(err)
		}
		list := strings.Split(strings.TrimSpace(string(targets)), "\n")
		sensitive = slices.Contains(list, clipboard.PasswordManagerHint)
		t := clipboard.PickTextType(list)
		if t == "" {
			return clipboard.Content{}, clipboard.ErrNoText
		}
		text, err = a.run.Output(ctx, a.maxBytes, "xclip", "-selection", "clipboard", "-o", "-t", t)
	}
	if err != nil {
		return clipboard.Content{}, classify(err)
	}
	if len(text) == 0 || !utf8.Valid(text) {
		return clipboard.Content{}, clipboard.ErrNoText
	}
	return clipboard.Content{Text: string(text), Sensitive: sensitive}, nil
}

// WriteText implements clipboard.Adapter.
func (a *Adapter) WriteText(ctx context.Context, text string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var err error
	switch a.tool {
	case XSel:
		err = a.run.Input(ctx, []byte(text), "xsel", "--clipboard", "--input")
	default:
		err = a.run.Input(ctx, []byte(text), "xclip", "-selection", "clipboard", "-i")
	}
	if err != nil {
		return classify(err)
	}
	return clipboard.WaitVisible(ctx, a.ReadText, text, 2*time.Second)
}

func classify(err error) error {
	if errors.Is(err, cmdutil.ErrOutputTooLarge) {
		return err
	}
	var ee *cmdutil.ExitError
	if errors.As(err, &ee) {
		msg := strings.ToLower(ee.Stderr)
		if strings.Contains(msg, "not available") || strings.Contains(msg, "no selection") {
			return clipboard.ErrNoText
		}
	}
	return fmt.Errorf("%w: %v", clipboard.ErrUnavailable, err)
}
