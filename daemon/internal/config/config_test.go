package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDefaultIsValid(t *testing.T) {
	if err := Default().Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestLoadMissingFileReturnsDefaults(t *testing.T) {
	cfg, err := Load(filepath.Join(t.TempDir(), "nope.toml"))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Network.Port != DefaultPort {
		t.Fatalf("port = %d", cfg.Network.Port)
	}
}

func TestLoadOverridesAndDurations(t *testing.T) {
	p := filepath.Join(t.TempDir(), "c.toml")
	data := `
[device]
name = "Workstation"
[network]
port = 50000
[sync]
max_event_age = "30s"
excluded_content_types = ["url"]
[clipboard]
backend = "x11"
poll_interval = "1s"
`
	if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Device.Name != "Workstation" || cfg.Network.Port != 50000 ||
		cfg.Sync.MaxEventAge.Duration != 30*time.Second || cfg.Clipboard.PollInterval.Duration != time.Second ||
		cfg.Clipboard.Backend != "x11" || len(cfg.Sync.ExcludedContentTypes) != 1 {
		t.Fatalf("unexpected config %+v", cfg)
	}
	if !cfg.Sync.Receive || cfg.History.MaxEntries != 50 {
		t.Fatal("defaults for unspecified keys were lost")
	}
}

func TestLoadRejectsUnknownKeysAndInvalidValues(t *testing.T) {
	for _, data := range []string{
		"[network]\nprot = 1\n",
		"[clipboard]\nbackend = \"gnome\"\n",
		"[sync]\nexcluded_content_types = [\"video\"]\n",
		"[sync]\nmax_payload_bytes = 999999999\n",
		"[logging]\nlevel = \"loud\"\n",
	} {
		p := filepath.Join(t.TempDir(), "c.toml")
		if err := os.WriteFile(p, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(p); err == nil {
			t.Errorf("expected error for %q", data)
		}
	}
}

func TestWriteDefaultRoundTrip(t *testing.T) {
	p := filepath.Join(t.TempDir(), "sub", "config.toml")
	created, err := WriteDefault(p)
	if err != nil || !created {
		t.Fatalf("WriteDefault: %v %v", created, err)
	}
	if created, _ := WriteDefault(p); created {
		t.Fatal("WriteDefault overwrote an existing file")
	}
	if _, err := Load(p); err != nil {
		t.Fatalf("generated default config does not load: %v", err)
	}
	b, _ := os.ReadFile(p)
	if !strings.Contains(string(b), "max_event_age = '2m0s'") && !strings.Contains(string(b), `max_event_age = "2m0s"`) {
		t.Fatalf("durations not rendered as strings:\n%s", b)
	}
}

func TestResolvePathsHome(t *testing.T) {
	home := t.TempDir()
	p, err := ResolvePaths(home)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(p.SocketPath(), home) || !strings.HasPrefix(p.ConfigFile(), home) {
		t.Fatalf("paths not under home: %+v", p)
	}
	if err := p.Ensure(); err != nil {
		t.Fatal(err)
	}
	st, _ := os.Stat(p.DataDir)
	if st.Mode().Perm() != 0o700 {
		t.Fatalf("data dir mode %o", st.Mode().Perm())
	}
}

func TestExampleConfigLoads(t *testing.T) {
	cfg, err := Load(filepath.Join("..", "..", "configs", "config.example.toml"))
	if err != nil {
		t.Fatalf("configs/config.example.toml is out of date: %v", err)
	}
	def := Default()
	def.Device.Name = cfg.Device.Name
	a, _ := Marshal(cfg)
	b, _ := Marshal(def)
	if string(a) != string(b) {
		t.Fatalf("example config differs from defaults:\n%s\n---\n%s", a, b)
	}
}

func TestMigrateLegacyDirs(t *testing.T) {
	base := t.TempDir()
	p := Paths{
		ConfigDir: filepath.Join(base, "config", AppDirName),
		DataDir:   filepath.Join(base, "data", AppDirName),
		StateDir:  filepath.Join(base, "state", AppDirName),
	}
	legacyData := filepath.Join(base, "data", legacyAppDirName)
	if err := os.MkdirAll(filepath.Join(legacyData, "identity"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(legacyData, "devices.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	// The new config dir already exists: it must not be replaced.
	if err := os.MkdirAll(p.ConfigDir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(base, "config", legacyAppDirName), 0o700); err != nil {
		t.Fatal(err)
	}
	moved, err := MigrateLegacyDirs(p)
	if err != nil {
		t.Fatal(err)
	}
	if len(moved) != 1 {
		t.Fatalf("moved %v", moved)
	}
	if _, err := os.Stat(filepath.Join(p.DataDir, "devices.json")); err != nil {
		t.Fatal("data not migrated")
	}
	if _, err := os.Stat(filepath.Join(base, "config", legacyAppDirName)); err != nil {
		t.Fatal("legacy config dir should be left alone when the new one exists")
	}
}
