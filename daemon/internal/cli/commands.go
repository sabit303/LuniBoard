package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/universal-clipboard/universal-clipboard/daemon/internal/api"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/config"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/history"
)

func runStatus(ctx context.Context, g *globals, args []string) error {
	cctx, cancel := timeoutCtx(ctx)
	defer cancel()
	s, err := g.client().Status(cctx)
	if err != nil {
		return err
	}
	if g.json {
		return g.printJSON(s)
	}
	sync := "active"
	if s.Paused {
		sync = "paused"
	}
	tw := tabwriter.NewWriter(g.env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintf(tw, "Device:\t%s (%s)\n", s.Device.Name, s.Device.ID)
	fmt.Fprintf(tw, "Version:\t%s\n", s.Version)
	fmt.Fprintf(tw, "Synchronization:\t%s\n", sync)
	fmt.Fprintf(tw, "Clipboard:\t%s, %s\n", s.Clipboard.Backend, s.Clipboard.Mode)
	if s.Clipboard.Detail != "" {
		fmt.Fprintf(tw, "\t%s\n", s.Clipboard.Detail)
	}
	fmt.Fprintf(tw, "Listening:\tport %d (%s)\n", s.ListenPort, strings.Join(s.Addresses, ", "))
	fmt.Fprintf(tw, "Discovery:\t%s\n", onOff(s.DiscoveryEnabled))
	fmt.Fprintf(tw, "Devices:\t%d paired, %d connected\n", s.PairedDevices, s.ConnectedDevices)
	fmt.Fprintf(tw, "Events:\t%s\n", s.Stats)
	fmt.Fprintf(tw, "Fingerprint:\t%s\n", formatFingerprint(s.Fingerprint))
	fmt.Fprintf(tw, "Uptime:\t%s\n", time.Since(s.StartedAt).Round(time.Second))
	fmt.Fprintf(tw, "Config:\t%s\n", s.ConfigFile)
	return tw.Flush()
}

func runDevices(ctx context.Context, g *globals, args []string) error {
	c := g.client()
	cctx, cancel := timeoutCtx(ctx)
	defer cancel()
	if len(args) > 0 && args[0] == "set" {
		return devicesSet(cctx, g, c, args[1:])
	}
	if len(args) > 0 && args[0] != "list" {
		return usageError("usage: devices [list] | devices set <device> [--send on|off] [--receive on|off]")
	}
	list, err := c.Devices(cctx)
	if err != nil {
		return err
	}
	if g.json {
		return g.printJSON(list)
	}
	if len(list) == 0 {
		g.printf("No paired devices. Run 'universal-clipboard pair' to add one.\n")
		return nil
	}
	tw := tabwriter.NewWriter(g.env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tPLATFORM\tSTATUS\tSEND\tRECEIVE\tLAST SEEN\tADDRESS")
	for _, d := range list {
		status, addr := "offline", ""
		if d.Connected {
			status, addr = "connected", d.Session.RemoteAddr
		} else if len(d.Addresses) > 0 {
			addr = d.Addresses[0]
		}
		last := ago(d.LastSeen)
		if d.Connected {
			last = "now"
		}
		fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s\t%s\t%s\n", d.Name, shortID(d.ID), d.Platform, status,
			onOff(d.Permissions.Send), onOff(d.Permissions.Receive), last, addr)
	}
	return tw.Flush()
}

func devicesSet(ctx context.Context, g *globals, c *api.Client, args []string) error {
	fs := newFlags("devices set", g)
	send := fs.String("send", "", "on|off: send clipboard events to this device")
	recv := fs.String("receive", "", "on|off: apply clipboard events from this device")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return usageError(err.Error())
	}
	if len(pos) != 1 || (*send == "" && *recv == "") {
		return usageError("usage: devices set <device> [--send on|off] [--receive on|off]")
	}
	d, err := resolveDevice(ctx, c, pos[0])
	if err != nil {
		return err
	}
	var req api.UpdateDeviceRequest
	if *send != "" {
		v, err := parseOnOff(*send)
		if err != nil {
			return err
		}
		req.Send = &v
	}
	if *recv != "" {
		v, err := parseOnOff(*recv)
		if err != nil {
			return err
		}
		req.Receive = &v
	}
	upd, err := c.UpdateDevice(ctx, d.ID, req)
	if err != nil {
		return err
	}
	if g.json {
		return g.printJSON(upd)
	}
	g.printf("%s: send %s, receive %s\n", upd.Name, onOff(upd.Permissions.Send), onOff(upd.Permissions.Receive))
	return nil
}

