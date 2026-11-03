// Package device maintains the registry of trusted (paired) devices.
package device

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/universal-clipboard/universal-clipboard/daemon/internal/storage"
	"github.com/universal-clipboard/universal-clipboard/daemon/pkg/protocol"
)

// ErrNotFound is returned when a device is not in the registry.
var ErrNotFound = errors.New("device not found")

// maxAddresses bounds the remembered listen addresses per device.
const maxAddresses = 8

// Permissions control synchronization with one device.
type Permissions struct {
	// Send allows this daemon to push clipboard events to the device.
	Send bool `json:"send"`
	// Receive allows clipboard events from the device to be applied locally.
	Receive bool `json:"receive"`
}

// Device is a trusted, paired device.
type Device struct {
	ID          string            `json:"id"`
	Name        string            `json:"name"`
	Platform    protocol.Platform `json:"platform"`
	AppVersion  string            `json:"app_version,omitempty"`
	Fingerprint string            `json:"fingerprint"`
	PairedAt    time.Time         `json:"paired_at"`
	LastSeen    time.Time         `json:"last_seen,omitempty"`
	// Addresses are host:port pairs where the device was last known to listen, most recent first.
	Addresses   []string    `json:"addresses,omitempty"`
	Permissions Permissions `json:"permissions"`
}

// Info returns the protocol-level description of the device.
func (d Device) Info() protocol.DeviceInfo {
	return protocol.DeviceInfo{ID: d.ID, Name: d.Name, Platform: d.Platform, AppVersion: d.AppVersion}
}

// ChangeKind describes a registry mutation.
type ChangeKind int

const (
	Added ChangeKind = iota + 1
	Removed
	Updated
)

// Change is delivered to subscribers after a mutation has been persisted.
type Change struct {
	Kind   ChangeKind
	Device Device
}

type fileFormat struct {
	Version int      `json:"version"`
	Devices []Device `json:"devices"`
}

// Registry is a persistent, concurrency-safe set of trusted devices.
type Registry struct {
	mu          sync.RWMutex
	path        string
	devices     map[string]Device
	subscribers []func(Change)
}

// Open loads the registry from path. An empty path keeps the registry in memory only.
func Open(path string) (*Registry, error) {
	r := &Registry{path: path, devices: make(map[string]Device)}
	if path == "" {
		return r, nil
	}
	var f fileFormat
	if _, err := storage.ReadJSON(path, &f); err != nil {
		return nil, err
	}
	for _, d := range f.Devices {
		if !protocol.ValidID(d.ID) || !protocol.ValidFingerprint(d.Fingerprint) {
			return nil, fmt.Errorf("devices file %s contains an invalid entry", path)
		}
		r.devices[d.ID] = d
	}
	return r, nil
}

// Subscribe registers fn to be called (synchronously, outside the lock) after each change.
func (r *Registry) Subscribe(fn func(Change)) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.subscribers = append(r.subscribers, fn)
}

// List returns all devices sorted by name.
func (r *Registry) List() []Device {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Device, 0, len(r.devices))
	for _, d := range r.devices {
		out = append(out, clone(d))
	}
	slices.SortFunc(out, func(a, b Device) int {
		if c := strings.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)); c != 0 {
			return c
		}
		return strings.Compare(a.ID, b.ID)
	})
	return out
}

// Get returns the device with the given ID.
func (r *Registry) Get(id string) (Device, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.devices[id]
	return clone(d), ok
}

// ByFingerprint returns the device whose certificate has the given fingerprint.
func (r *Registry) ByFingerprint(fp string) (Device, bool) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	for _, d := range r.devices {
		if d.Fingerprint == fp {
			return clone(d), true
		}
	}
	return Device{}, false
}

