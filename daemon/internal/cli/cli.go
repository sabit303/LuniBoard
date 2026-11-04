// Package cli implements the luniboard command-line interface. Every
// command other than "daemon" is a thin client of the local API.
package cli

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/luniboard/luniboard/daemon/internal/api"
	"github.com/luniboard/luniboard/daemon/internal/config"
)

// Env carries process-wide inputs so commands are testable.
type Env struct {
	Stdin  io.Reader
	Stdout io.Writer
	Stderr io.Writer
}

type globals struct {
	home  string
	json  bool
	paths config.Paths
	env   Env
}

func (g *globals) client() *api.Client { return api.NewClient(g.paths.SocketPath()) }

func (g *globals) printf(format string, a ...any) { fmt.Fprintf(g.env.Stdout, format, a...) }

func (g *globals) printJSON(v any) error {
	enc := json.NewEncoder(g.env.Stdout)
	enc.SetIndent("", "  ")
	return enc.Encode(v)
}

type command struct {
	name    string
	summary string
	run     func(ctx context.Context, g *globals, args []string) error
}

func commands() []command {
	return []command{
		{"daemon", "Run the daemon in the foreground", runDaemon},
		{"start", "Start the daemon in the background", runStart},
		{"stop", "Stop the daemon", runStop},
		{"status", "Show daemon status", runStatus},
		{"devices", "List paired devices; 'devices set <device> --send on|off --receive on|off'", runDevices},
		{"discover", "List compatible devices visible on the network", runDiscover},
		{"pair", "Pair a device; also 'pair connect|approve|reject|cancel|status'", runPair},
		{"unpair", "Remove a paired device and revoke its access", runUnpair},
		{"send", "Send text (argument or stdin) to paired devices", runSend},
		{"history", "Show clipboard history; 'history clear' deletes it", runHistory},
		{"pause", "Pause automatic synchronization", runPause},
		{"resume", "Resume automatic synchronization", runResume},
		{"config", "Show or initialize configuration: config [show|path|init]", runConfig},
		{"logs", "Show daemon logs: logs [-n lines] [-f]", runLogs},
		{"service", "Manage the background service: service install|uninstall|status", runService},
		{"version", "Print version", runVersion},
	}
}

func usage(w io.Writer) {
	fmt.Fprintf(w, "LuniBoard — LAN clipboard synchronization\n\n")
	fmt.Fprintf(w, "Usage: luniboard [--home DIR] [--json] <command> [arguments]\n\nCommands:\n")
	for _, c := range commands() {
		fmt.Fprintf(w, "  %-10s %s\n", c.name, c.summary)
	}
	fmt.Fprintf(w, "\nGlobal flags:\n  --home DIR  keep all state under DIR (env %s)\n  --json      machine-readable output\n", config.HomeEnv)
}

// Main runs the CLI and returns the process exit code.
func Main(ctx context.Context, args []string, env Env) int {
	g := &globals{env: env}
	fs := flag.NewFlagSet("luniboard", flag.ContinueOnError)
	fs.SetOutput(env.Stderr)
	fs.StringVar(&g.home, "home", "", "state directory")
	fs.BoolVar(&g.json, "json", false, "JSON output")
	fs.Usage = func() { usage(env.Stderr) }
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}
		return 2
	}
	rest := fs.Args()
	if len(rest) == 0 || rest[0] == "help" || rest[0] == "-h" || rest[0] == "--help" {
		usage(env.Stdout)
		return 0
	}
	paths, err := config.ResolvePaths(g.home)
	if err != nil {
		fmt.Fprintln(env.Stderr, "error:", err)
		return 1
	}
	g.paths = paths
	for _, c := range commands() {
		if c.name == rest[0] {
			if err := c.run(ctx, g, rest[1:]); err != nil {
				var ue usageError
				if errors.As(err, &ue) {
					fmt.Fprintln(env.Stderr, "error:", err)
					return 2
				}
				if errors.Is(err, api.ErrDaemonNotRunning) {
					fmt.Fprintln(env.Stderr, "error: the daemon is not running (start it with 'luniboard start')")
					return 1
				}
				fmt.Fprintln(env.Stderr, "error:", err)
				return 1
			}
			return 0
		}
	}
	fmt.Fprintf(env.Stderr, "error: unknown command %q\n\n", rest[0])
	usage(env.Stderr)
	return 2
}

type usageError string

func (e usageError) Error() string { return string(e) }

func newFlags(name string, g *globals) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(g.env.Stderr)
	return fs
}

// parseInterleaved parses flags that may appear after positional arguments.
func parseInterleaved(fs *flag.FlagSet, args []string) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, err
		}
		args = fs.Args()
		if len(args) == 0 {
			return pos, nil
		}
		pos = append(pos, args[0])
		args = args[1:]
	}
}

func timeoutCtx(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, 10*time.Second)
}

// resolveDevice maps a user-supplied ID, unique ID prefix or name to a device ID.
func resolveDevice(ctx context.Context, c *api.Client, ref string) (api.DeviceView, error) {
	list, err := c.Devices(ctx)
	if err != nil {
		return api.DeviceView{}, err
	}
	var matches []api.DeviceView
	for _, d := range list {
		if d.ID == ref {
			return d, nil
		}
	}
	for _, d := range list {
		if strings.EqualFold(d.Name, ref) || (len(ref) >= 4 && strings.HasPrefix(d.ID, ref)) {
			matches = append(matches, d)
		}
	}
	switch len(matches) {
	case 1:
		return matches[0], nil
	case 0:
		return api.DeviceView{}, fmt.Errorf("no paired device matches %q (see 'luniboard devices')", ref)
	default:
		return api.DeviceView{}, fmt.Errorf("%q matches %d devices; use the device ID", ref, len(matches))
	}
}

func shortID(id string) string {
	if len(id) > 8 {
		return id[:8]
	}
	return id
}

func ago(t time.Time) string {
	if t.IsZero() {
		return "never"
	}
	d := time.Since(t)
	switch {
	case d < time.Minute:
		return "just now"
	case d < time.Hour:
		return fmt.Sprintf("%dm ago", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh ago", int(d.Hours()))
	default:
		return t.Local().Format("2006-01-02")
	}
}

func onOff(b bool) string {
	if b {
		return "on"
	}
	return "off"
}

func parseOnOff(s string) (bool, error) {
	switch strings.ToLower(s) {
	case "on", "true", "yes", "1":
		return true, nil
	case "off", "false", "no", "0":
		return false, nil
	}
	return false, usageError(fmt.Sprintf("expected on or off, got %q", s))
}

func isTerminal(f any) bool {
	file, ok := f.(*os.File)
	if !ok {
		return false
	}
	st, err := file.Stat()
	return err == nil && st.Mode()&os.ModeCharDevice != 0
}

// formatFingerprint groups a hex fingerprint for reading aloud.
func formatFingerprint(fp string) string {
	var parts []string
	for i := 0; i+4 <= len(fp) && i < 32; i += 4 {
		parts = append(parts, fp[i:i+4])
	}
	return strings.Join(parts, " ") + " …"
}
