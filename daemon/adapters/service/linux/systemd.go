// Package linux installs the daemon as a systemd user service. It works on
// any systemd-based distribution and does not depend on a desktop environment.
package linux

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// UnitName is the systemd user unit name.
const UnitName = "universal-clipboard.service"

// UnitOptions parameterize the generated unit.
type UnitOptions struct {
	// Executable is the absolute path of the universal-clipboard binary.
	Executable string
	// Home, when set, is exported as UCLIP_HOME.
	Home string
}

// Unit renders the unit file.
func Unit(o UnitOptions) string {
	var b strings.Builder
	b.WriteString(`[Unit]
Description=Universal Clipboard daemon (LAN clipboard synchronization)
Documentation=https://github.com/universal-clipboard/universal-clipboard
After=network-online.target graphical-session.target
Wants=network-online.target

[Service]
Type=simple
`)
	fmt.Fprintf(&b, "ExecStart=%s daemon\n", systemdQuote(o.Executable))
	if o.Home != "" {
		fmt.Fprintf(&b, "Environment=%s\n", systemdQuote("UCLIP_HOME="+o.Home))
	}
	b.WriteString(`Restart=on-failure
RestartSec=3
NoNewPrivileges=yes

[Install]
WantedBy=default.target
`)
	return b.String()
}

// systemdQuote quotes a value for ExecStart/Environment when it contains spaces.
func systemdQuote(s string) string {
	if !strings.ContainsAny(s, " \t\"\\") {
		return s
	}
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}

// UnitPath returns ~/.config/systemd/user/universal-clipboard.service (respecting XDG_CONFIG_HOME).
func UnitPath() (string, error) {
	base := os.Getenv("XDG_CONFIG_HOME")
	if base == "" || !filepath.IsAbs(base) {
		home, err := os.UserHomeDir()
		if err != nil {
			return "", err
		}
		base = filepath.Join(home, ".config")
	}
	return filepath.Join(base, "systemd", "user", UnitName), nil
}

// Available reports whether a systemd user manager can be controlled.
func Available() bool {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return false
	}
	return systemctl(context.Background(), "show-environment") == nil
}

// Installed reports whether the unit file exists.
func Installed() bool {
	p, err := UnitPath()
	if err != nil {
		return false
	}
	_, err = os.Stat(p)
	return err == nil
}

// Active reports whether the service is running.
func Active() bool {
	return systemctl(context.Background(), "is-active", "--quiet", UnitName) == nil
}

// Install writes the unit, reloads systemd and enables + starts the service.
func Install(o UnitOptions) (string, error) {
	if !filepath.IsAbs(o.Executable) {
		return "", errors.New("executable path must be absolute")
	}
	p, err := UnitPath()
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(p, []byte(Unit(o)), 0o644); err != nil {
		return "", err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := systemctl(ctx, "daemon-reload"); err != nil {
		return p, err
	}
	return p, systemctl(ctx, "enable", "--now", UnitName)
}

// Uninstall stops, disables and removes the unit.
func Uninstall() error {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	_ = systemctl(ctx, "disable", "--now", UnitName)
	p, err := UnitPath()
	if err != nil {
		return err
	}
	if err := os.Remove(p); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return systemctl(ctx, "daemon-reload")
}

// Start starts the service.
func Start() error { return systemctl(context.Background(), "start", UnitName) }

// Stop stops the service.
func Stop() error { return systemctl(context.Background(), "stop", UnitName) }

func systemctl(ctx context.Context, args ...string) error {
	cmd := exec.CommandContext(ctx, "systemctl", append([]string{"--user"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("systemctl --user %s: %v %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
	}
	return nil
}
