package linux

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnitContent(t *testing.T) {
	u := Unit(UnitOptions{Executable: "/home/me/my apps/universal-clipboard", Home: "/tmp/x"})
	for _, want := range []string{
		`ExecStart="/home/me/my apps/universal-clipboard" daemon`,
		"Environment=UCLIP_HOME=/tmp/x",
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
