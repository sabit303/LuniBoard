//go:build unix && !linux

package api

import "syscall"

func umask(mask int) int { return syscall.Umask(mask) }
