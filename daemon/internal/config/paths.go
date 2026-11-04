package config

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
)

// AppDirName is the directory name used under the platform's standard locations.
const AppDirName = "luniboard"

// HomeEnv overrides all locations with a single root directory. It is useful
// for running several daemons on one machine (tests, development).
const HomeEnv = "LUNIBOARD_HOME"

// Paths are the on-disk locations used by the daemon.
type Paths struct {
	// ConfigDir holds config.toml.
	ConfigDir string
	// DataDir holds the identity key pair, the device registry and persisted history.
	DataDir string
	// StateDir holds logs.
	StateDir string
	// RuntimeDir holds the local API socket.
	RuntimeDir string
}

// ConfigFile returns the configuration file path.
func (p Paths) ConfigFile() string { return filepath.Join(p.ConfigDir, "config.toml") }

// SocketPath returns the local API unix socket path.
func (p Paths) SocketPath() string { return filepath.Join(p.RuntimeDir, "daemon.sock") }

// LogFile returns the daemon log path.
func (p Paths) LogFile() string { return filepath.Join(p.StateDir, "daemon.log") }

// IdentityDir returns the directory holding the TLS identity.
func (p Paths) IdentityDir() string { return filepath.Join(p.DataDir, "identity") }

// DevicesFile returns the trusted device registry path.
func (p Paths) DevicesFile() string { return filepath.Join(p.DataDir, "devices.json") }

// HistoryFile returns the persisted history path.
func (p Paths) HistoryFile() string { return filepath.Join(p.DataDir, "history.json") }

// Ensure creates all directories with owner-only permissions.
func (p Paths) Ensure() error {
	for _, d := range []string{p.ConfigDir, p.DataDir, p.StateDir, p.RuntimeDir} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return fmt.Errorf("create %s: %w", d, err)
		}
		// MkdirAll does not change existing directories; tighten if needed.
		if runtime.GOOS != "windows" {
			if err := os.Chmod(d, 0o700); err != nil {
				return fmt.Errorf("chmod %s: %w", d, err)
			}
		}
	}
	return nil
}

// ResolvePaths returns the platform-specific default locations, or locations
// under home when non-empty (or when LUNIBOARD_HOME is set).
func ResolvePaths(home string) (Paths, error) {
	if home == "" {
		home = os.Getenv(HomeEnv)
	}
	if home != "" {
		abs, err := filepath.Abs(home)
		if err != nil {
			return Paths{}, err
		}
		return Paths{
			ConfigDir:  filepath.Join(abs, "config"),
			DataDir:    filepath.Join(abs, "data"),
			StateDir:   filepath.Join(abs, "state"),
			RuntimeDir: filepath.Join(abs, "run"),
		}, nil
	}

	userHome, err := os.UserHomeDir()
	if err != nil {
		return Paths{}, fmt.Errorf("determine home directory: %w", err)
	}
	cfgBase, err := os.UserConfigDir()
	if err != nil {
		return Paths{}, fmt.Errorf("determine config directory: %w", err)
	}

	switch runtime.GOOS {
	case "windows":
		local := os.Getenv("LocalAppData")
		if local == "" {
			return Paths{}, errors.New("LocalAppData is not set")
		}
		base := filepath.Join(local, "LuniBoard")
		return Paths{
			ConfigDir:  filepath.Join(cfgBase, "LuniBoard"),
			DataDir:    base,
			StateDir:   filepath.Join(base, "logs"),
			RuntimeDir: filepath.Join(base, "run"),
		}, nil
	case "darwin":
		base := filepath.Join(cfgBase, "LuniBoard")
		return Paths{
			ConfigDir:  base,
			DataDir:    base,
			StateDir:   filepath.Join(userHome, "Library", "Logs", "LuniBoard"),
			RuntimeDir: filepath.Join(base, "run"),
		}, nil
	default:
		return Paths{
			ConfigDir:  filepath.Join(cfgBase, AppDirName),
			DataDir:    filepath.Join(xdg("XDG_DATA_HOME", filepath.Join(userHome, ".local", "share")), AppDirName),
			StateDir:   filepath.Join(xdg("XDG_STATE_HOME", filepath.Join(userHome, ".local", "state")), AppDirName),
			RuntimeDir: runtimeDir(),
		}, nil
	}
}

func xdg(env, fallback string) string {
	if v := os.Getenv(env); v != "" && filepath.IsAbs(v) {
		return v
	}
	return fallback
}

func runtimeDir() string {
	if v := os.Getenv("XDG_RUNTIME_DIR"); v != "" && filepath.IsAbs(v) {
		return filepath.Join(v, AppDirName)
	}
	return filepath.Join(os.TempDir(), AppDirName+"-"+strconv.Itoa(os.Getuid()))
}
