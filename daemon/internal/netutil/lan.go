// Package netutil finds the local network interfaces LuniBoard is used on.
package netutil

import (
	"net"
	"strings"
)

// virtualInterfacePrefixes identify loopback, container, VM and VPN
// interfaces, which are not the user's local network.
var virtualInterfacePrefixes = []string{
	"lo", "docker", "veth", "br-", "virbr", "vmnet", "vboxnet", "tun", "tap", "wg", "zt",
	"tailscale", "podman", "cni", "flannel", "lxc", "lxd",
}

// IsVirtualInterface reports whether name looks like a non-LAN interface.
func IsVirtualInterface(name string) bool {
	for _, p := range virtualInterfacePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// LAN is an IPv4 local network this machine is attached to.
type LAN struct {
	Interface string
	IP        net.IP
	Network   *net.IPNet
}

// LANs returns the private IPv4 networks of the machine's physical interfaces
// that are up (Wi-Fi, Ethernet), e.g. 192.168.0.0/24 on wlp2s0.
func LANs() []LAN {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []LAN
	seen := map[string]bool{}
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 || IsVirtualInterface(iface.Name) {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, a := range addrs {
			ipn, ok := a.(*net.IPNet)
			if !ok {
				continue
			}
			ip4 := ipn.IP.To4()
			if ip4 == nil || !ip4.IsPrivate() {
				continue
			}
			network := &net.IPNet{IP: ip4.Mask(ipn.Mask), Mask: ipn.Mask}
			if seen[network.String()] {
				continue
			}
			seen[network.String()] = true
			out = append(out, LAN{Interface: iface.Name, IP: ip4, Network: network})
		}
	}
	return out
}
