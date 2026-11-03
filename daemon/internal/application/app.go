// Package application wires the daemon's components together and manages
// their lifecycle. It contains no protocol or platform logic of its own.
package application

import (
	"context"
	"io"
	"log/slog"
	"net"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/universal-clipboard/universal-clipboard/daemon/internal/api"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/clipboard"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/config"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/device"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/discovery"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/history"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/pairing"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/security"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/synchronization"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/transport"
	"github.com/universal-clipboard/universal-clipboard/daemon/pkg/protocol"
)

// Version is the application version, overridable at build time with
// -ldflags "-X .../internal/application.Version=1.2.3".
var Version = "0.1.0-dev"

// Options configures an App.
type Options struct {
	Paths  config.Paths
	Config config.Config
	Logger *slog.Logger
	// DetectClipboard overrides clipboard backend detection (tests).
	DetectClipboard func() (clipboard.Backend, error)
	// TransportTuning lets tests shorten timers.
	TransportTuning func(*transport.Options)
}

// App is a running daemon instance.
type App struct {
	opts      Options
	cfg       config.Config
	paths     config.Paths
	log       *slog.Logger
	identity  *security.Identity
	devices   *device.Registry
	history   *history.Store
	engine    *synchronization.Engine
	transport *transport.Manager
	pairing   *pairing.Manager
	discovery *discovery.Service
	clipboard *clipboard.Service
	api       *api.Server
	startedAt time.Time

	shutdownOnce sync.Once
	shutdown     chan struct{}
}

// PlatformName maps GOOS to a protocol platform.
func PlatformName() protocol.Platform {
	switch runtime.GOOS {
	case "linux":
		return protocol.PlatformLinux
	case "darwin":
		return protocol.PlatformMacOS
	case "windows":
		return protocol.PlatformWindows
	default:
		return protocol.PlatformOther
	}
}

