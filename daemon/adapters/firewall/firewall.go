// Package firewall opens (and closes) the LuniBoard port in the host firewall
// for the local network only. It drives the firewall's own command-line tool
// (ufw or firewall-cmd), elevating with sudo/pkexec when not running as root.
package firewall

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// Tag marks rules created by LuniBoard so they can be found and removed.
const Tag = "LuniBoard"

// MDNSPort is the multicast DNS port used for discovery.
const MDNSPort = 5353

// Network is a local subnet that may reach the daemon.
type Network struct {
	Subnet    string // e.g. 192.168.0.0/24
	Interface string // e.g. wlp2s0 (used to pick the firewalld zone)
}

// Rule describes what to open.
type Rule struct {
	Port     int
	Networks []Network
	// MDNS also opens UDP 5353 so the phone app can discover this computer.
	MDNS bool
}

// Validate checks the rule before any command runs.
func (r Rule) Validate() error {
	if r.Port < 1 || r.Port > 65535 {
		return fmt.Errorf("invalid port %d (a fixed network.port is required)", r.Port)
	}
	if len(r.Networks) == 0 {
		return errors.New("no local network found; connect to your Wi-Fi/LAN or pass --subnet")
	}
	for _, n := range r.Networks {
		if !validSubnet(n.Subnet) {
			return fmt.Errorf("invalid subnet %q", n.Subnet)
		}
	}
	return nil
}

var subnetPattern = regexp.MustCompile(`^(\d{1,3}\.){3}\d{1,3}/\d{1,2}$`)

func validSubnet(s string) bool { return subnetPattern.MatchString(s) }

// Runner executes a firewall command (args[0] is the program) and returns stdout.
type Runner interface {
	Run(ctx context.Context, args ...string) (string, error)
}

// Backend is one supported firewall.
type Backend interface {
	Name() string
	Allow(ctx context.Context, run Runner, r Rule) error
	Remove(ctx context.Context, run Runner, r Rule) error
}

// UnsupportedError means a firewall is active that LuniBoard cannot configure automatically.
type UnsupportedError struct {
	Name         string
	Instructions string
}

func (e *UnsupportedError) Error() string {
	return fmt.Sprintf("%s is active but cannot be configured automatically.\n%s", e.Name, e.Instructions)
}

// ErrNoFirewall means no active firewall was found: nothing needs to be opened.
var ErrNoFirewall = errors.New("no active firewall detected")

// Probe inspects the system (substituted in tests).
type Probe struct {
	ServiceActive func(name string) bool
	HasBinary     func(name string) bool
	ReadFile      func(path string) ([]byte, error)
}

// SystemProbe inspects the real system without root privileges.
func SystemProbe() Probe {
	return Probe{
		ServiceActive: func(name string) bool {
			return exec.Command("systemctl", "is-active", "--quiet", name).Run() == nil
		},
		HasBinary: func(name string) bool {
			_, err := exec.LookPath(name)
			return err == nil
		},
		ReadFile: os.ReadFile,
	}
}

// Detect returns the active firewall backend.
func Detect(p Probe) (Backend, error) {
	if p.ServiceActive("firewalld") && p.HasBinary("firewall-cmd") {
		return Firewalld{}, nil
	}
	if p.ServiceActive("ufw") && p.HasBinary("ufw") && ufwEnabled(p) {
		return UFW{}, nil
	}
	if p.ServiceActive("nftables") {
		return nil, &UnsupportedError{Name: "nftables", Instructions: manualNft}
	}
	if p.ServiceActive("iptables") {
		return nil, &UnsupportedError{Name: "iptables", Instructions: manualIptables}
	}
	return nil, ErrNoFirewall
}

// ufwEnabled reads /etc/ufw/ufw.conf (world-readable): ufw.service can be
// active while the firewall itself is disabled.
func ufwEnabled(p Probe) bool {
	b, err := p.ReadFile("/etc/ufw/ufw.conf")
	if err != nil {
		return true // cannot tell; assume enabled since the service is active
	}
	sc := bufio.NewScanner(strings.NewReader(string(b)))
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "ENABLED=") {
			return strings.Trim(strings.TrimPrefix(line, "ENABLED="), `"'`) == "yes"
		}
	}
	return false
}

const manualNft = `Add rules like these to the input chain in /etc/nftables.conf, then run 'sudo systemctl reload nftables':
    ip saddr <your LAN, e.g. 192.168.0.0/24> tcp dport <port> accept
    ip saddr <your LAN> udp dport 5353 accept`

const manualIptables = `Run (and persist with your distribution's iptables-save mechanism):
    sudo iptables -I INPUT -s <your LAN, e.g. 192.168.0.0/24> -p tcp --dport <port> -j ACCEPT
    sudo iptables -I INPUT -s <your LAN> -p udp --dport 5353 -j ACCEPT`

// UFW configures the Uncomplicated Firewall.
type UFW struct{}

func (UFW) Name() string { return "ufw" }

