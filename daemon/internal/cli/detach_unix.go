//go:build unix

package cli

import (
	"os/exec"
	"syscall"
)

// detach starts the child in its own session so it survives the terminal.
func detach(cmd *exec.Cmd) { cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true} }
