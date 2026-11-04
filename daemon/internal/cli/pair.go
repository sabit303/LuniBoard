package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/mdp/qrterminal/v3"

	"github.com/luniboard/luniboard/daemon/internal/api"
	"github.com/luniboard/luniboard/daemon/internal/pairing"
	"github.com/luniboard/luniboard/daemon/pkg/protocol"
)

func runPair(ctx context.Context, g *globals, args []string) error {
	if len(args) > 0 {
		switch args[0] {
		case "connect":
			return pairConnect(ctx, g, args[1:])
		case "approve", "reject":
			if len(args) != 2 {
				return usageError("usage: pair " + args[0] + " <request-id>")
			}
			cctx, cancel := timeoutCtx(ctx)
			defer cancel()
			if err := g.client().DecidePairing(cctx, args[1], args[0] == "approve"); err != nil {
				return err
			}
			g.printf("Pairing request %sd.\n", args[0])
			return nil
		case "cancel":
			cctx, cancel := timeoutCtx(ctx)
			defer cancel()
			if err := g.client().CancelPairing(cctx); err != nil {
				return err
			}
			g.printf("Pairing window closed.\n")
			return nil
		case "status":
			cctx, cancel := timeoutCtx(ctx)
			defer cancel()
			st, err := g.client().PairingState(cctx)
			if err != nil {
				return err
			}
			if g.json {
				return g.printJSON(st)
			}
			printPairingState(g, st)
			return nil
		}
	}
	return pairInteractive(ctx, g, args)
}

func printPairingState(g *globals, st pairing.State) {
	if !st.Open {
		g.printf("Pairing window: closed\n")
	} else {
		g.printf("Pairing window: open until %s, code %s\n", st.Invitation.ExpiresAt.Local().Format(time.Kitchen), st.Invitation.Code)
	}
	for _, r := range st.Pending {
		g.printf("Pending request %s from %q (%s) at %s — verification code %s\n",
			r.ID, r.Device.Name, r.Device.Platform, r.RemoteAddr, r.VerificationCode)
	}
}

// pairInteractive opens a pairing window, shows the QR code and waits for a
// request that the user must approve after comparing verification codes.
func pairInteractive(ctx context.Context, g *globals, args []string) error {
	fs := newFlags("pair", g)
	noQR := fs.Bool("no-qr", false, "do not print the QR code")
	if err := fs.Parse(args); err != nil {
		return usageError(err.Error())
	}
	c := g.client()
	cctx, cancel := timeoutCtx(ctx)
	inv, err := c.StartPairing(cctx)
	cancel()
	if err != nil {
		return err
	}
	if g.json {
		return g.printJSON(inv)
	}
	// Close the window if we exit before a successful pairing.
	paired := false
	defer func() {
		if !paired {
			cctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			defer cancel()
			_ = c.CancelPairing(cctx)
		}
	}()

	g.printf("Pairing is open until %s.\n\n", inv.ExpiresAt.Local().Format(time.Kitchen))
	if !*noQR {
		g.printf("Scan this QR code with the LuniBoard app:\n\n")
		qrterminal.GenerateHalfBlock(inv.URI, qrterminal.L, g.env.Stdout)
		g.printf("\n")
	}
	g.printf("Or enter manually on the other device:\n")
	g.printf("  Pairing code:  %s\n", inv.Code)
	g.printf("  Addresses:     %s\n", strings.Join(inv.Addresses, ", "))
	g.printf("  Fingerprint:   %s\n", formatFingerprint(inv.Fingerprint))
	g.printf("To pair another desktop, run on it:\n  luniboard pair connect '%s'\n\n", inv.URI)
	g.printf("Waiting for a pairing request (Ctrl+C to cancel)...\n")

	interactive := isTerminal(g.env.Stdin)
	reader := bufio.NewReader(g.env.Stdin)
	handled := map[string]bool{}
	for {
		select {
		case <-ctx.Done():
			g.printf("\nPairing cancelled.\n")
			return nil
		case <-time.After(300 * time.Millisecond):
		}
		cctx, cancel := timeoutCtx(ctx)
		st, err := c.PairingState(cctx)
		cancel()
		if err != nil {
			return err
		}
		for _, r := range st.Pending {
			if handled[r.ID] {
				continue
			}
			handled[r.ID] = true
			g.printf("\nPairing request from %q (%s) at %s\n", r.Device.Name, r.Device.Platform, r.RemoteAddr)
			g.printf("Verification code: %s\n", r.VerificationCode)
			g.printf("Check that the other device shows the same code.\n")
			if !interactive {
				g.printf("Approve with: luniboard pair approve %s\n", r.ID)
				continue
			}
			g.printf("Approve this device? [y/N]: ")
			line, ok := readLine(ctx, reader)
			if !ok {
				g.printf("\nPairing cancelled.\n")
				return nil
			}
			approve := strings.EqualFold(strings.TrimSpace(line), "y") || strings.EqualFold(strings.TrimSpace(line), "yes")
			cctx, cancel := timeoutCtx(ctx)
			err := c.DecidePairing(cctx, r.ID, approve)
			cancel()
			if err != nil {
				return fmt.Errorf("the request is no longer pending: %w", err)
			}
			if !approve {
				g.printf("Rejected.\n")
				continue
			}
			paired = true
			g.printf("Paired with %q. It will connect automatically.\n", r.Device.Name)
			return nil
		}
		if !st.Open && len(st.Pending) == 0 {
			if len(handled) > 0 && !interactive {
				return nil
			}
			return errors.New("the pairing window closed (expired or too many wrong codes); run 'pair' again")
		}
	}
}

