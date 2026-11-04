package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/luniboard/luniboard/daemon/adapters/firewall"
	"github.com/luniboard/luniboard/daemon/internal/config"
	"github.com/luniboard/luniboard/daemon/internal/netutil"
)

// privateRanges are used with --any-private (laptops that move between trusted networks).
var privateRanges = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}

func runFirewall(ctx context.Context, g *globals, args []string) error {
	sub := "status"
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		sub, args = args[0], args[1:]
	}
	fs := newFlags("firewall "+sub, g)
	anyPrivate := fs.Bool("any-private", false, "allow all private address ranges instead of only the current local network(s)")
	subnets := fs.String("subnet", "", "comma-separated subnets to allow, e.g. 192.168.1.0/24")
	noMDNS := fs.Bool("no-mdns", false, "do not open UDP 5353 (discovery); pairing by QR code still works")
	dryRun := fs.Bool("dry-run", false, "print the commands without running them")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}

	rule, err := firewallRule(g, *anyPrivate, *subnets, !*noMDNS)
	if err != nil && sub != "status" {
		return err
	}
	backend, detectErr := firewall.Detect(firewall.SystemProbe())

	switch sub {
	case "status":
		switch {
		case errors.Is(detectErr, firewall.ErrNoFirewall):
			g.printf("No active firewall detected: LuniBoard is reachable on port %d.\n", rule.Port)
		case detectErr != nil:
			g.printf("%v\n", detectErr)
		default:
			g.printf("Firewall: %s (active)\n", backend.Name())
			g.printf("Run 'luniboard firewall allow' to open port %d for: %s\n", rule.Port, describeNetworks(rule))
		}
		return nil
	case "allow", "remove":
	default:
		return usageError("usage: firewall [status|allow|remove] [--any-private] [--subnet CIDR,...] [--no-mdns] [--dry-run]")
	}

	if errors.Is(detectErr, firewall.ErrNoFirewall) {
		g.printf("No active firewall detected; nothing to change.\n")
		return nil
	}
	if detectErr != nil {
		return detectErr
	}
	run := firewall.ExecRunner{Out: g.env.Stdout, DryRun: *dryRun}
	if sub == "allow" {
		g.printf("Opening port %d (%s) in %s for %s:\n", rule.Port, mdnsNote(rule), backend.Name(), describeNetworks(rule))
		if err := backend.Allow(ctx, run, rule); err != nil {
			return fmt.Errorf("firewall: %w", err)
		}
		if !*dryRun {
			g.printf("Done. Devices on these networks can now reach LuniBoard. Undo with 'luniboard firewall remove'.\n")
		}
		return nil
	}
	g.printf("Removing LuniBoard rules from %s:\n", backend.Name())
	if err := backend.Remove(ctx, run, rule); err != nil {
		return fmt.Errorf("firewall: %w", err)
	}
	if !*dryRun {
		g.printf("Done.\n")
	}
	return nil
}

// firewallRule builds the rule from the configured port and the local networks.
func firewallRule(g *globals, anyPrivate bool, subnets string, mdns bool) (firewall.Rule, error) {
	port := config.DefaultPort
	if cfg, err := config.Load(g.paths.ConfigFile()); err == nil {
		port = cfg.Network.Port
	}
	rule := firewall.Rule{Port: port, MDNS: mdns}
	switch {
	case subnets != "":
		for _, s := range strings.Split(subnets, ",") {
			rule.Networks = append(rule.Networks, firewall.Network{Subnet: strings.TrimSpace(s)})
		}
	case anyPrivate:
		for _, s := range privateRanges {
			rule.Networks = append(rule.Networks, firewall.Network{Subnet: s})
		}
	default:
		for _, lan := range netutil.LANs() {
			rule.Networks = append(rule.Networks, firewall.Network{Subnet: lan.Network.String(), Interface: lan.Interface})
		}
	}
	return rule, rule.Validate()
}

func describeNetworks(r firewall.Rule) string {
	if len(r.Networks) == 0 {
		return "(no local network found)"
	}
	parts := make([]string, len(r.Networks))
	for i, n := range r.Networks {
		parts[i] = n.Subnet
		if n.Interface != "" {
			parts[i] += " on " + n.Interface
		}
	}
	return strings.Join(parts, ", ")
}

func mdnsNote(r firewall.Rule) string {
	if r.MDNS {
		return fmt.Sprintf("TCP, plus UDP %d for discovery", firewall.MDNSPort)
	}
	return "TCP"
}

// offerFirewall is called after 'service install': if a firewall is active it
// offers to open the port, so phones can connect without manual setup.
func offerFirewall(ctx context.Context, g *globals) {
	backend, err := firewall.Detect(firewall.SystemProbe())
	if errors.Is(err, firewall.ErrNoFirewall) {
		return
	}
	if err != nil {
		g.printf("\nNote: %v\n", err)
		return
	}
	rule, err := firewallRule(g, false, "", true)
	if err != nil {
		g.printf("\nYour firewall (%s) is active, but no local network was found. Later run: luniboard firewall allow\n", backend.Name())
		return
	}
	g.printf("\nYour firewall (%s) is active and will block your phone.\n", backend.Name())
	if !isTerminal(g.env.Stdin) {
		g.printf("Open the port for your local network with: luniboard firewall allow\n")
		return
	}
	g.printf("Open port %d for %s now? (needs your password) [Y/n]: ", rule.Port, describeNetworks(rule))
	line, ok := readLine(ctx, bufio.NewReader(g.env.Stdin))
	if !ok {
		return
	}
	if a := strings.ToLower(strings.TrimSpace(line)); a != "" && a != "y" && a != "yes" {
		g.printf("Skipped. You can do it later with: luniboard firewall allow\n")
		return
	}
	if err := backend.Allow(ctx, firewall.ExecRunner{Out: g.env.Stdout}, rule); err != nil {
		g.printf("Could not update the firewall: %v\nRun 'luniboard firewall allow' to try again.\n", err)
		return
	}
	g.printf("Firewall updated.\n")
}
