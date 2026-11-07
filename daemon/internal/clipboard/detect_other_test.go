//go:build !windows

package clipboard

import (
	"errors"
	"strings"
	"testing"
)

func TestDetectWindowsUnavailable(t *testing.T) {
	for _, backend := range []string{"auto", "windows"} {
		t.Run(backend, func(t *testing.T) {
			_, err := Detect(backend, Env{GOOS: "windows"}, 1024)
			if !errors.Is(err, ErrNoBackend) || !strings.Contains(err.Error(), "only available on Windows") {
				t.Fatalf("expected Windows-only backend error, got %v", err)
			}
		})
	}
}
