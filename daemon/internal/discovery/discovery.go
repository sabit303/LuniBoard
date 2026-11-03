// Package discovery advertises this device and finds peers on the local
// network with mDNS / DNS-SD. Discovery results are hints only: they never
// grant access. Connections are always authenticated with pinned certificates.
package discovery

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/brutella/dnssd"
	dnssdlog "github.com/brutella/dnssd/log"

	"github.com/universal-clipboard/universal-clipboard/daemon/pkg/protocol"
)

// TXT record keys.
const (
	txtID       = "id"
	txtVersion  = "v"
	txtFP       = "fp"
	txtPlatform = "os"
	txtName     = "name"
)

// Peer is a device found on the network.
type Peer struct {
	DeviceID    string            `json:"device_id"`
	Name        string            `json:"name"`
	Platform    protocol.Platform `json:"platform"`
	Fingerprint string            `json:"fingerprint"`
	Addresses   []string          `json:"addresses"`
	Version     int               `json:"protocol_version"`
	LastSeen    time.Time         `json:"last_seen"`
	// virtual marks entries received on container/VM bridge interfaces.
	virtual bool
}

// Options configures the discovery service.
type Options struct {
	Self        func() protocol.DeviceInfo
	Fingerprint string
	Port        func() int
	Advertise   bool
	Interfaces  []string
	Logger      *slog.Logger
	// OnFound is called (from the browse goroutine) whenever a peer appears or changes.
	OnFound func(Peer)
}

// Service runs mDNS advertisement and browsing.
type Service struct {
	opts  Options
	log   *slog.Logger
	mu    sync.RWMutex
	peers map[string]Peer
	// byInstance maps DNS-SD instance names to device IDs for removals.
	byInstance map[string]string
}

// New creates a discovery service.
func New(opts Options) *Service {
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	// The library logs to stdout by default; route nothing there.
	dnssdlog.Info.Disable()
	return &Service{opts: opts, log: opts.Logger, peers: make(map[string]Peer), byInstance: make(map[string]string)}
}

// TXT builds the TXT record advertised for a device.
func TXT(self protocol.DeviceInfo, fingerprint string) map[string]string {
	return map[string]string{
		txtID:       self.ID,
		txtVersion:  strconv.Itoa(protocol.Version),
		txtFP:       fingerprint,
		txtPlatform: string(self.Platform),
		txtName:     self.Name,
	}
}

// Browse cycle tuning. The DNS-SD library sends its PTR query only when a
// browse starts and may emit an entry before its TXT record has arrived (it
// learns SRV/A records from other hosts' probe packets), so browsing is
// restarted periodically, and shortly after an incomplete entry.
const (
	browseCycle     = time.Minute
	incompleteRetry = 2 * time.Second
	peerExpiry      = 5 * time.Minute
)

// Run advertises (if enabled) and browses until ctx is done.
func (s *Service) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	defer wg.Wait()
	if s.opts.Advertise {
		if err := s.startResponder(ctx, &wg); err != nil {
			s.log.Warn("mDNS advertisement unavailable", "error", err)
		}
	}
	failures := 0
	for ctx.Err() == nil {
		cctx, cancel := context.WithTimeout(ctx, browseCycle)
		incomplete := make(chan struct{}, 1)
		go func() {
			select {
			case <-incomplete:
				select {
				case <-time.After(incompleteRetry):
				case <-cctx.Done():
				}
				cancel()
			case <-cctx.Done():
			}
		}()
		err := dnssd.LookupType(cctx, protocol.ServiceType+".local.", func(e dnssd.BrowseEntry) {
			if !s.add(e) {
				select {
				case incomplete <- struct{}{}:
				default:
				}
			}
		}, s.remove)
		cancel()
		if ctx.Err() != nil {
			return nil
		}
		if err != nil && !errors.Is(err, context.DeadlineExceeded) && !errors.Is(err, context.Canceled) {
			failures++
			s.log.Warn("mDNS browsing failed", "error", err)
			select {
			case <-ctx.Done():
			case <-time.After(time.Duration(min(failures, 6)) * 10 * time.Second):
			}
			continue
		}
		failures = 0
	}
	return nil
}

func (s *Service) startResponder(ctx context.Context, wg *sync.WaitGroup) error {
	self := s.opts.Self()
	cfg := dnssd.Config{
		Name:   self.Name,
		Type:   protocol.ServiceType,
		Domain: "local",
		// A dedicated host name avoids clashing with the system's own mDNS responder (Avahi, Bonjour).
		Host:   "uclip-" + strings.ReplaceAll(self.ID, "-", "")[:12],
		Port:   s.opts.Port(),
		Text:   TXT(self, s.opts.Fingerprint),
		Ifaces: s.opts.Interfaces,
	}
	svc, err := dnssd.NewService(cfg)
	if err != nil {
		return err
	}
	rp, err := dnssd.NewResponder()
	if err != nil {
		return err
	}
	if _, err := rp.Add(svc); err != nil {
		return err
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		if err := rp.Respond(ctx); err != nil && ctx.Err() == nil {
			s.log.Warn("mDNS responder stopped", "error", err)
		}
	}()
	s.log.Info("advertising via mDNS", "service", protocol.ServiceType, "name", self.Name, "port", cfg.Port)
	return nil
}

