// Package config defines the daemon configuration, its defaults and on-disk locations.
package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pelletier/go-toml/v2"

	"github.com/luniboard/luniboard/daemon/internal/storage"
	"github.com/luniboard/luniboard/daemon/pkg/protocol"
)

// DefaultPort is the default TCP port for the LAN listener.
const DefaultPort = 47823

// Duration wraps time.Duration so it can be written as "750ms" or "2m" in TOML.
type Duration struct{ time.Duration }

// UnmarshalText implements encoding.TextUnmarshaler.
func (d *Duration) UnmarshalText(b []byte) error {
	v, err := time.ParseDuration(string(b))
	if err != nil {
		return err
	}
	d.Duration = v
	return nil
}

// MarshalText implements encoding.TextMarshaler.
func (d Duration) MarshalText() ([]byte, error) { return []byte(d.String()), nil }

// Config is the complete daemon configuration.
type Config struct {
	Device    DeviceConfig    `toml:"device"`
	Network   NetworkConfig   `toml:"network"`
	Discovery DiscoveryConfig `toml:"discovery"`
	Clipboard ClipboardConfig `toml:"clipboard"`
	Sync      SyncConfig      `toml:"sync"`
	History   HistoryConfig   `toml:"history"`
	Pairing   PairingConfig   `toml:"pairing"`
	Security  SecurityConfig  `toml:"security"`
	Logging   LoggingConfig   `toml:"logging"`
}

type DeviceConfig struct {
	// Name shown to other devices. Defaults to the host name.
	Name string `toml:"name"`
}

type NetworkConfig struct {
	// ListenAddress is the IP to bind; empty binds all interfaces.
	ListenAddress string `toml:"listen_address"`
	Port          int    `toml:"port"`
	// MaxConnections bounds concurrent inbound TCP connections.
	MaxConnections int `toml:"max_connections"`
}

type DiscoveryConfig struct {
	Enabled bool `toml:"enabled"`
	// Advertise publishes this device via mDNS; Enabled alone only browses.
	Advertise bool `toml:"advertise"`
	// Interfaces restricts mDNS to the named interfaces; empty means all multicast interfaces.
	Interfaces []string `toml:"interfaces"`
}

type ClipboardConfig struct {
	// Backend is auto, wayland, x11, windows or none.
	Backend      string   `toml:"backend"`
	PollInterval Duration `toml:"poll_interval"`
	// SkipSensitive ignores password-manager hints and Windows clipboard exclusion metadata.
	SkipSensitive bool `toml:"skip_sensitive"`
}

type SyncConfig struct {
	// Enabled is the persistent default; pause/resume toggles it at runtime.
	Enabled bool `toml:"enabled"`
	// AutoSend broadcasts local clipboard changes automatically.
	AutoSend bool `toml:"auto_send"`
	// Receive applies clipboard events from peers.
	Receive              bool     `toml:"receive"`
	MaxPayloadBytes      int64    `toml:"max_payload_bytes"`
	MaxEventAge          Duration `toml:"max_event_age"`
	OrderingTolerance    Duration `toml:"ordering_tolerance"`
	ExcludedContentTypes []string `toml:"excluded_content_types"`
	// Relay forwards events received from one peer to other peers.
	Relay bool `toml:"relay"`
}

type HistoryConfig struct {
	Enabled    bool `toml:"enabled"`
	Persist    bool `toml:"persist"`
	MaxEntries int  `toml:"max_entries"`
}

type PairingConfig struct {
	Window      Duration `toml:"window"`
	MaxAttempts int      `toml:"max_attempts"`
}

type SecurityConfig struct {
	EventsPerSecond float64 `toml:"events_per_second"`
	EventBurst      int     `toml:"event_burst"`
}

type LoggingConfig struct {
	Level  string `toml:"level"`
	Format string `toml:"format"`
	File   bool   `toml:"file"`
}

// Default returns the default configuration.
func Default() Config {
	host, _ := os.Hostname()
	if host == "" {
		host = "LuniBoard device"
	}
	return Config{
		Device:    DeviceConfig{Name: truncateRunes(host, protocol.MaxDeviceNameRunes)},
		Network:   NetworkConfig{Port: DefaultPort, MaxConnections: 64},
		Discovery: DiscoveryConfig{Enabled: true, Advertise: true},
		Clipboard: ClipboardConfig{
			Backend:       "auto",
			PollInterval:  Duration{750 * time.Millisecond},
			SkipSensitive: true,
		},
		Sync: SyncConfig{
			Enabled:           true,
			AutoSend:          true,
			Receive:           true,
			MaxPayloadBytes:   1 << 20,
			MaxEventAge:       Duration{2 * time.Minute},
			OrderingTolerance: Duration{2 * time.Second},
		},
		History:  HistoryConfig{Enabled: true, Persist: false, MaxEntries: 50},
		Pairing:  PairingConfig{Window: Duration{2 * time.Minute}, MaxAttempts: 5},
		Security: SecurityConfig{EventsPerSecond: 5, EventBurst: 20},
		Logging:  LoggingConfig{Level: "info", Format: "text", File: true},
	}
}

