package firewall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
)

// ExecRunner runs firewall commands, elevating with sudo (or pkexec) when the
// current user is not root. Every command is echoed before it runs so the
// user can see exactly what changes.
type ExecRunner struct {
	Out    io.Writer // where commands are echoed and sudo prompts appear
	DryRun bool
}

// Run implements Runner.
func (r ExecRunner) Run(ctx context.Context, args ...string) (string, error) {
	argv := args
	if os.Geteuid() != 0 {
		switch {
		case has("sudo"):
			argv = append([]string{"sudo"}, args...)
		case has("pkexec"):
			argv = append([]string{"pkexec"}, args...)
		default:
			return "", errors.New("root privileges are required (install sudo, or run this command as root)")
		}
	}
	if r.Out != nil {
		fmt.Fprintf(r.Out, "  $ %s\n", quote(argv))
	}
	if r.DryRun {
		return "", nil
	}
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	var stdout, stderr bytes.Buffer
	cmd.Stdin = os.Stdin // lets sudo ask for the password
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return stdout.String(), fmt.Errorf("%s: %v: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func has(name string) bool {
	_, err := exec.LookPath(name)
	return err == nil
}

func quote(argv []string) string {
	out := make([]string, len(argv))
	for i, a := range argv {
		if strings.ContainsAny(a, " \"'") {
			a = "'" + strings.ReplaceAll(a, "'", `'\''`) + "'"
		}
		out[i] = a
	}
	return strings.Join(out, " ")
}
