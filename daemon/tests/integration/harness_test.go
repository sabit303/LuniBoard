// Package integration runs complete daemon instances in-process (real TLS,
// WebSocket, pairing, local API over Unix sockets) with in-memory clipboards.
package integration

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	adapter "github.com/luniboard/luniboard/daemon/adapters/clipboard"
	"github.com/luniboard/luniboard/daemon/adapters/clipboard/mock"
	"github.com/luniboard/luniboard/daemon/internal/api"
	"github.com/luniboard/luniboard/daemon/internal/application"
	"github.com/luniboard/luniboard/daemon/internal/clipboard"
	"github.com/luniboard/luniboard/daemon/internal/config"
	"github.com/luniboard/luniboard/daemon/internal/logging"
	"github.com/luniboard/luniboard/daemon/internal/transport"
)

// node is one running daemon.
type node struct {
	t      *testing.T
	name   string
	home   string
	clip   *mock.Clipboard
	app    *application.App
	client *api.Client
	cancel context.CancelFunc
	done   chan struct{}
	cfgMut func(*config.Config)
}

// shortTempDir keeps Unix socket paths below the 108-byte limit.
func shortTempDir(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "luniboard-it-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(dir) })
	return dir
}

func startNode(t *testing.T, name string, mut func(*config.Config)) *node {
	t.Helper()
	n := &node{t: t, name: name, home: shortTempDir(t), clip: mock.New(), cfgMut: mut}
	n.start()
	t.Cleanup(n.stop)
	return n
}

func (n *node) start() {
	t := n.t
	t.Helper()
	paths, err := config.ResolvePaths(n.home)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Device.Name = n.name
	cfg.Network.ListenAddress = "127.0.0.1"
	cfg.Network.Port = 0
	cfg.Discovery.Enabled = false
	cfg.History.Persist = true
	if n.cfgMut != nil {
		n.cfgMut(&cfg)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatal(err)
	}
	logger := logging.Discard()
	if os.Getenv("LUNIBOARD_TEST_LOG") == "1" {
		logger, _, _ = logging.New(logging.Options{Level: "debug", Format: "text", Stderr: os.Stderr})
		logger = logger.With("node", n.name)
	}
	clip := n.clip
	n.app, err = application.New(application.Options{
		Paths:  paths,
		Config: cfg,
		Logger: logger,
		DetectClipboard: func() (clipboard.Backend, error) {
			return clipboard.Backend{Adapter: clip, Watcher: clip, PollFallback: clip, Description: "mock"}, nil
		},
		TransportTuning: func(o *transport.Options) {
			o.DialInterval = 100 * time.Millisecond
			o.MaxBackoff = 300 * time.Millisecond
			o.PingInterval = 2 * time.Second
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	n.cancel, n.done = cancel, make(chan struct{})
	go func() {
		defer close(n.done)
		if err := n.app.Run(ctx); err != nil {
			t.Errorf("%s: run: %v", n.name, err)
		}
	}()
	n.client = api.NewClient(filepath.Join(paths.RuntimeDir, "daemon.sock"))
	waitFor(t, n.name+" API", func() bool { return n.client.Ping(200 * time.Millisecond) })
	// Let the clipboard monitor record the initial content before tests copy.
	waitFor(t, n.name+" clipboard", func() bool {
		s, err := n.client.Status(context.Background())
		return err == nil && s.Clipboard.Mode == clipboard.ModeWatch
	})
}

func (n *node) stop() {
	if n.cancel == nil {
		return
	}
	n.cancel()
	select {
	case <-n.done:
	case <-time.After(10 * time.Second):
		n.t.Errorf("%s did not shut down", n.name)
	}
	n.cancel = nil
}

func (n *node) restart() {
	n.stop()
	n.start()
}

func (n *node) id() string { return n.app.Self().ID }

// copy simulates the user copying text in another application.
func (n *node) copy(text string) { n.clip.Copy(adapter.Content{Text: text}) }

func (n *node) connectedTo(other *node) bool {
	list, err := n.client.Devices(context.Background())
	if err != nil {
		return false
	}
	for _, d := range list {
		if d.ID == other.id() {
			return d.Connected
		}
	}
	return false
}

// pair pairs requester with listener through the local APIs of both daemons,
// approving on the listener after checking the verification codes match.
func pair(t *testing.T, requester, listener *node) {
	t.Helper()
	ctx := context.Background()
	inv, err := listener.client.StartPairing(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result := make(chan api.ConnectResponse, 1)
	errs := make(chan error, 1)
	go func() {
		res, err := requester.client.ConnectPairing(ctx, inv.URI)
		if err != nil {
			errs <- err
			return
		}
		result <- res
	}()
	var sas string
	waitFor(t, "pairing request", func() bool {
		st, err := listener.client.PairingState(ctx)
		if err != nil || len(st.Pending) == 0 {
			return false
		}
		sas = st.Pending[0].VerificationCode
		return listener.client.DecidePairing(ctx, st.Pending[0].ID, true) == nil
	})
	select {
	case res := <-result:
		if res.Status != "approved" {
			t.Fatalf("pairing status %s", res.Status)
		}
		if res.VerificationCode != sas {
			t.Fatalf("verification codes differ: %s vs %s", res.VerificationCode, sas)
		}
	case err := <-errs:
		t.Fatal(err)
	case <-time.After(10 * time.Second):
		t.Fatal("pairing timed out")
	}
	waitFor(t, "connection", func() bool { return requester.connectedTo(listener) && listener.connectedTo(requester) })
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func eventually(t *testing.T, what string, cond func() bool) { t.Helper(); waitFor(t, what, cond) }

// consistently asserts cond stays true for d.
func consistently(t *testing.T, what string, d time.Duration, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !cond() {
			t.Fatalf("condition violated: %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