// New constructs all components. Nothing touches the network until Run.
func New(opts Options) (*App, error) {
	if opts.Logger == nil {
		opts.Logger = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if err := opts.Paths.Ensure(); err != nil {
		return nil, err
	}
	cfg := opts.Config
	a := &App{opts: opts, cfg: cfg, paths: opts.Paths, log: opts.Logger, shutdown: make(chan struct{})}

	var err error
	if a.identity, err = security.LoadOrCreateIdentity(opts.Paths.IdentityDir()); err != nil {
		return nil, err
	}
	if a.devices, err = device.Open(opts.Paths.DevicesFile()); err != nil {
		return nil, err
	}
	if a.history, err = history.Open(history.Options{
		Enabled:    cfg.History.Enabled,
		Persist:    cfg.History.Persist,
		MaxEntries: cfg.History.MaxEntries,
		Path:       opts.Paths.HistoryFile(),
		Logger:     a.log,
	}); err != nil {
		return nil, err
	}

	a.clipboard = clipboard.NewService(clipboard.Options{
		Backend:      cfg.Clipboard.Backend,
		PollInterval: cfg.Clipboard.PollInterval.Duration,
		MaxBytes:     cfg.Sync.MaxPayloadBytes,
		Logger:       a.log.With("component", "clipboard"),
		Detect:       opts.DetectClipboard,
	})

	a.pairing = pairing.NewManager(pairing.Options{
		Self:        a.Self,
		Fingerprint: a.identity.Fingerprint,
		ListenPort:  func() int { return a.transport.Port() },
		Addresses:   a.invitationAddresses,
		Registrar:   a.devices,
		Window:      cfg.Pairing.Window.Duration,
		MaxAttempts: cfg.Pairing.MaxAttempts,
	})
	topts := transport.Options{
		Identity:        a.identity,
		Self:            a.Self,
		ListenAddress:   cfg.Network.ListenAddress,
		Port:            cfg.Network.Port,
		MaxConnections:  cfg.Network.MaxConnections,
		MaxPayloadBytes: cfg.Sync.MaxPayloadBytes,
		EventsPerSecond: cfg.Security.EventsPerSecond,
		EventBurst:      cfg.Security.EventBurst,
		Devices:         a.devices,
		Pairer:          a.pairing,
		Addresses:       a.peerAddresses,
		Logger:          a.log.With("component", "transport"),
	}
	if opts.TransportTuning != nil {
		opts.TransportTuning(&topts)
	}
	a.transport = transport.New(topts)

	excluded := make([]protocol.ContentType, 0, len(cfg.Sync.ExcludedContentTypes))
	for _, t := range cfg.Sync.ExcludedContentTypes {
		excluded = append(excluded, protocol.ContentType(t))
	}
	var clip synchronization.Clipboard
	if a.clipboard.Enabled() {
		clip = a.clipboard
	}
	a.engine = synchronization.NewEngine(synchronization.Options{
		Self: a.Self(),
		Settings: synchronization.Settings{
			AutoSend:             cfg.Sync.AutoSend,
			Receive:              cfg.Sync.Receive,
			SkipSensitive:        cfg.Clipboard.SkipSensitive,
			Relay:                cfg.Sync.Relay,
			MaxPayloadBytes:      cfg.Sync.MaxPayloadBytes,
			MaxEventAge:          cfg.Sync.MaxEventAge.Duration,
			OrderingTolerance:    cfg.Sync.OrderingTolerance.Duration,
			ExcludedContentTypes: excluded,
		},
		Clipboard: clip,
		Peers:     peersAdapter{a.transport},
		Devices:   a.devices,
		History:   a.history,
		Logger:    a.log.With("component", "sync"),
		Paused:    !cfg.Sync.Enabled,
	})
	a.transport.SetHandler(a.engine)

	a.devices.Subscribe(func(c device.Change) {
		switch c.Kind {
		case device.Removed:
			a.log.Info("device unpaired; closing its connection", "device_id", c.Device.ID, "device", c.Device.Name)
			a.transport.Disconnect(c.Device.ID, "device unpaired")
		case device.Added:
			a.log.Info("device paired", "device_id", c.Device.ID, "device", c.Device.Name, "platform", c.Device.Platform)
			a.transport.Trigger(c.Device.ID)
		}
	})

	if cfg.Discovery.Enabled {
		a.discovery = discovery.New(discovery.Options{
			Self:        a.Self,
			Fingerprint: a.identity.Fingerprint,
			Port:        a.transport.Port,
			Advertise:   cfg.Discovery.Advertise,
			Interfaces:  cfg.Discovery.Interfaces,
			Logger:      a.log.With("component", "discovery"),
			OnFound: func(p discovery.Peer) {
				if d, ok := a.devices.Get(p.DeviceID); ok && d.Fingerprint == p.Fingerprint {
					a.transport.Trigger(p.DeviceID)
				}
			},
		})
	}
	a.api = api.NewServer(a, a.log.With("component", "api"))
	return a, nil
}

// Self returns this device's protocol description.
func (a *App) Self() protocol.DeviceInfo {
	return protocol.DeviceInfo{ID: a.identity.DeviceID, Name: a.cfg.Device.Name, Platform: PlatformName(), AppVersion: Version}
}

// Identity exposes the TLS identity (tests).
func (a *App) Identity() *security.Identity { return a.identity }

// Port returns the bound LAN port.
func (a *App) Port() int { return a.transport.Port() }

// Run starts all components and blocks until ctx is cancelled or a shutdown
// is requested through the local API, then shuts down gracefully.
func (a *App) Run(ctx context.Context) error {
	a.startedAt = time.Now()
	if err := a.api.Listen(a.paths.SocketPath()); err != nil {
		return err
	}
	if err := a.transport.Listen(); err != nil {
		_ = a.api.Close()
		return err
	}
	if err := a.transport.Start(); err != nil {
		_ = a.api.Close()
		return err
	}

	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); a.api.Serve() }()
	go func() { defer wg.Done(); a.clipboard.Run(ctx, a.engine) }()
	if a.discovery != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := a.discovery.Run(ctx); err != nil {
				a.log.Warn("discovery stopped", "error", err)
			}
		}()
	}
	a.log.Info("daemon started",
		"version", Version,
		"device_id", a.identity.DeviceID,
		"name", a.cfg.Device.Name,
		"port", a.transport.Port(),
		"socket", a.paths.SocketPath(),
		"paired_devices", len(a.devices.List()),
		"sync_paused", a.engine.Paused())

	select {
	case <-ctx.Done():
	case <-a.shutdown:
	}
	a.log.Info("shutting down")
	cancel()
	a.pairing.Cancel()
	_ = a.transport.Close()
	_ = a.api.Close()
	wg.Wait()
	a.log.Info("stopped")
	return nil
}

