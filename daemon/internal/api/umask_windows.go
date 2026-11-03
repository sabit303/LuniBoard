//go:build windows

package api

func umask(mask int) int { return 0 }
