// Package windows implements native Unicode clipboard access on Windows.
// Changes are detected by the clipboard service's polling loop.
package windows

import (
	"context"
	"encoding/binary"
	"fmt"
	"runtime"
	"time"

	"github.com/luniboard/luniboard/daemon/adapters/clipboard"
)

const unicodeText = 13 // CF_UNICODETEXT

// native isolates Win32 calls so ownership and failure paths can be tested
// without modifying the system clipboard.
type native interface {
	createWindow() (uintptr, error)
	destroyWindow(uintptr) error
	openClipboard(uintptr) error
	closeClipboard() error
	formatAvailable(uint32) bool
	readData(format uint32, maxBytes int64) ([]byte, error)
	allocData([]byte) (uintptr, error)
	freeData(uintptr) error
	emptyClipboard() error
	setData(format uint32, memory uintptr) error
}

// Adapter reads and writes CF_UNICODETEXT in the current user's window station.
// It owns no persistent windows or goroutines and is safe for concurrent use.
type Adapter struct {
	api              native
	maxBytes         int64
	sensitiveFormats []uint32
	privacyFormats   []uint32
}

var _ clipboard.Adapter = (*Adapter)(nil)

// Name implements clipboard.Adapter.
func (a *Adapter) Name() string { return "windows" }

// ReadText implements clipboard.Adapter.
func (a *Adapter) ReadText(ctx context.Context) (content clipboard.Content, err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	// OpenClipboard/CloseClipboard must run on the same OS thread.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	if err = a.open(ctx, 0); err != nil {
		return content, err
	}
	defer func() {
		if closeErr := a.api.closeClipboard(); closeErr != nil && err == nil {
			err = unavailable("CloseClipboard", closeErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return clipboard.Content{}, unavailable("read clipboard", err)
	}
	if !a.api.formatAvailable(unicodeText) {
		return content, clipboard.ErrNoText
	}
	// UTF-16 needs at most two bytes per UTF-8 byte, plus its NUL terminator.
	data, err := a.read(ctx, unicodeText, 2*a.maxBytes+2)
	if err != nil {
		return content, unavailable("GetClipboardData", err)
	}
	text, err := decodeText(data, a.maxBytes)
	if err != nil {
		return content, err
	}
	content.Text = text
	for _, format := range a.sensitiveFormats {
		if a.api.formatAvailable(format) {
			content.Sensitive = true
		}
	}
	for _, format := range a.privacyFormats {
		if !a.api.formatAvailable(format) {
			continue
		}
		flag, readErr := a.read(ctx, format, 4)
		if readErr != nil {
			return clipboard.Content{}, unavailable("read clipboard privacy flag", readErr)
		}
		// Treat malformed restrictions conservatively rather than syncing a secret.
		if len(flag) < 4 || binary.LittleEndian.Uint32(flag) == 0 {
			content.Sensitive = true
		}
	}
	if err := ctx.Err(); err != nil {
		return clipboard.Content{}, unavailable("read clipboard", err)
	}
	return content, nil
}

func (a *Adapter) read(ctx context.Context, format uint32, maxBytes int64) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	data, err := a.api.readData(format, maxBytes)
	// GetClipboardData can block while another application renders its data.
	// Win32 cannot cancel that call; discard its result if the context expired.
	if ctxErr := ctx.Err(); ctxErr != nil {
		return nil, ctxErr
	}
	return data, err
}

// WriteText implements clipboard.Adapter. All data is rendered eagerly so it
// remains available after the temporary owner window is destroyed.
func (a *Adapter) WriteText(ctx context.Context, text string) (err error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := ctx.Err(); err != nil {
		return err
	}
	data, err := encodeText(text, a.maxBytes)
	if err != nil {
		return err
	}
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	owner, err := a.api.createWindow()
	if err != nil {
		return unavailable("CreateWindowExW", err)
	}
	defer func() {
		if destroyErr := a.api.destroyWindow(owner); destroyErr != nil && err == nil {
			err = unavailable("DestroyWindow", destroyErr)
		}
	}()
	// Prepare memory before clearing the user's existing clipboard.
	memory, err := a.api.allocData(data)
	if err != nil {
		return unavailable("GlobalAlloc", err)
	}
	defer func() {
		if memory != 0 {
			_ = a.api.freeData(memory)
		}
	}()
	if err := a.open(ctx, owner); err != nil {
		return err
	}
	defer func() {
		if closeErr := a.api.closeClipboard(); closeErr != nil && err == nil {
			err = unavailable("CloseClipboard", closeErr)
		}
	}()
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := a.api.emptyClipboard(); err != nil {
		return unavailable("EmptyClipboard", err)
	}
	if err := a.api.setData(unicodeText, memory); err != nil {
		return unavailable("SetClipboardData", err)
	}
	memory = 0 // Windows owns the allocation after successful SetClipboardData.
	return nil
}

func (a *Adapter) open(ctx context.Context, owner uintptr) error {
	for {
		if err := ctx.Err(); err != nil {
			return unavailable("OpenClipboard", err)
		}
		if err := a.api.openClipboard(owner); err == nil {
			return nil
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return unavailable("OpenClipboard", ctx.Err())
		case <-timer.C:
		}
	}
}

func unavailable(operation string, err error) error {
	return fmt.Errorf("%w: %s: %w", clipboard.ErrUnavailable, operation, err)
}
