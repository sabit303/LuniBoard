package cmdutil

import (
	"context"
	"strings"
	"sync"
)

// FakeRunner is a scriptable Runner for adapter unit tests.
type FakeRunner struct {
	mu sync.Mutex
	// Responses maps "name arg1 arg2..." to a canned result.
	Responses map[string]FakeResult
	// OnInput is called for Input invocations.
	OnInput func(cmdline string, stdin []byte) error
	// Calls records every command line.
	Calls []string
}

// FakeResult is a canned command result.
type FakeResult struct {
	Stdout string
	Err    error
}

func cmdline(name string, args []string) string {
	return strings.Join(append([]string{name}, args...), " ")
}

// Output implements Runner.
func (f *FakeRunner) Output(_ context.Context, maxBytes int64, name string, args ...string) ([]byte, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	cl := cmdline(name, args)
	f.Calls = append(f.Calls, cl)
	r, ok := f.Responses[cl]
	if !ok {
		return nil, &ExitError{Cmd: name, Err: errUnknown, Stderr: "unexpected command: " + cl}
	}
	if r.Err != nil {
		return nil, r.Err
	}
	if int64(len(r.Stdout)) > maxBytes {
		return nil, ErrOutputTooLarge
	}
	return []byte(r.Stdout), nil
}

// Input implements Runner.
func (f *FakeRunner) Input(_ context.Context, stdin []byte, name string, args ...string) error {
	f.mu.Lock()
	cl := cmdline(name, args)
	f.Calls = append(f.Calls, cl)
	fn := f.OnInput
	f.mu.Unlock()
	if fn != nil {
		return fn(cl, stdin)
	}
	return nil
}

// Lines implements Runner by emitting the configured stdout lines once.
func (f *FakeRunner) Lines(ctx context.Context, name string, args ...string) (<-chan string, func() error, error) {
	out, err := f.Output(ctx, 1<<20, name, args...)
	if err != nil {
		return nil, nil, err
	}
	ch := make(chan string, 64)
	for _, l := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if l != "" {
			ch <- l
		}
	}
	close(ch)
	return ch, func() error { return nil }, nil
}

// Set replaces a response (safe for concurrent use).
func (f *FakeRunner) Set(cl string, r FakeResult) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Responses == nil {
		f.Responses = make(map[string]FakeResult)
	}
	f.Responses[cl] = r
}

type fakeErr string

func (e fakeErr) Error() string { return string(e) }

const errUnknown = fakeErr("exit status 1")
