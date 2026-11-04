package integration

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/luniboard/luniboard/daemon/internal/api"
	"github.com/luniboard/luniboard/daemon/internal/application"
	"github.com/luniboard/luniboard/daemon/internal/config"
	"github.com/luniboard/luniboard/daemon/internal/history"
	"github.com/luniboard/luniboard/daemon/internal/logging"
	"github.com/luniboard/luniboard/daemon/pkg/protocol"
)

func TestDesktopPairingAndBidirectionalSync(t *testing.T) {
	a := startNode(t, "Arch Desktop", nil)
	b := startNode(t, "Ubuntu Laptop", nil)
	pair(t, b, a)

	a.copy("copied on arch")
	eventually(t, "b receives", func() bool { return b.clip.Text() == "copied on arch" })

	b.copy("copied on ubuntu")
	eventually(t, "a receives", func() bool { return a.clip.Text() == "copied on ubuntu" })

	// No ping-pong: each side wrote exactly once and the text stays put.
	consistently(t, "no loop", 500*time.Millisecond, func() bool {
		return a.clip.Writes() == 1 && b.clip.Writes() == 1 && a.clip.Text() == "copied on ubuntu"
	})
	st, _ := a.client.Status(context.Background())
	if st.Stats.Sent != 1 || st.Stats.Applied != 1 {
		t.Fatalf("unexpected stats %+v", st.Stats)
	}
}

func TestMobileClientShareAndReceive(t *testing.T) {
	desktop := startNode(t, "Desktop", nil)
	phone := newMobileClient(t)
	phone.pairWith(desktop)
	phone.connect()
	eventually(t, "phone connected", func() bool {
		list, _ := desktop.client.Devices(context.Background())
		return len(list) == 1 && list[0].Connected && list[0].Platform == protocol.PlatformAndroid
	})

	// Android share sheet -> Linux clipboard.
	ev := phone.share("https://example.com/shared-from-android")
	eventually(t, "desktop clipboard updated", func() bool { return desktop.clip.Text() == "https://example.com/shared-from-android" })
	eventually(t, "ack", func() bool { a, ok := phone.ack(ev.EventID); return ok && a.Status == protocol.AckApplied })

	// Duplicate delivery is detected.
	phone.resend(ev)
	eventually(t, "duplicate ack", func() bool { a, _ := phone.ack(ev.EventID); return a.Status == protocol.AckDuplicate })

	// Linux clipboard -> Android, without echoing the phone's own event back.
	desktop.copy("copied on linux")
	eventually(t, "phone receives", func() bool {
		r := phone.received()
		return len(r) == 1 && r[0].Payload.Text == "copied on linux" && r[0].OriginDeviceID == desktop.id()
	})
	consistently(t, "phone's own content not echoed", 300*time.Millisecond, func() bool { return len(phone.received()) == 1 })

	// The desktop recorded the phone's acknowledgement.
	eventually(t, "delivery recorded", func() bool {
		h, _ := desktop.client.History(context.Background(), 1)
		return len(h) == 1 && h[0].Deliveries[phone.info.ID] == "applied"
	})
}

func TestMultiDeviceFanOutSelectiveSendAndPermissions(t *testing.T) {
	hub := startNode(t, "Hub", nil)
	b := startNode(t, "B", nil)
	c := startNode(t, "C", nil)
	pair(t, b, hub)
	pair(t, c, hub)
	ctx := context.Background()

	hub.copy("to everyone")
	eventually(t, "fan-out", func() bool { return b.clip.Text() == "to everyone" && c.clip.Text() == "to everyone" })

	// Without relay, B's content reaches the hub but not C.
	b.copy("from b")
	eventually(t, "hub receives", func() bool { return hub.clip.Text() == "from b" })
	consistently(t, "c untouched", 300*time.Millisecond, func() bool { return c.clip.Text() == "to everyone" })

	// Manual selective send.
	res, err := hub.client.Send(ctx, "only for c", []string{c.id()})
	if err != nil {
		t.Fatal(err)
	}
	if res.Results[c.id()].Status != "applied" {
		t.Fatalf("send result %+v", res)
	}
	if c.clip.Text() != "only for c" || b.clip.Text() == "only for c" {
		t.Fatal("selective send reached the wrong devices")
	}
	if hub.clip.Text() != "from b" {
		t.Fatal("manual send must not change the local clipboard")
	}

	// Exclude C from automatic sync.
	off := false
	if _, err := hub.client.UpdateDevice(ctx, c.id(), api.UpdateDeviceRequest{Send: &off}); err != nil {
		t.Fatal(err)
	}
	hub.copy("not for c")
	eventually(t, "b receives", func() bool { return b.clip.Text() == "not for c" })
	consistently(t, "c excluded", 300*time.Millisecond, func() bool { return c.clip.Text() == "only for c" })
}