// ParseEntry validates a browse result. It returns false for entries that
// are malformed, incompatible or describe this device.
func ParseEntry(e dnssd.BrowseEntry, selfID string) (Peer, bool) {
	id := e.Text[txtID]
	fp := e.Text[txtFP]
	if !protocol.ValidID(id) || id == selfID || !protocol.ValidFingerprint(fp) {
		return Peer{}, false
	}
	v, _ := strconv.Atoi(e.Text[txtVersion])
	if !protocol.SupportsVersion(v) || e.Port <= 0 || e.Port > 65535 {
		return Peer{}, false
	}
	name := e.Text[txtName]
	if name == "" {
		name = e.Name
	}
	info := protocol.DeviceInfo{ID: id, Name: name, Platform: protocol.Platform(e.Text[txtPlatform])}
	if !info.Platform.Valid() {
		info.Platform = protocol.PlatformOther
	}
	if protocol.ValidateDeviceInfo(info) != nil {
		info.Name = "Unnamed device"
	}
	p := Peer{DeviceID: id, Name: info.Name, Platform: info.Platform, Fingerprint: fp, Version: v, LastSeen: time.Now().UTC()}
	for _, ip := range e.IPs {
		host := ip.String()
		if ip.IsLinkLocalUnicast() && ip.To4() == nil {
			if e.IfaceName == "" {
				continue
			}
			host += "%" + e.IfaceName
		}
		if ip.IsLoopback() || ip.IsUnspecified() || ip.IsMulticast() {
			continue
		}
		p.Addresses = append(p.Addresses, net.JoinHostPort(host, strconv.Itoa(e.Port)))
	}
	if isVirtualInterface(e.IfaceName) {
		p.virtual = true
	}
	sortAddresses(p.Addresses)
	return p, len(p.Addresses) > 0
}

// virtualInterfacePrefixes identify container/VM bridges whose addresses are
// rarely reachable from other machines.
var virtualInterfacePrefixes = []string{"docker", "veth", "br-", "virbr", "vmnet", "vboxnet", "podman", "cni", "flannel", "lxc", "lxd"}

func isVirtualInterface(name string) bool {
	for _, p := range virtualInterfacePrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// sortAddresses prefers IPv4, then global IPv6, then link-local IPv6.
func sortAddresses(addrs []string) {
	slices.SortStableFunc(addrs, func(a, b string) int { return addrRank(a) - addrRank(b) })
}

func addrRank(a string) int {
	switch {
	case !strings.HasPrefix(a, "["):
		return 0
	case strings.Contains(a, "%"):
		return 2
	default:
		return 1
	}
}

// add records a browse entry. It returns false when the entry lacked the TXT
// data needed to identify the device (so browsing should be retried).
func (s *Service) add(e dnssd.BrowseEntry) bool {
	if e.Text[txtID] == "" {
		return false
	}
	p, ok := ParseEntry(e, s.opts.Self().ID)
	if !ok {
		return true
	}
	s.mu.Lock()
	if old, ok := s.peers[p.DeviceID]; ok {
		// Entries arrive once per interface. Merge them, keeping addresses
		// learned on physical interfaces ahead of container/VM bridges.
		first, second := old, p
		if old.virtual && !p.virtual {
			first, second = p, old
		}
		merged := slices.Clone(first.Addresses)
		for _, a := range second.Addresses {
			if !slices.Contains(merged, a) {
				merged = append(merged, a)
			}
		}
		if len(merged) > 16 {
			merged = merged[:16]
		}
		p.Addresses = merged
		p.virtual = old.virtual && p.virtual
	}
	s.peers[p.DeviceID] = p
	s.byInstance[e.EscapedServiceInstanceName()] = p.DeviceID
	s.mu.Unlock()
	s.log.Debug("discovered device", "device_id", p.DeviceID, "name", p.Name, "addresses", p.Addresses)
	if s.opts.OnFound != nil {
		s.opts.OnFound(p)
	}
	return true
}

func (s *Service) remove(e dnssd.BrowseEntry) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := e.EscapedServiceInstanceName()
	if id, ok := s.byInstance[key]; ok {
		delete(s.byInstance, key)
		delete(s.peers, id)
		s.log.Debug("device left the network", "device_id", id)
	}
}

// Peers returns the currently visible devices.
func (s *Service) Peers() []Peer {
	s.mu.RLock()
	defer s.mu.RUnlock()
	out := make([]Peer, 0, len(s.peers))
	for _, p := range s.peers {
		if time.Since(p.LastSeen) > peerExpiry {
			continue
		}
		p.Addresses = slices.Clone(p.Addresses)
		out = append(out, p)
	}
	slices.SortFunc(out, func(a, b Peer) int { return strings.Compare(a.Name, b.Name) })
	return out
}

// Lookup returns the discovered peer with the given ID.
func (s *Service) Lookup(id string) (Peer, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	p, ok := s.peers[id]
	if !ok || time.Since(p.LastSeen) > peerExpiry {
		return Peer{}, false
	}
	p.Addresses = slices.Clone(p.Addresses)
	return p, true
}
