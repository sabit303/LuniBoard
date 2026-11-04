package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
)

// legacyAppDirName is the directory name used before the daemon was renamed to luniboard.
const legacyAppDirName = "universal-clipboard"

// MigrateLegacyDirs moves configuration, identity, paired devices and logs
// from the pre-rename "universal-clipboard" directories to the current ones,
// so existing pairings keep working. Only directories that exist in the old
// location and not yet in the new one are moved. It does nothing when a
// custom home directory is in use or on non-Linux platforms.
func MigrateLegacyDirs(p Paths) ([]string, error) {
	if runtime.GOOS != "linux" && runtime.GOOS != "freebsd" {
		return nil, nil
	}
	var moved []string
	var errs []error
	for _, dir := range []string{p.ConfigDir, p.DataDir, p.StateDir} {
		if filepath.Base(dir) != AppDirName {
			continue
		}
		legacy := filepath.Join(filepath.Dir(dir), legacyAppDirName)
		if _, err := os.Stat(legacy); err != nil {
			continue
		}
		if _, err := os.Stat(dir); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			errs = append(errs, err)
			continue
		}
		if err := os.Rename(legacy, dir); err != nil {
			errs = append(errs, fmt.Errorf("move %s to %s: %w", legacy, dir, err))
			continue
		}
		moved = append(moved, legacy+" -> "+dir)
	}
	return moved, errors.Join(errs...)
}