// pairConnect pairs this daemon with another listening device (desktop to desktop).
func pairConnect(ctx context.Context, g *globals, args []string) error {
	if len(args) != 1 {
		return usageError("usage: pair connect '<luniboard://pair?... URI shown by the other device>'")
	}
	inv, err := protocol.ParsePairingURI(args[0])
	if err != nil {
		return usageError("invalid pairing URI: " + err.Error())
	}
	c := g.client()
	cctx, cancel := timeoutCtx(ctx)
	st, err := c.Status(cctx)
	cancel()
	if err != nil {
		return err
	}
	g.printf("Requesting pairing with %q...\n", inv.Device.Name)
	g.printf("Verification code: %s — approve on the other device only if it shows the same code.\n",
		protocol.VerificationCode(st.Fingerprint, inv.Fingerprint))
	res, err := c.ConnectPairing(ctx, args[0])
	if err != nil {
		var ae *api.APIError
		if errors.As(err, &ae) {
			return fmt.Errorf("pairing failed: %s", ae.Message)
		}
		return err
	}
	if g.json {
		return g.printJSON(res)
	}
	switch res.Status {
	case protocol.PairApproved:
		g.printf("Paired with %q.\n", res.Device.Name)
		return nil
	case protocol.PairRejected:
		return errors.New("the other device rejected the pairing request")
	default:
		return errors.New("the pairing window on the other device expired")
	}
}

func runUnpair(ctx context.Context, g *globals, args []string) error {
	if len(args) != 1 {
		return usageError("usage: unpair <device id or name>")
	}
	c := g.client()
	cctx, cancel := timeoutCtx(ctx)
	defer cancel()
	d, err := resolveDevice(cctx, c, args[0])
	if err != nil {
		return err
	}
	if err := c.RemoveDevice(cctx, d.ID); err != nil {
		return err
	}
	g.printf("Unpaired %q; its credentials are revoked. Remove this computer on that device too.\n", d.Name)
	return nil
}

// readLine reads one line unless ctx is cancelled first (Ctrl+C).
func readLine(ctx context.Context, r *bufio.Reader) (string, bool) {
	ch := make(chan string, 1)
	go func() {
		line, _ := r.ReadString('\n')
		ch <- line
	}()
	select {
	case <-ctx.Done():
		return "", false
	case line := <-ch:
		return line, true
	}
}
