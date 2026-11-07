// Package clipboard selects the platform clipboard adapter for the current
// session and turns clipboard changes into calls on the synchronization engine.
package clipboard

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"

	adapter "github.com/luniboard/luniboard/daemon/adapters/clipboard"
	"github.com/luniboard/luniboard/daemon/adapters/clipboard/cmdutil"
	"github.com/luniboard/luniboard/daemon/adapters/clipboard/wayland"
	"github.com/luniboard/luniboard/daemon/adapters/clipboard/x11"
)

// Backend is a detected clipboard integration.
type Backend struct {
	// Adapter reads and writes the clipboard.
	Adapter adapter.Adapter
	// Watcher, when non-nil, provides change notifications.
	Watcher adapter.Watcher
	// PollFallback is used for polling when Watcher is nil or fails.
	PollFallback adapter.Adapter
	// Description explains the choice for status output and logs.
	Description string
}

// ErrNoBackend means no usable clipboard integration was found.
var ErrNoBackend = errors.New("no usable clipboard backend found")

// Env abstracts the process environment for detection (tests substitute it).
type Env struct {
	Getenv   func(string) string
	LookPath func(string) (string, error)
	Glob     func(string) ([]string, error)
	GOOS     string
}

// OSEnv returns the real environment.
func OSEnv() Env {
	return Env{Getenv: os.Getenv, LookPath: exec.LookPath, Glob: filepath.Glob, GOOS: runtime.GOOS}
}

func (e Env) has(tool string) bool {
	_, err := e.LookPath(tool)
	return err == nil
}

// Detect chooses a backend. backend is "auto", "wayland", "x11" or "windows".
func Detect(backend string, env Env, maxBytes int64) (Backend, error) {
	if backend == "windows" && env.GOOS != "windows" {
		return Backend{}, fmt.Errorf("%w: windows backend is only available on Windows", ErrNoBackend)
	}
	switch env.GOOS {
	case "windows":
		if backend != "auto" && backend != "windows" {
			return Backend{}, fmt.Errorf("%w: %s backend is not supported on Windows", ErrNoBackend, backend)
		}
		return detectWindows(maxBytes)
	case "linux", "freebsd", "openbsd", "netbsd", "dragonfly":
	default:
		return Backend{}, fmt.Errorf("%w: native clipboard support for %s is not implemented yet", ErrNoBackend, env.GOOS)
	}

	wl, wlErr := detectWayland(env, maxBytes)
	xb, xErr := detectX11(env, maxBytes)

	switch backend {
	case "wayland":
		if wlErr != nil {
			return Backend{}, wlErr
		}
		return wl, nil
	case "x11":
		if xErr != nil {
			return Backend{}, xErr
		}
		return xb, nil
	}

	session := env.Getenv("XDG_SESSION_TYPE")
	preferX11 := session == "x11" || (session != "wayland" && env.Getenv("WAYLAND_DISPLAY") == "" && env.Getenv("DISPLAY") != "")
	if preferX11 && xErr == nil {
		return xb, nil
	}
	if wlErr == nil {
		if xErr == nil {
			// On compositors without data-control (notably GNOME/Mutter), polling
			// through XWayland avoids wl-paste's focus-stealing fallback.
			wl.PollFallback = xb.Adapter
			wl.Description += "; polling fallback via XWayland"
		}
		return wl, nil
	}
	if xErr == nil {
		return xb, nil
	}
	return Backend{}, fmt.Errorf("%w (wayland: %v; x11: %v)", ErrNoBackend, wlErr, xErr)
}

func detectWayland(env Env, maxBytes int64) (Backend, error) {
	for _, t := range wayland.Tools {
		if !env.has(t) {
			return Backend{}, fmt.Errorf("%s not installed (package wl-clipboard)", t)
		}
	}
	var extra []string
	display := env.Getenv("WAYLAND_DISPLAY")
	if display == "" {
		// systemd user services often lack WAYLAND_DISPLAY; look for the socket.
		rt := env.Getenv("XDG_RUNTIME_DIR")
		if rt == "" {
			return Backend{}, errors.New("WAYLAND_DISPLAY and XDG_RUNTIME_DIR are not set")
		}
		matches, _ := env.Glob(filepath.Join(rt, "wayland-[0-9]*"))
		var sockets []string
		for _, m := range matches {
			if filepath.Ext(m) != ".lock" {
				sockets = append(sockets, filepath.Base(m))
			}
		}
		if len(sockets) != 1 {
			return Backend{}, fmt.Errorf("WAYLAND_DISPLAY is not set and %d Wayland sockets were found", len(sockets))
		}
		display = sockets[0]
		extra = append(extra, "WAYLAND_DISPLAY="+display)
	}
	a := wayland.New(cmdutil.ExecRunner{Env: extra}, maxBytes)
	return Backend{
		Adapter:      a,
		Watcher:      a,
		PollFallback: a,
		Description:  "wayland (wl-clipboard, display " + display + ")",
	}, nil
}

func detectX11(env Env, maxBytes int64) (Backend, error) {
	display := env.Getenv("DISPLAY")
	if display == "" {
		return Backend{}, errors.New("DISPLAY is not set")
	}
	var tool x11.Tool
	switch {
	case env.has(string(x11.XClip)):
		tool = x11.XClip
	case env.has(string(x11.XSel)):
		tool = x11.XSel
	default:
		return Backend{}, errors.New("neither xclip nor xsel is installed")
	}
	a := x11.New(cmdutil.ExecRunner{}, tool, maxBytes)
	return Backend{
		Adapter:      a,
		PollFallback: a,
		Description:  "x11 (" + string(tool) + ", display " + display + ")",
	}, nil
}