// Allow adds one rule per local subnet, tagged with a comment. ufw skips
// rules that already exist, so running it again is harmless.
func (UFW) Allow(ctx context.Context, run Runner, r Rule) error {
	if err := r.Validate(); err != nil {
		return err
	}
	for _, n := range r.Networks {
		if _, err := run.Run(ctx, "ufw", "allow", "from", n.Subnet, "to", "any", "port", strconv.Itoa(r.Port), "proto", "tcp", "comment", Tag); err != nil {
			return err
		}
		if r.MDNS {
			if _, err := run.Run(ctx, "ufw", "allow", "from", n.Subnet, "to", "any", "port", strconv.Itoa(MDNSPort), "proto", "udp", "comment", Tag+" mDNS"); err != nil {
				return err
			}
		}
	}
	return nil
}

var ufwNumbered = regexp.MustCompile(`^\[\s*(\d+)\]`)

// Remove deletes every rule whose comment starts with the LuniBoard tag,
// regardless of which subnet it was created for.
func (UFW) Remove(ctx context.Context, run Runner, _ Rule) error {
	out, err := run.Run(ctx, "ufw", "status", "numbered")
	if err != nil {
		return err
	}
	nums := ufwTaggedRules(out)
	// Delete from the highest number down: numbers shift after each deletion.
	for _, n := range nums {
		if _, err := run.Run(ctx, "ufw", "--force", "delete", strconv.Itoa(n)); err != nil {
			return err
		}
	}
	return nil
}

// ufwTaggedRules returns the numbers of LuniBoard rules in `ufw status numbered` output, descending.
func ufwTaggedRules(status string) []int {
	var nums []int
	for _, line := range strings.Split(status, "\n") {
		m := ufwNumbered.FindStringSubmatch(strings.TrimSpace(line))
		if m == nil {
			continue
		}
		i := strings.Index(line, "# ")
		if i < 0 || !strings.HasPrefix(line[i+2:], Tag) {
			continue
		}
		n, _ := strconv.Atoi(m[1])
		nums = append(nums, n)
	}
	sort.Sort(sort.Reverse(sort.IntSlice(nums)))
	return nums
}

// Firewalld configures firewalld with permanent rich rules in the zone of
// each interface.
type Firewalld struct{}

func (Firewalld) Name() string { return "firewalld" }

func firewalldPortRule(subnet string, port int) string {
	return fmt.Sprintf(`rule family="ipv4" source address="%s" port port="%d" protocol="tcp" accept`, subnet, port)
}

func firewalldMDNSRule(subnet string) string {
	return fmt.Sprintf(`rule family="ipv4" source address="%s" service name="mdns" accept`, subnet)
}

func (f Firewalld) zone(ctx context.Context, run Runner, iface string) (string, error) {
	if iface != "" {
		if z, err := run.Run(ctx, "firewall-cmd", "--get-zone-of-interface="+iface); err == nil && strings.TrimSpace(z) != "" {
			return strings.TrimSpace(z), nil
		}
	}
	z, err := run.Run(ctx, "firewall-cmd", "--get-default-zone")
	return strings.TrimSpace(z), err
}

// Allow adds permanent rich rules and reloads firewalld. Existing rules only produce a warning.
func (f Firewalld) Allow(ctx context.Context, run Runner, r Rule) error {
	if err := r.Validate(); err != nil {
		return err
	}
	for _, n := range r.Networks {
		zone, err := f.zone(ctx, run, n.Interface)
		if err != nil {
			return err
		}
		if _, err := run.Run(ctx, "firewall-cmd", "--permanent", "--zone="+zone, "--add-rich-rule="+firewalldPortRule(n.Subnet, r.Port)); err != nil {
			return err
		}
		if r.MDNS {
			if _, err := run.Run(ctx, "firewall-cmd", "--permanent", "--zone="+zone, "--add-rich-rule="+firewalldMDNSRule(n.Subnet)); err != nil {
				return err
			}
		}
	}
	_, err := run.Run(ctx, "firewall-cmd", "--reload")
	return err
}

// Remove deletes rich rules matching the shapes LuniBoard creates, in every zone.
func (f Firewalld) Remove(ctx context.Context, run Runner, r Rule) error {
	zones, err := run.Run(ctx, "firewall-cmd", "--get-zones")
	if err != nil {
		return err
	}
	ours := regexp.MustCompile(fmt.Sprintf(`^rule family="ipv4" source address="[0-9./]+" (port port="%d" protocol="tcp"|service name="mdns") accept$`, r.Port))
	for _, zone := range strings.Fields(zones) {
		rules, err := run.Run(ctx, "firewall-cmd", "--permanent", "--zone="+zone, "--list-rich-rules")
		if err != nil {
			continue
		}
		for _, rule := range strings.Split(rules, "\n") {
			rule = strings.TrimSpace(rule)
			if ours.MatchString(rule) {
				if _, err := run.Run(ctx, "firewall-cmd", "--permanent", "--zone="+zone, "--remove-rich-rule="+rule); err != nil {
					return err
				}
			}
		}
	}
	_, err = run.Run(ctx, "firewall-cmd", "--reload")
	return err
}
