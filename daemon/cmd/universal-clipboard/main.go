// Command universal-clipboard is the Universal Clipboard daemon and its CLI.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/universal-clipboard/universal-clipboard/daemon/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Main(ctx, os.Args[1:], cli.Env{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr})
	stop()
	os.Exit(code)
}
