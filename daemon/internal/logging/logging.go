// Package logging configures structured logging for the daemon.
package logging

import (
	"fmt"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
)

// maxLogBytes triggers rotation of the log file at startup.
const maxLogBytes = 5 << 20

// Options controls logger construction.
type Options struct {
	Level  string
	Format string
	// FilePath, when non-empty, additionally writes logs to this file.
	FilePath string
	Stderr   io.Writer
}

// New builds a slog.Logger and returns a closer for the log file, if any.
func New(opts Options) (*slog.Logger, io.Closer, error) {
	var level slog.Level
	if err := level.UnmarshalText([]byte(strings.ToUpper(opts.Level))); err != nil {
		return nil, nil, fmt.Errorf("log level: %w", err)
	}
	out := opts.Stderr
	if out == nil {
		out = os.Stderr
	}
	var closer io.Closer = nopCloser{}
	if opts.FilePath != "" {
		f, err := openLogFile(opts.FilePath)
		if err != nil {
			return nil, nil, err
		}
		out = io.MultiWriter(out, f)
		closer = f
	}
	hopts := &slog.HandlerOptions{Level: level}
	var h slog.Handler
	if opts.Format == "json" {
		h = slog.NewJSONHandler(out, hopts)
	} else {
		h = slog.NewTextHandler(out, hopts)
	}
	return slog.New(h), closer, nil
}

func openLogFile(path string) (*os.File, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("create log dir: %w", err)
	}
	if st, err := os.Stat(path); err == nil && st.Size() > maxLogBytes {
		_ = os.Rename(path, path+".1")
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log file: %w", err)
	}
	return f, nil
}

// Discard returns a logger that drops everything; useful in tests.
func Discard() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

type nopCloser struct{}

func (nopCloser) Close() error { return nil }
