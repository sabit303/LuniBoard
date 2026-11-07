//go:build !windows

package clipboard

import "fmt"

func detectWindows(_ int64) (Backend, error) {
	return Backend{}, fmt.Errorf("%w: windows backend is only available on Windows", ErrNoBackend)
}