func truncateRunes(s string, n int) string {
	r := []rune(s)
	if len(r) > n {
		return string(r[:n])
	}
	return s
}

// Validate checks semantic constraints.
func (c Config) Validate() error {
	var errs []error
	if err := protocol.ValidateDeviceInfo(protocol.DeviceInfo{ID: "x", Name: c.Device.Name, Platform: protocol.PlatformOther}); err != nil {
		errs = append(errs, fmt.Errorf("device.name: %w", err))
	}
	if c.Network.Port < 0 || c.Network.Port > 65535 {
		errs = append(errs, errors.New("network.port must be 0-65535"))
	}
	if c.Network.MaxConnections < 1 {
		errs = append(errs, errors.New("network.max_connections must be >= 1"))
	}
	switch c.Clipboard.Backend {
	case "auto", "wayland", "x11", "windows", "none":
	default:
		errs = append(errs, fmt.Errorf("clipboard.backend %q must be auto, wayland, x11, windows or none", c.Clipboard.Backend))
	}
	if c.Clipboard.PollInterval.Duration < 100*time.Millisecond {
		errs = append(errs, errors.New("clipboard.poll_interval must be >= 100ms"))
	}
	if c.Sync.MaxPayloadBytes < 1 || c.Sync.MaxPayloadBytes > protocol.HardMaxPayloadBytes {
		errs = append(errs, fmt.Errorf("sync.max_payload_bytes must be 1-%d", protocol.HardMaxPayloadBytes))
	}
	if c.Sync.MaxEventAge.Duration < time.Second {
		errs = append(errs, errors.New("sync.max_event_age must be >= 1s"))
	}
	if c.Sync.OrderingTolerance.Duration < 0 {
		errs = append(errs, errors.New("sync.ordering_tolerance must be >= 0"))
	}
	for _, t := range c.Sync.ExcludedContentTypes {
		if !protocol.ContentType(t).Valid() {
			errs = append(errs, fmt.Errorf("sync.excluded_content_types: unknown type %q", t))
		}
	}
	if c.History.MaxEntries < 0 || c.History.MaxEntries > 10000 {
		errs = append(errs, errors.New("history.max_entries must be 0-10000"))
	}
	if c.Pairing.Window.Duration < 10*time.Second || c.Pairing.Window.Duration > time.Hour {
		errs = append(errs, errors.New("pairing.window must be between 10s and 1h"))
	}
	if c.Pairing.MaxAttempts < 1 || c.Pairing.MaxAttempts > 100 {
		errs = append(errs, errors.New("pairing.max_attempts must be 1-100"))
	}
	if c.Security.EventsPerSecond <= 0 || c.Security.EventBurst < 1 {
		errs = append(errs, errors.New("security.events_per_second and event_burst must be positive"))
	}
	switch strings.ToLower(c.Logging.Level) {
	case "debug", "info", "warn", "error":
	default:
		errs = append(errs, fmt.Errorf("logging.level %q must be debug, info, warn or error", c.Logging.Level))
	}
	switch c.Logging.Format {
	case "text", "json":
	default:
		errs = append(errs, fmt.Errorf("logging.format %q must be text or json", c.Logging.Format))
	}
	return errors.Join(errs...)
}

// Load reads the configuration file at path, applying defaults for missing
// keys. A missing file yields the defaults. Unknown keys are rejected so typos
// are not silently ignored.
func Load(path string) (Config, error) {
	cfg := Default()
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return cfg, cfg.Validate()
	}
	if err != nil {
		return cfg, fmt.Errorf("read config: %w", err)
	}
	dec := toml.NewDecoder(bytes.NewReader(b)).DisallowUnknownFields()
	if err := dec.Decode(&cfg); err != nil {
		var strict *toml.StrictMissingError
		if errors.As(err, &strict) {
			return cfg, fmt.Errorf("config %s: %s", path, strict.String())
		}
		return cfg, fmt.Errorf("config %s: %w", path, err)
	}
	if err := cfg.Validate(); err != nil {
		return cfg, fmt.Errorf("config %s: %w", path, err)
	}
	return cfg, nil
}

// Marshal renders the configuration as TOML.
func Marshal(c Config) ([]byte, error) {
	return toml.Marshal(c)
}

// WriteDefault writes the default configuration to path unless it already exists.
func WriteDefault(path string) (bool, error) {
	if _, err := os.Stat(path); err == nil {
		return false, nil
	}
	b, err := Marshal(Default())
	if err != nil {
		return false, err
	}
	header := []byte("# LuniBoard daemon configuration.\n# See docs/setup.md for every option. Restart the daemon after editing.\n\n")
	return true, storage.WriteFileAtomic(path, append(header, b...), 0o600)
}
