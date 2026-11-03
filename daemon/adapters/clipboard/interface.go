// Package clipboard defines the contract between the platform-independent core
// and operating-system clipboard integrations. Implementations live in the
// sub-packages (wayland, x11, mock, ...).
package clipboard

import (
	"context"
	"errors"
	"fmt"
	"time"
)

var (
	// ErrNoText means the clipboard is empty or holds only non-text data.
	ErrNoText = errors.New("clipboard does not contain text")
	// ErrUnavailable means the backend cannot reach the clipboard (no session, tool missing, ...).
	ErrUnavailable = errors.New("clipboard backend unavailable")
)

// Content is text read from the clipboard.
type Content struct {
	Text string
	// Sensitive is true when the owning application marked the content as a
	// secret (e.g. the x-kde-passwordManagerHint MIME type used by KeePassXC).
	Sensitive bool
}

// Adapter reads and writes the system clipboard.
//
// WriteText must not return before the new content is visible to ReadText,
// so that the core can reliably suppress the echo of its own writes.
type Adapter interface {
	// Name identifies the backend in logs and status output.
	Name() string
	ReadText(ctx context.Context) (Content, error)
	WriteText(ctx context.Context, text string) error
}

// Watcher is implemented by adapters that can be notified of clipboard
// changes instead of being polled.
type Watcher interface {
	// Watch blocks, calling notify whenever the clipboard may have changed.
	// It returns nil when ctx is done and an error when watching fails, in
	// which case the caller should fall back to polling.
	Watch(ctx context.Context, notify func()) error
}

// PasswordManagerHint is the MIME type/target password managers (KeePassXC,
// KDE apps, ...) add to mark clipboard content as secret.
const PasswordManagerHint = "x-kde-passwordManagerHint"

// PickTextType returns the preferred text MIME type/X11 target from offered, or "".
func PickTextType(offered []string) string {
	for _, want := range []string{"text/plain;charset=utf-8", "UTF8_STRING", "text/plain", "STRING", "TEXT"} {
		for _, o := range offered {
			if o == want {
				return o
			}
		}
	}
	return ""
}

// WaitVisible polls read until it returns text, so writers can guarantee the
// "visible before return" contract even with tools that fork before owning
// the selection.
func WaitVisible(ctx context.Context, read func(context.Context) (Content, error), text string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		c, err := read(ctx)
		if err == nil && c.Text == text {
			return nil
		}
		if time.Now().After(deadline) {
			if err == nil {
				err = errors.New("clipboard content did not change")
			}
			return fmt.Errorf("verify clipboard write: %w", err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(20 * time.Millisecond):
		}
	}
}