// RequestShutdown asks Run to return.
func (a *App) RequestShutdown() {
	a.shutdownOnce.Do(func() { close(a.shutdown) })
}

// peerAddresses returns dial candidates: fresh mDNS results first (only when
// they advertise the pinned fingerprint), then remembered addresses.
func (a *App) peerAddresses(id string) []string {
	d, ok := a.devices.Get(id)
	if !ok {
		return nil
	}
	var out []string
	if a.discovery != nil {
		if p, ok := a.discovery.Lookup(id); ok && p.Fingerprint == d.Fingerprint {
			out = append(out, p.Addresses...)
		}
	}
	for _, addr := range d.Addresses {
		if !slices.Contains(out, addr) {
			out = append(out, addr)
		}
	}
	return out
}

// invitationAddresses lists host:port pairs to embed in pairing invitations.
func (a *App) invitationAddresses() []string {
	port := strconv.Itoa(a.transport.Port())
	if la := a.cfg.Network.ListenAddress; la != "" {
		if ip := net.ParseIP(la); ip != nil && !ip.IsUnspecified() {
			return []string{net.JoinHostPort(la, port)}
		}
	}
	ips := LocalIPs()
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		out = append(out, net.JoinHostPort(ip, port))
	}
	if len(out) == 0 {
		out = append(out, net.JoinHostPort("127.0.0.1", port))
	}
	return out
}

// virtualInterfacePrefixes are skipped when choosing addresses to advertise in invitations.
var virtualInterfacePrefixes = []string{"lo", "docker", "veth", "br-", "virbr", "vmnet", "vboxnet", "tun", "tap", "wg", "zt", "tailscale", "podman", "cni", "flannel"}

// LocalIPs returns usable LAN addresses, private IPv4 first, at most four.
func LocalIPs() []string {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var v4, v6 []string
	for _, iface := range ifaces {
		if iface.Flags&net.FlagUp == 0 || iface.Flags&net.FlagLoopback != 0 {
			continue
		}
		skip := false
		for _, p := range virtualInterfacePrefixes {
			if strings.HasPrefix(iface.Name, p) {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		addrs, _ := iface.Addrs()
		for _, addr := range addrs {
			ipn, ok := addr.(*net.IPNet)
			if !ok {
				continue
			}
			ip := ipn.IP
			switch {
			case ip.To4() != nil && (ip.IsPrivate() || ip.IsGlobalUnicast()):
				v4 = append(v4, ip.String())
			case ip.To4() == nil && ip.IsGlobalUnicast():
				v6 = append(v6, ip.String())
			}
		}
	}
	slices.SortStableFunc(v4, func(x, y string) int {
		return boolRank(!net.ParseIP(x).IsPrivate()) - boolRank(!net.ParseIP(y).IsPrivate())
	})
	out := append(v4, v6...)
	if len(out) > 4 {
		out = out[:4]
	}
	return out
}

func boolRank(b bool) int {
	if b {
		return 1
	}
	return 0
}

// peersAdapter exposes transport sessions to the synchronization engine.
type peersAdapter struct{ tm *transport.Manager }

func (p peersAdapter) Connected() []synchronization.PeerInfo {
	sessions := p.tm.Sessions()
	out := make([]synchronization.PeerInfo, 0, len(sessions))
	for _, s := range sessions {
		out = append(out, synchronization.PeerInfo{ID: s.DeviceID, CanReceive: s.CanReceive, MaxPayloadBytes: s.MaxPayloadBytes})
	}
	return out
}

func (p peersAdapter) Send(id string, msg []byte) error { return p.tm.Send(id, msg) }
