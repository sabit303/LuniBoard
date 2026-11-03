// Package mock provides an in-memory clipboard used by automated tests and by
// the "none" backend semantics in integration tests.
package mock

import (
	"context"
	"sync"

	"github.com/universal-clipboard/universal-clipboard/daemon/adapters/clipboard"
)

// Clipboard is an in-memory clipboard with change notifications.
type Clipboard struct {
	mu        sync.Mutex
	content   clipboard.Content
	has       bool
	writes    int
	watchers  []chan struct{}
	failWrite error
}

// New returns an empty clipboard.
func New() *Clipboard { return &Clipboard{} }

// Name implements clipboard.Adapter.
func (c *Clipboard) Name() string { return "mock" }

// ReadText implements clipboard.Adapter.
func (c *Clipboard) ReadText(context.Context) (clipboard.Content, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.has || c.content.Text == "" {
		return clipboard.Content{}, clipboard.ErrNoText
	}
	return c.content, nil
}

// WriteText implements clipboard.Adapter. Like a real clipboard it notifies
// watchers, which lets tests exercise echo suppression.
func (c *Clipboard) WriteText(_ context.Context, text string) error {
	c.mu.Lock()
	if c.failWrite != nil {
		err := c.failWrite
		c.mu.Unlock()
		return err
	}
	c.content = clipboard.Content{Text: text}
	c.has = true
	c.writes++
	c.mu.Unlock()
	c.notify()
	return nil
}

// Watch implements clipboard.Watcher.
func (c *Clipboard) Watch(ctx context.Context, notify func()) error {
	ch := make(chan struct{}, 1)
	c.mu.Lock()
	c.watchers = append(c.watchers, ch)
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		for i, w := range c.watchers {
			if w == ch {
				c.watchers = append(c.watchers[:i], c.watchers[i+1:]...)
				break
			}
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ch:
			notify()
		}
	}
}

// Copy simulates the user copying content in another application.
func (c *Clipboard) Copy(content clipboard.Content) {
	c.mu.Lock()
	c.content = content
	c.has = true
	c.mu.Unlock()
	c.notify()
}

// Text returns the current text.
func (c *Clipboard) Text() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.content.Text
}

// Writes returns how many times WriteText succeeded.
func (c *Clipboard) Writes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.writes
}

// FailWrites makes subsequent writes return err (nil restores normal behaviour).
func (c *Clipboard) FailWrites(err error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.failWrite = err
}

func (c *Clipboard) notify() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, w := range c.watchers {
		select {
		case w <- struct{}{}:
		default:
		}
	}
}
