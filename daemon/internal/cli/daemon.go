package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	servicelinux "github.com/luniboard/luniboard/daemon/adapters/service/linux"
	"github.com/luniboard/luniboard/daemon/internal/application"
	"github.com/luniboard/luniboard/daemon/internal/config"
	"github.com/luniboard/luniboard/daemon/internal/logging"
)

func runDaemon(ctx context.Context, g *globals, args []string) error {
	fs := newFlags("daemon", g)
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	var migrated []string
	if g.home == "" && os.Getenv(config.HomeEnv) == "" {
		var err error
		if migrated, err = config.MigrateLegacyDirs(g.paths); err != nil {
			fmt.Fprintln(g.env.Stderr, "warning: migrating old universal-clipboard directories:", err)
		}
	}
	cfg, err := config.Load(g.paths.ConfigFile())
	if err != nil {
		return err
	}
	if err := g.paths.Ensure(); err != nil {
		return err
	}
	logFile := ""
	if cfg.Logging.File {
		logFile = g.paths.LogFile()
	}
	logger, closer, err := logging.New(logging.Options{
		Level: cfg.Logging.Level, Format: cfg.Logging.Format, FilePath: logFile, Stderr: g.env.Stderr,
	})
	if err != nil {
		return err
	}
	defer closer.Close()
	for _, m := range migrated {
		logger.Info("migrated data from the old universal-clipboard location", "move", m)
	}

	app, err := application.New(application.Options{Paths: g.paths, Config: cfg, Logger: logger})
	if err != nil {
		logger.Error("startup failed", "error", err)
		return err
	}
	ctx, stop := signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := app.Run(ctx); err != nil {
		logger.Error("daemon failed", "error", err)
		return err
	}
	return nil
}

func runStart(ctx context.Context, g *globals, args []string) error {
	c := g.client()
	if c.Ping(time.Second) {
		g.printf("Daemon is already running.\n")
		return nil
	}
	if runtime.GOOS == "linux" && g.home == "" && os.Getenv(config.HomeEnv) == "" && servicelinux.Installed() && servicelinux.Available() {
		if err := servicelinux.Start(); err != nil {
			return err
		}
	} else {
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if err := g.paths.Ensure(); err != nil {
			return err
		}
		cmdArgs := []string{"daemon"}
		if g.home != "" {
			cmdArgs = append([]string{"--home", g.home}, cmdArgs...)
		}
		cmd := exec.Command(exe, cmdArgs...)
		cmd.Stdin, cmd.Stdout, cmd.Stderr = nil, nil, nil
		detach(cmd)
		if err := cmd.Start(); err != nil {
			return err
		}
		_ = cmd.Process.Release()
	}
	for i := 0; i < 50; i++ {
		if c.Ping(200 * time.Millisecond) {
			g.printf("Daemon started.\n")
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return fmt.Errorf("daemon did not become ready; see 'luniboard logs'")
}

func runStop(ctx context.Context, g *globals, args []string) error {
	if runtime.GOOS == "linux" && servicelinux.Installed() && servicelinux.Available() && servicelinux.Active() &&
		g.home == "" && os.Getenv(config.HomeEnv) == "" {
		if err := servicelinux.Stop(); err != nil {
			return err
		}
		g.printf("Daemon stopped.\n")
		return nil
	}
	c := g.client()
	cctx, cancel := timeoutCtx(ctx)
	defer cancel()
	if err := c.Shutdown(cctx); err != nil {
		return err
	}
	for i := 0; i < 50; i++ {
		if !c.Ping(200 * time.Millisecond) {
			g.printf("Daemon stopped.\n")
			return nil
		}
		time.Sleep(100 * time.Millisecond)
	}
	return errors.New("daemon did not stop in time")
}

func runService(ctx context.Context, g *globals, args []string) error {
	if runtime.GOOS != "linux" {
		return fmt.Errorf("service management for %s is not implemented yet", runtime.GOOS)
	}
	if len(args) != 1 {
		return usageError("usage: service install|uninstall|status")
	}
	switch args[0] {
	case "install":
		exe, err := os.Executable()
		if err != nil {
			return err
		}
		if exe, err = filepath.EvalSymlinks(exe); err != nil {
			return err
		}
		home := g.home
		if home == "" {
			home = os.Getenv(config.HomeEnv)
		}
		path, err := servicelinux.Install(servicelinux.UnitOptions{Executable: exe, Home: home})
		if err != nil {
			return err
		}
		g.printf("Installed %s and started it.\n", path)
		g.printf("The service starts automatically when you log in.\n")
		g.printf("If clipboard access fails on X11 or in minimal Wayland sessions, export the session environment:\n")
		g.printf("  systemctl --user import-environment DISPLAY WAYLAND_DISPLAY XAUTHORITY\n")
		return nil
	case "uninstall":
		if err := servicelinux.Uninstall(); err != nil {
			return err
		}
		g.printf("Service removed.\n")
		return nil
	case "status":
		path, _ := servicelinux.UnitPath()
		g.printf("unit file: %s (installed: %v)\n", path, servicelinux.Installed())
		g.printf("active:    %v\n", servicelinux.Active())
		return nil
	}
	return usageError("usage: service install|uninstall|status")
}

func runVersion(ctx context.Context, g *globals, args []string) error {
	g.printf("luniboard %s (%s/%s, %s)\n", application.Version, runtime.GOOS, runtime.GOARCH, runtime.Version())
	return nil
}
