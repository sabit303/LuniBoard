package pairing

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/luniboard/luniboard/daemon/internal/security"
	"github.com/luniboard/luniboard/daemon/pkg/protocol"
)

// RemoteError is an error reported by the remote device.
type RemoteError struct {
	Status  int
	Code    string
	Message string
}

func (e *RemoteError) Error() string {
	return fmt.Sprintf("remote device refused pairing (%d %s): %s", e.Status, e.Code, e.Message)
}

// Result is the outcome of a successful pairing request.
type Result struct {
	Response protocol.PairResponse
	// Address is the host:port that answered.
	Address string
}

// RequestPairing sends a pairing request to the device described by inv,
// trying each advertised address in order. The server certificate is pinned
// to inv.Fingerprint, so the request (including the code) can only reach the
// device that generated the invitation.
func RequestPairing(ctx context.Context, id *security.Identity, req protocol.PairRequest, inv protocol.PairingInvitation) (Result, error) {
	body, err := json.Marshal(req)
	if err != nil {
		return Result{}, err
	}
	dialer := &net.Dialer{Timeout: 5 * time.Second}
	client := &http.Client{
		Transport: &http.Transport{
			TLSClientConfig:     security.ClientTLSConfig(id, inv.Fingerprint),
			DialContext:         dialer.DialContext,
			TLSHandshakeTimeout: 10 * time.Second,
			ForceAttemptHTTP2:   false,
		},
	}
	defer client.CloseIdleConnections()

	var errs []error
	for _, addr := range inv.Addresses {
		res, err := tryAddress(ctx, client, addr, body)
		if err == nil {
			if res.Response.Status == protocol.PairApproved && res.Response.Device.ID != inv.Device.ID {
				return Result{}, errors.New("remote device identity does not match the invitation")
			}
			return res, nil
		}
		var remote *RemoteError
		var mismatch *security.FingerprintMismatchError
		if errors.As(err, &remote) || errors.As(err, &mismatch) || ctx.Err() != nil {
			return Result{}, err
		}
		errs = append(errs, fmt.Errorf("%s: %w", addr, err))
	}
	return Result{}, fmt.Errorf("could not reach device: %w", errors.Join(errs...))
}

func tryAddress(ctx context.Context, client *http.Client, addr string, body []byte) (Result, error) {
	hreq, err := http.NewRequestWithContext(ctx, http.MethodPost, "https://"+addr+protocol.PathPair, bytes.NewReader(body))
	if err != nil {
		return Result{}, err
	}
	hreq.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(hreq)
	if err != nil {
		return Result{}, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
	if err != nil {
		return Result{}, err
	}
	if resp.StatusCode != http.StatusOK {
		var he protocol.HTTPError
		_ = json.Unmarshal(raw, &he)
		return Result{}, &RemoteError{Status: resp.StatusCode, Code: he.Error.Code, Message: he.Error.Message}
	}
	var pr protocol.PairResponse
	if err := json.Unmarshal(raw, &pr); err != nil {
		return Result{}, fmt.Errorf("decode pairing response: %w", err)
	}
	if pr.Status == protocol.PairApproved {
		if err := protocol.ValidateDeviceInfo(pr.Device); err != nil {
			return Result{}, err
		}
		if pr.ListenPort < 0 || pr.ListenPort > 65535 {
			return Result{}, errors.New("invalid listen_port in pairing response")
		}
	}
	return Result{Response: pr, Address: addr}, nil
}
