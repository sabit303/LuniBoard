// Command luniboard is the LuniBoard daemon and its CLI.
package main

import (
	"context"
	"os"
	"os/signal"
	"syscall"

	"github.com/luniboard/luniboard/daemon/internal/cli"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := cli.Main(ctx, os.Args[1:], cli.Env{Stdin: os.Stdin, Stdout: os.Stdout, Stderr: os.Stderr})
	stop()
	os.Exit(code)
}
