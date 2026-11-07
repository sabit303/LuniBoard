//go:build windows

package clipboard

import (
	"fmt"

	"github.com/luniboard/luniboard/daemon/adapters/clipboard/windows"
)

func detectWindows(maxBytes int64) (Backend, error) {
	a, err := windows.New(maxBytes)
	if err != nil {
		return Backend{}, fmt.Errorf("%w: windows: %w", ErrNoBackend, err)
	}
	return Backend{
		Adapter:      a,
		PollFallback: a,
		Description:  "windows (CF_UNICODETEXT)",
	}, nil
}