func TestRelayToIndirectPeers(t *testing.T) {
	relay := func(c *config.Config) { c.Sync.Relay = true }
	hub := startNode(t, "Hub", relay)
	b := startNode(t, "B", nil)
	c := startNode(t, "C", nil)
	pair(t, b, hub)
	pair(t, c, hub)
	b.copy("via hub")
	eventually(t, "c receives through relay", func() bool { return c.clip.Text() == "via hub" })
	consistently(t, "single write each", 300*time.Millisecond, func() bool {
		return hub.clip.Writes() == 1 && c.clip.Writes() == 1 && b.clip.Writes() == 0
	})
}

func TestRevocation(t *testing.T) {
	a := startNode(t, "A", nil)
	b := startNode(t, "B", nil)
	pair(t, b, a)
	if err := a.client.RemoveDevice(context.Background(), b.id()); err != nil {
		t.Fatal(err)
	}
	eventually(t, "disconnect", func() bool { return !a.connectedTo(b) && !b.connectedTo(a) })
	b.copy("after revocation")
	consistently(t, "revoked device cannot reach a", time.Second, func() bool {
		return a.clip.Text() != "after revocation" && !a.connectedTo(b)
	})
}

func TestReconnectAfterRestartWithoutStaleReplay(t *testing.T) {
	a := startNode(t, "A", func(c *config.Config) { c.Sync.MaxEventAge = config.Duration{Duration: 2 * time.Second} })
	b := startNode(t, "B", nil)
	pair(t, b, a)

	b.stop()
	a.copy("fresh while b was down")
	b.restart()
	eventually(t, "reconnect", func() bool { return a.connectedTo(b) && b.connectedTo(a) })
	eventually(t, "fresh latest delivered on reconnect", func() bool { return b.clip.Text() == "fresh while b was down" })

	b.stop()
	a.copy("stale by the time b returns")
	time.Sleep(2500 * time.Millisecond)
	b.restart()
	eventually(t, "reconnect again", func() bool { return a.connectedTo(b) && b.connectedTo(a) })
	consistently(t, "stale content not replayed", 500*time.Millisecond, func() bool {
		return b.clip.Text() == "fresh while b was down"
	})
}

func TestPauseHistoryAndLimits(t *testing.T) {
	a := startNode(t, "A", func(c *config.Config) { c.Sync.MaxPayloadBytes = 1024 })
	b := startNode(t, "B", nil)
	pair(t, b, a)
	ctx := context.Background()

	if err := a.client.SetPaused(ctx, true); err != nil {
		t.Fatal(err)
	}
	a.copy("while paused")
	consistently(t, "paused", 300*time.Millisecond, func() bool { return b.clip.Writes() == 0 })
	if err := a.client.SetPaused(ctx, false); err != nil {
		t.Fatal(err)
	}
	a.copy("after resume")
	eventually(t, "resumed", func() bool { return b.clip.Text() == "after resume" })

	// Oversized content is neither sent automatically nor accepted manually.
	a.copy(strings.Repeat("x", 2048))
	consistently(t, "oversize not sent", 300*time.Millisecond, func() bool { return b.clip.Text() == "after resume" })
	if _, err := a.client.Send(ctx, strings.Repeat("y", 2048), nil); err == nil {
		t.Fatal("oversized manual send accepted")
	}

	h, err := a.client.History(ctx, 0)
	if err != nil || len(h) == 0 || h[0].Direction != history.Outgoing {
		t.Fatalf("history %v %v", h, err)
	}
	if err := a.client.ClearHistory(ctx); err != nil {
		t.Fatal(err)
	}
	if h, _ := a.client.History(ctx, 0); len(h) != 0 {
		t.Fatal("history not cleared")
	}
}

func TestSecondInstanceRefused(t *testing.T) {
	a := startNode(t, "A", nil)
	paths, err := config.ResolvePaths(a.home)
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	cfg.Discovery.Enabled = false
	cfg.Network.ListenAddress = "127.0.0.1"
	cfg.Network.Port = 0
	cfg.Clipboard.Backend = "none"
	app, err := application.New(application.Options{Paths: paths, Config: cfg, Logger: logging.Discard()})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := app.Run(ctx); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("expected 'already running' error, got %v", err)
	}
	if !a.client.Ping(time.Second) {
		t.Fatal("first instance was disturbed")
	}
}