// Add stores a newly paired device, replacing any entry with the same ID or fingerprint.
func (r *Registry) Add(d Device) error {
	if err := protocol.ValidateDeviceInfo(d.Info()); err != nil {
		return err
	}
	if !protocol.ValidFingerprint(d.Fingerprint) {
		return errors.New("invalid fingerprint")
	}
	r.mu.Lock()
	var replaced []Device
	for id, existing := range r.devices {
		if id == d.ID || existing.Fingerprint == d.Fingerprint {
			replaced = append(replaced, existing)
			delete(r.devices, id)
		}
	}
	d.Addresses = normalizeAddresses(d.Addresses)
	r.devices[d.ID] = clone(d)
	err := r.saveLocked()
	subs := slices.Clone(r.subscribers)
	r.mu.Unlock()
	if err != nil {
		return err
	}
	for _, old := range replaced {
		notify(subs, Change{Kind: Removed, Device: old})
	}
	notify(subs, Change{Kind: Added, Device: clone(d)})
	return nil
}

// Remove revokes trust in a device.
func (r *Registry) Remove(id string) (Device, error) {
	r.mu.Lock()
	d, ok := r.devices[id]
	if !ok {
		r.mu.Unlock()
		return Device{}, ErrNotFound
	}
	delete(r.devices, id)
	err := r.saveLocked()
	subs := slices.Clone(r.subscribers)
	r.mu.Unlock()
	if err != nil {
		return Device{}, err
	}
	notify(subs, Change{Kind: Removed, Device: d})
	return d, nil
}

// SetPermissions updates the synchronization permissions of a device.
func (r *Registry) SetPermissions(id string, p Permissions) (Device, error) {
	return r.update(id, true, func(d *Device) { d.Permissions = p })
}

// RecordSeen updates the last-seen time and, when non-empty, the most recent listen address.
func (r *Registry) RecordSeen(id, listenAddr string, t time.Time) error {
	_, err := r.update(id, false, func(d *Device) {
		d.LastSeen = t.UTC()
		if listenAddr != "" {
			d.Addresses = normalizeAddresses(append([]string{listenAddr}, d.Addresses...))
		}
	})
	return err
}

// SetInfo refreshes a device's display information from a hello message.
func (r *Registry) SetInfo(info protocol.DeviceInfo) error {
	_, err := r.update(info.ID, true, func(d *Device) {
		d.Name, d.Platform, d.AppVersion = info.Name, info.Platform, info.AppVersion
	})
	return err
}

func (r *Registry) update(id string, notifySubs bool, fn func(*Device)) (Device, error) {
	r.mu.Lock()
	d, ok := r.devices[id]
	if !ok {
		r.mu.Unlock()
		return Device{}, ErrNotFound
	}
	before := clone(d)
	fn(&d)
	r.devices[id] = d
	changed := !equal(before, d)
	var err error
	if changed {
		err = r.saveLocked()
	}
	subs := slices.Clone(r.subscribers)
	r.mu.Unlock()
	if err != nil {
		return Device{}, err
	}
	if changed && notifySubs {
		notify(subs, Change{Kind: Updated, Device: clone(d)})
	}
	return clone(d), nil
}

func (r *Registry) saveLocked() error {
	if r.path == "" {
		return nil
	}
	f := fileFormat{Version: 1, Devices: make([]Device, 0, len(r.devices))}
	for _, d := range r.devices {
		f.Devices = append(f.Devices, d)
	}
	slices.SortFunc(f.Devices, func(a, b Device) int { return strings.Compare(a.ID, b.ID) })
	return storage.WriteJSON(r.path, f)
}

func notify(subs []func(Change), c Change) {
	for _, fn := range subs {
		fn(c)
	}
}

func normalizeAddresses(in []string) []string {
	out := make([]string, 0, len(in))
	for _, a := range in {
		if a != "" && !slices.Contains(out, a) {
			out = append(out, a)
		}
	}
	if len(out) > maxAddresses {
		out = out[:maxAddresses]
	}
	return out
}

func clone(d Device) Device {
	d.Addresses = slices.Clone(d.Addresses)
	return d
}

func equal(a, b Device) bool {
	return a.Name == b.Name && a.Platform == b.Platform && a.AppVersion == b.AppVersion &&
		a.LastSeen.Equal(b.LastSeen) && a.Permissions == b.Permissions && slices.Equal(a.Addresses, b.Addresses)
}
