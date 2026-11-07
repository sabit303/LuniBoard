package discovery

import (
	"context"
	"net"
	"os"
	"testing"
	"time"

	"github.com/brutella/dnssd"

	"github.com/luniboard/luniboard/daemon/internal/logging"
	"github.com/luniboard/luniboard/daemon/pkg/protocol"
)

var testFP = protocol.CertificateFingerprint([]byte("x"))

func entry(txt map[string]string) dnssd.BrowseEntry {
	return dnssd.BrowseEntry{
		IPs:       []net.IP{net.ParseIP("fe80::1"), net.ParseIP("192.168.1.20"), net.ParseIP("2001:db8::5"), net.ParseIP("127.0.0.1")},
		Port:      47823,
		IfaceName: "wlan0",
		Name:      "Laptop",
		Text:      txt,
	}
}

func TestParseEntry(t *testing.T) {
	p, ok := ParseEntry(entry(map[string]string{"id": "peer-1", "v": "1", "fp": testFP, "os": "linux", "name": "Arch Laptop"}), "self")
	if !ok {
		t.Fatal("valid entry rejected")
	}
	want := []string{"192.168.1.20:47823", "[2001:db8::5]:47823", "[fe80::1%wlan0]:47823"}
	if len(p.Addresses) != len(want) {
		t.Fatalf("addresses %v", p.Addresses)
	}
	for i := range want {
		if p.Addresses[i] != want[i] {
			t.Fatalf("addresses %v, want %v", p.Addresses, want)
		}
	}
	if p.Name != "Arch Laptop" || p.Platform != protocol.PlatformLinux || p.Fingerprint != testFP {
		t.Fatalf("unexpected %+v", p)
	}
}

func TestParseEntryRejects(t *testing.T) {
	for name, txt := range map[string]map[string]string{
		"self":        {"id": "self", "v": "1", "fp": testFP},
		"bad id":      {"id": "a b", "v": "1", "fp": testFP},
		"bad fp":      {"id": "peer", "v": "1", "fp": "zz"},
		"bad version": {"id": "peer", "v": "99", "fp": testFP},
		"no txt":      {},
	} {
		if _, ok := ParseEntry(entry(txt), "self"); ok {
			t.Errorf("%s: accepted", name)
		}
	}
	p, ok := ParseEntry(entry(map[string]string{"id": "peer", "v": "1", "fp": testFP, "os": "plan9", "name": "\x01bad"}), "self")
	if !ok || p.Platform != protocol.PlatformOther || p.Name != "Unnamed device" {
		t.Fatalf("sanitization failed: %+v", p)
	}
}

// TestLiveAdvertiseAndBrowse uses real multicast on the host network.
func TestLiveAdvertiseAndBrowse(t *testing.T) {
	if os.Getenv("LUNIBOARD_TEST_MDNS") != "1" {
		t.Skip("set LUNIBOARD_TEST_MDNS=1 to run against the real network")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	adv := New(Options{
		Self: func() protocol.DeviceInfo {
			return protocol.DeviceInfo{ID: "advertiser-123456", Name: "Test Advertiser", Platform: protocol.PlatformLinux}
		},
		Fingerprint: testFP, Port: func() int { return 47999 }, Advertise: true, Logger: logging.Discard(),
	})
	found := make(chan Peer, 1)
	browser := New(Options{
		Self: func() protocol.DeviceInfo {
			return protocol.DeviceInfo{ID: "browser-1", Name: "B", Platform: protocol.PlatformLinux}
		},
		Logger: logging.Discard(),
		OnFound: func(p Peer) {
			if p.DeviceID == "advertiser-123456" {
				select {
				case found <- p:
				default:
				}
			}
		},
	})
	go func() { t.Logf("adv: %v", adv.Run(ctx)) }()
	go func() { t.Logf("browser: %v", browser.Run(ctx)) }()
	select {
	case p := <-found:
		t.Logf("found %+v", p)
	case <-ctx.Done():
		t.Fatal("advertised service not discovered")
	}
}

func TestMergePrefersPhysicalInterfaces(t *testing.T) {
	s := New(Options{Self: func() protocol.DeviceInfo { return protocol.DeviceInfo{ID: "self"} }, Logger: logging.Discard()})
	txt := map[string]string{"id": "peer-1", "v": "1", "fp": testFP, "os": "linux", "name": "Peer"}
	s.add(dnssd.BrowseEntry{IPs: []net.IP{net.ParseIP("172.17.0.1")}, Port: 1, IfaceName: "docker0", Name: "Peer", Text: txt})
	s.add(dnssd.BrowseEntry{IPs: []net.IP{net.ParseIP("192.168.1.9")}, Port: 1, IfaceName: "wlan0", Name: "Peer", Text: txt})
	p, ok := s.Lookup("peer-1")
	if !ok || len(p.Addresses) != 2 || p.Addresses[0] != "192.168.1.9:1" {
		t.Fatalf("addresses %v", p.Addresses)
	}
	if s.add(dnssd.BrowseEntry{IPs: []net.IP{net.ParseIP("192.168.1.9")}, Port: 1, Name: "X", Text: map[string]string{}}) {
		t.Fatal("entry without TXT should be reported incomplete")
	}
}