func runDiscover(ctx context.Context, g *globals, args []string) error {
	cctx, cancel := timeoutCtx(ctx)
	defer cancel()
	list, err := g.client().Discovered(cctx)
	if err != nil {
		return err
	}
	if g.json {
		return g.printJSON(list)
	}
	if len(list) == 0 {
		g.printf("No devices found yet (discovery may be disabled, or mDNS blocked by a firewall: UDP 5353).\n")
		return nil
	}
	tw := tabwriter.NewWriter(g.env.Stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(tw, "NAME\tID\tPLATFORM\tPAIRED\tADDRESSES")
	for _, p := range list {
		fmt.Fprintf(tw, "%s\t%s\t%s\t%v\t%s\n", p.Name, shortID(p.DeviceID), p.Platform, p.Paired, strings.Join(p.Addresses, ", "))
	}
	return tw.Flush()
}

func runSend(ctx context.Context, g *globals, args []string) error {
	fs := newFlags("send", g)
	to := fs.String("to", "", "comma-separated device IDs or names (default: all connected)")
	pos, err := parseInterleaved(fs, args)
	if err != nil {
		return usageError(err.Error())
	}
	var text string
	switch {
	case len(pos) > 0:
		text = strings.Join(pos, " ")
	case !isTerminal(g.env.Stdin):
		b, err := io.ReadAll(io.LimitReader(g.env.Stdin, 64<<20))
		if err != nil {
			return err
		}
		text = string(b)
	default:
		return usageError("usage: send [--to devices] <text>   (or pipe text on stdin)")
	}
	c := g.client()
	cctx, cancel := timeoutCtx(ctx)
	defer cancel()
	var targets []string
	if *to != "" {
		for _, ref := range strings.Split(*to, ",") {
			d, err := resolveDevice(cctx, c, strings.TrimSpace(ref))
			if err != nil {
				return err
			}
			targets = append(targets, d.ID)
		}
	}
	res, err := c.Send(cctx, text, targets)
	if err != nil {
		return err
	}
	if g.json {
		return g.printJSON(res)
	}
	if len(res.Results) == 0 {
		return errors.New("no connected devices to send to")
	}
	names := deviceNames(cctx, c)
	failed := 0
	for id, out := range res.Results {
		line := fmt.Sprintf("%s: %s", nameOr(names, id), out.Status)
		if out.Reason != "" {
			line += " (" + out.Reason + ")"
		}
		g.printf("%s\n", line)
		if out.Status != "applied" && out.Status != "sent" {
			failed++
		}
	}
	if failed == len(res.Results) {
		return errors.New("not delivered to any device")
	}
	return nil
}

func deviceNames(ctx context.Context, c *api.Client) map[string]string {
	m := map[string]string{}
	if list, err := c.Devices(ctx); err == nil {
		for _, d := range list {
			m[d.ID] = d.Name
		}
	}
	return m
}

func nameOr(names map[string]string, id string) string {
	if n, ok := names[id]; ok {
		return n
	}
	return shortID(id)
}

func runHistory(ctx context.Context, g *globals, args []string) error {
	c := g.client()
	cctx, cancel := timeoutCtx(ctx)
	defer cancel()
	if len(args) > 0 && args[0] == "clear" {
		if err := c.ClearHistory(cctx); err != nil {
			return err
		}
		g.printf("History cleared.\n")
		return nil
	}
	fs := newFlags("history", g)
	limit := fs.Int("n", 20, "number of entries")
	full := fs.Bool("full", false, "print complete text instead of a preview")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	entries, err := c.History(cctx, *limit)
	if err != nil {
		return err
	}
	if g.json {
		return g.printJSON(entries)
	}
	if len(entries) == 0 {
		g.printf("History is empty (it may be disabled in the configuration).\n")
		return nil
	}
	names := deviceNames(cctx, c)
	for _, e := range entries {
		peer := "this device"
		arrow := "→"
		if e.Direction == history.Incoming {
			arrow = "←"
			peer = nameOr(names, e.FromDeviceID)
			if e.OriginDeviceID != e.FromDeviceID {
				peer += " (from " + nameOr(names, e.OriginDeviceID) + ")"
			}
		}
		g.printf("%s %s %-5s %-9s %s\n", e.Timestamp.Local().Format("15:04:05"), arrow, e.ContentType, e.Source, peer)
		if *full {
			g.printf("%s\n", e.Text)
		} else {
			g.printf("    %s\n", preview(e.Text, 72))
		}
		if len(e.Deliveries) > 0 {
			var parts []string
			for id, st := range e.Deliveries {
				parts = append(parts, nameOr(names, id)+": "+string(st))
			}
			g.printf("    delivered: %s\n", strings.Join(parts, ", "))
		}
	}
	return nil
}

func preview(s string, n int) string {
	s = strings.Join(strings.Fields(s), " ")
	r := []rune(s)
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return s
}

func runPause(ctx context.Context, g *globals, args []string) error {
	cctx, cancel := timeoutCtx(ctx)
	defer cancel()
	if err := g.client().SetPaused(cctx, true); err != nil {
		return err
	}
	g.printf("Synchronization paused. Manual 'send' still works.\n")
	return nil
}

func runResume(ctx context.Context, g *globals, args []string) error {
	cctx, cancel := timeoutCtx(ctx)
	defer cancel()
	if err := g.client().SetPaused(cctx, false); err != nil {
		return err
	}
	g.printf("Synchronization resumed.\n")
	return nil
}

func runConfig(ctx context.Context, g *globals, args []string) error {
	sub := "show"
	if len(args) > 0 {
		sub = args[0]
	}
	switch sub {
	case "path":
		g.printf("%s\n", g.paths.ConfigFile())
		return nil
	case "init":
		created, err := config.WriteDefault(g.paths.ConfigFile())
		if err != nil {
			return err
		}
		if created {
			g.printf("Wrote %s\n", g.paths.ConfigFile())
		} else {
			g.printf("%s already exists\n", g.paths.ConfigFile())
		}
		return nil
	case "show":
		// Prefer the daemon's effective configuration; fall back to the file.
		cctx, cancel := timeoutCtx(ctx)
		defer cancel()
		if s, err := g.client().ConfigTOML(cctx); err == nil {
			g.printf("# effective configuration of the running daemon\n%s", s)
			return nil
		}
		cfg, err := config.Load(g.paths.ConfigFile())
		if err != nil {
			return err
		}
		b, err := config.Marshal(cfg)
		if err != nil {
			return err
		}
		g.printf("# configuration from %s (daemon not running)\n%s", g.paths.ConfigFile(), b)
		return nil
	}
	return usageError("usage: config [show|path|init]")
}

func runLogs(ctx context.Context, g *globals, args []string) error {
	fs := newFlags("logs", g)
	n := fs.Int("n", 50, "number of lines")
	follow := fs.Bool("f", false, "follow")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	path := g.paths.LogFile()
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("no log file at %s (file logging disabled? try: journalctl --user -u universal-clipboard)", path)
		}
		return err
	}
	defer f.Close()
	lines, err := tail(f, *n)
	if err != nil {
		return err
	}
	for _, l := range lines {
		g.printf("%s\n", l)
	}
	if !*follow {
		return nil
	}
	if _, err := f.Seek(0, io.SeekEnd); err != nil {
		return err
	}
	r := bufio.NewReader(f)
	for {
		line, err := r.ReadString('\n')
		if len(line) > 0 {
			g.printf("%s", line)
		}
		if errors.Is(err, io.EOF) {
			select {
			case <-ctx.Done():
				return nil
			case <-time.After(300 * time.Millisecond):
			}
			continue
		}
		if err != nil {
			return err
		}
	}
}

// tail returns the last n lines of r.
func tail(r io.Reader, n int) ([]string, error) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	var ring []string
	for sc.Scan() {
		ring = append(ring, sc.Text())
		if len(ring) > n {
			ring = ring[1:]
		}
	}
	return ring, sc.Err()
}
