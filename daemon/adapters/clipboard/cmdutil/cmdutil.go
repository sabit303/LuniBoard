// Package cmdutil runs external clipboard tools (wl-clipboard, xclip, xsel)
// with bounded output and an explicit environment.
package cmdutil

import (
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// ErrOutputTooLarge is returned when a command prints more than the allowed bytes.
var ErrOutputTooLarge = errors.New("command output exceeds limit")

// Runner executes commands. It is an interface so adapters can be unit-tested.
type Runner interface {
	// Output runs a short-lived reader command and returns at most maxBytes of stdout.
	Output(ctx context.Context, maxBytes int64, name string, args ...string) ([]byte, error)
	// Input runs a writer command, feeding stdin. Writers such as wl-copy and
	// xclip fork a background process that owns the selection; Input returns
	// as soon as the direct child exits.
	Input(ctx context.Context, stdin []byte, name string, args ...string) error
	// Lines starts a long-running command and delivers stdout lines on the
	// returned channel (coalescing bursts), which is closed when the process
	// exits. The returned function reports the exit error after the close.
	Lines(ctx context.Context, name string, args ...string) (<-chan string, func() error, error)
}

// ExecRunner runs real processes.
type ExecRunner struct {
	// Env entries (KEY=VALUE) appended to the current environment.
	Env []string
}

func (r ExecRunner) env() []string {
	if len(r.Env) == 0 {
		return nil
	}
	return append(os.Environ(), r.Env...)
}

// Output implements Runner.
func (r ExecRunner) Output(ctx context.Context, maxBytes int64, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = r.env()
	stderr := &limitedBuffer{max: 4096}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	out, readErr := io.ReadAll(io.LimitReader(stdout, maxBytes+1))
	if int64(len(out)) > maxBytes {
		_ = cmd.Process.Kill()
		_ = cmd.Wait()
		return nil, ErrOutputTooLarge
	}
	if err := cmd.Wait(); err != nil {
		return nil, &ExitError{Cmd: name, Err: err, Stderr: strings.TrimSpace(stderr.String())}
	}
	return out, readErr
}

// Input implements Runner.
//
// Stdout goes to /dev/null and stderr to a temporary file: if either were a
// pipe, Wait would block until the forked selection owner exits.
func (r ExecRunner) Input(ctx context.Context, stdin []byte, name string, args ...string) error {
	errFile, err := os.CreateTemp("", "luniboard-stderr-*")
	if err != nil {
		return err
	}
	defer os.Remove(errFile.Name())
	defer errFile.Close()

	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = r.env()
	cmd.Stdin = bytes.NewReader(stdin)
	cmd.Stderr = errFile
	if err := cmd.Run(); err != nil {
		msg, _ := io.ReadAll(io.NewSectionReader(errFile, 0, 4096))
		return &ExitError{Cmd: name, Err: err, Stderr: strings.TrimSpace(string(msg))}
	}
	return nil
}

// Lines implements Runner.
func (r ExecRunner) Lines(ctx context.Context, name string, args ...string) (<-chan string, func() error, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Env = r.env()
	stderr := &limitedBuffer{max: 4096}
	cmd.Stderr = stderr
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	ch := make(chan string, 1)
	var exitErr error
	done := make(chan struct{})
	go func() {
		defer close(ch)
		sc := bufio.NewScanner(stdout)
		for sc.Scan() {
			select {
			case ch <- sc.Text():
			default: // a notification is already pending
			}
		}
		if err := cmd.Wait(); err != nil {
			exitErr = &ExitError{Cmd: name, Err: err, Stderr: strings.TrimSpace(stderr.String())}
		}
		close(done)
	}()
	return ch, func() error { <-done; return exitErr }, nil
}

// ExitError wraps a failed command with its stderr.
type ExitError struct {
	Cmd    string
	Err    error
	Stderr string
}

func (e *ExitError) Error() string {
	if e.Stderr != "" {
		return fmt.Sprintf("%s: %v: %s", e.Cmd, e.Err, e.Stderr)
	}
	return fmt.Sprintf("%s: %v", e.Cmd, e.Err)
}

func (e *ExitError) Unwrap() error { return e.Err }

type limitedBuffer struct {
	bytes.Buffer
	max int
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	if room := b.max - b.Len(); room > 0 {
		if len(p) > room {
			b.Buffer.Write(p[:room])
		} else {
			b.Buffer.Write(p)
		}
	}
	return len(p), nil
}
