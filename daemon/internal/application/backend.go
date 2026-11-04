package application

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/luniboard/luniboard/daemon/internal/api"
	"github.com/luniboard/luniboard/daemon/internal/config"
	"github.com/luniboard/luniboard/daemon/internal/device"
	"github.com/luniboard/luniboard/daemon/internal/history"
	"github.com/luniboard/luniboard/daemon/internal/pairing"
	"github.com/luniboard/luniboard/daemon/internal/synchronization"
	"github.com/luniboard/luniboard/daemon/internal/transport"
	"github.com/luniboard/luniboard/daemon/pkg/protocol"
)

// The App is the local API backend.
var _ api.Backend = (*App)(nil)

// Status implements api.Backend.
func (a *App) Status() api.StatusResponse {
	return api.StatusResponse{
		Version:          Version,
		Device:           a.Self(),
		Fingerprint:      a.identity.Fingerprint,
		ListenPort:       a.transport.Port(),
		Addresses:        a.invitationAddresses(),
		StartedAt:        a.startedAt.UTC(),
		Paused:           a.engine.Paused(),
		Clipboard:        a.clipboard.Status(),
		DiscoveryEnabled: a.discovery != nil,
		PairingOpen:      a.pairing.State().Open,
		PairedDevices:    len(a.devices.List()),
		ConnectedDevices: len(a.transport.Sessions()),
		Stats:            a.engine.Stats(),
		ConfigFile:       a.paths.ConfigFile(),
		LogFile:          a.paths.LogFile(),
	}
}

func (a *App) view(d device.Device, sessions []transport.SessionInfo) api.DeviceView {
	v := api.DeviceView{Device: d}
	for i := range sessions {
		if sessions[i].DeviceID == d.ID {
			s := sessions[i]
			v.Connected, v.Session = true, &s
		}
	}
	return v
}

// Devices implements api.Backend.
func (a *App) Devices() []api.DeviceView {
	sessions := a.transport.Sessions()
	list := a.devices.List()
	out := make([]api.DeviceView, 0, len(list))
	for _, d := range list {
		out = append(out, a.view(d, sessions))
	}
	return out
}

// UpdateDevice implements api.Backend.
func (a *App) UpdateDevice(id string, req api.UpdateDeviceRequest) (api.DeviceView, error) {
	d, ok := a.devices.Get(id)
	if !ok {
		return api.DeviceView{}, fmt.Errorf("device %q %w", id, api.ErrNotFound)
	}
	p := d.Permissions
	if req.Send != nil {
		p.Send = *req.Send
	}
	if req.Receive != nil {
		p.Receive = *req.Receive
	}
	d, err := a.devices.SetPermissions(id, p)
	if err != nil {
		return api.DeviceView{}, err
	}
	return a.view(d, a.transport.Sessions()), nil
}

// RemoveDevice implements api.Backend.
func (a *App) RemoveDevice(id string) error {
	if _, err := a.devices.Remove(id); err != nil {
		if errors.Is(err, device.ErrNotFound) {
			return fmt.Errorf("device %q %w", id, api.ErrNotFound)
		}
		return err
	}
	return nil
}

// Discovered implements api.Backend.
func (a *App) Discovered() []api.DiscoveredView {
	if a.discovery == nil {
		return []api.DiscoveredView{}
	}
	peers := a.discovery.Peers()
	out := make([]api.DiscoveredView, 0, len(peers))
	for _, p := range peers {
		d, ok := a.devices.Get(p.DeviceID)
		out = append(out, api.DiscoveredView{Peer: p, Paired: ok && d.Fingerprint == p.Fingerprint})
	}
	return out
}

// StartPairing implements api.Backend.
func (a *App) StartPairing() pairing.Invitation {
	inv := a.pairing.Start()
	a.log.Info("pairing window opened", "expires_at", inv.ExpiresAt)
	return inv
}

// CancelPairing implements api.Backend.
func (a *App) CancelPairing() { a.pairing.Cancel() }

// PairingState implements api.Backend.
func (a *App) PairingState() pairing.State { return a.pairing.State() }

// DecidePairing implements api.Backend.
func (a *App) DecidePairing(id string, approve bool) error {
	if err := a.pairing.Decide(id, approve); err != nil {
		return fmt.Errorf("pairing request %q %w", id, api.ErrNotFound)
	}
	return nil
}

// ConnectPairing implements api.Backend: it pairs this daemon with another
// listening device using that device's invitation URI.
func (a *App) ConnectPairing(ctx context.Context, uri string) (api.ConnectResponse, error) {
	inv, err := protocol.ParsePairingURI(uri)
	if err != nil {
		return api.ConnectResponse{}, fmt.Errorf("%w: %v", api.ErrBadRequest, err)
	}
	if inv.Fingerprint == a.identity.Fingerprint || inv.Device.ID == a.identity.DeviceID {
		return api.ConnectResponse{}, fmt.Errorf("%w: this is the invitation of this device", api.ErrBadRequest)
	}
	sas := protocol.VerificationCode(a.identity.Fingerprint, inv.Fingerprint)
	a.log.Info("requesting pairing", "device", inv.Device.Name, "verification_code", sas)

	ctx, cancel := context.WithTimeout(ctx, a.cfg.Pairing.Window.Duration+30*time.Second)
	defer cancel()
	res, err := pairing.RequestPairing(ctx, a.identity, protocol.PairRequest{
		ProtocolVersion: protocol.Version,
		Device:          a.Self(),
		Code:            inv.Code,
		ListenPort:      a.transport.Port(),
	}, inv)
	if err != nil {
		var remote *pairing.RemoteError
		if errors.As(err, &remote) {
			return api.ConnectResponse{}, fmt.Errorf("%w: %s", api.ErrConflict, remote.Message)
		}
		return api.ConnectResponse{}, err
	}
	out := api.ConnectResponse{Status: res.Response.Status, VerificationCode: sas}
	if res.Response.Status != protocol.PairApproved {
		return out, nil
	}
	addrs := []string{res.Address}
	for _, ad := range inv.Addresses {
		if !slices.Contains(addrs, ad) {
			addrs = append(addrs, ad)
		}
	}
	d := device.Device{
		ID:          res.Response.Device.ID,
		Name:        res.Response.Device.Name,
		Platform:    res.Response.Device.Platform,
		AppVersion:  res.Response.Device.AppVersion,
		Fingerprint: inv.Fingerprint,
		PairedAt:    time.Now().UTC(),
		Addresses:   addrs,
		Permissions: device.Permissions{Send: true, Receive: true},
	}
	if err := a.devices.Add(d); err != nil {
		return api.ConnectResponse{}, err
	}
	v := a.view(d, a.transport.Sessions())
	out.Device = &v
	return out, nil
}

// History implements api.Backend.
func (a *App) History(limit int) []history.Entry { return a.history.List(limit) }

// ClearHistory implements api.Backend.
func (a *App) ClearHistory() error {
	a.log.Info("history cleared")
	return a.history.Clear()
}

// SetPaused implements api.Backend.
func (a *App) SetPaused(p bool) {
	a.engine.SetPaused(p)
	a.log.Info("synchronization state changed", "paused", p)
}

// Send implements api.Backend.
func (a *App) Send(ctx context.Context, text string, devices []string) (synchronization.SendResult, error) {
	return a.engine.SendManual(ctx, text, devices, 3*time.Second)
}

// ConfigTOML implements api.Backend.
func (a *App) ConfigTOML() (string, error) {
	b, err := config.Marshal(a.cfg)
	return string(b), err
}
