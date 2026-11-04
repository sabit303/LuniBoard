package linux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitContent(t *testing.T) {
	u := Unit(UnitOptions{Executable: "/home/me/my apps/luniboard", Home: "/tmp/x"})
	for _, want := range []string{
		`ExecStart="/home/me/my apps/luniboard" daemon`,
		"Environment=LUNIBOARD_HOME=/tmp/x",
		"WantedBy=default.target",
		"Restart=on-failure",
	} {
		if !strings.Contains(u, want) {
			t.Errorf("unit lacks %q:\n%s", want, u)
		}
	}
}

// TestUnitVerifies runs systemd-analyze on the generated unit when available.
func TestUnitVerifies(t *testing.T) {
	if _, err := exec.LookPath("systemd-analyze"); err != nil {
		t.Skip("systemd-analyze not available")
	}
	exe, err := exec.LookPath("true")
	if err != nil {
		t.Skip("no executable to reference")
	}
	p := filepath.Join(t.TempDir(), UnitName)
	if err := os.WriteFile(p, []byte(Unit(UnitOptions{Executable: exe})), 0o644); err != nil {
		t.Fatal(err)
	}
	out, err := exec.Command("systemd-analyze", "--user", "verify", p).CombinedOutput()
	if err != nil {
		t.Fatalf("systemd-analyze verify failed: %v\n%s", err, out)
	}
}

func TestUsePackagedUnit(t *testing.T) {
	dir := t.TempDir()
	old := VendorUnitPath
	defer func() { VendorUnitPath = old }()
	VendorUnitPath = filepath.Join(dir, UnitName)

	pkg := UnitOptions{Executable: packagedExecutable}
	if usePackagedUnit(pkg) {
		t.Fatal("no vendor unit installed yet")
	}
	if err := os.WriteFile(VendorUnitPath, []byte(Unit(pkg)), 0o644); err != nil {
		t.Fatal(err)
	}
	if !usePackagedUnit(pkg) {
		t.Fatal("packaged binary with vendor unit should use it")
	}
	if usePackagedUnit(UnitOptions{Executable: "/home/me/.local/bin/luniboard"}) {
		t.Fatal("a ~/.local binary must get its own unit")
	}
	if usePackagedUnit(UnitOptions{Executable: packagedExecutable, Home: "/tmp/x"}) {
		t.Fatal("custom home needs its own unit (Environment=)")
	}
}
