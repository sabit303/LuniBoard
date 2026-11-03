// Package history keeps a bounded, optionally persisted log of clipboard events.
package history

import (
	"log/slog"
	"slices"
	"sync"
	"time"

	"github.com/universal-clipboard/universal-clipboard/daemon/internal/storage"
	"github.com/universal-clipboard/universal-clipboard/daemon/pkg/protocol"
)

// Direction tells whether an entry was produced locally or received from a peer.
type Direction string

const (
	Outgoing Direction = "outgoing"
	Incoming Direction = "incoming"
)

// Source describes what produced an outgoing entry.
type Source string

const (
	SourceClipboard Source = "clipboard" // local clipboard monitor
	SourceManual    Source = "manual"    // explicit send via API/CLI
	SourcePeer      Source = "peer"      // received from a peer
)

// DeliveryStatus is "sent", "failed" or an ack status reported by the peer.
type DeliveryStatus string

const (
	DeliverySent   DeliveryStatus = "sent"
	DeliveryFailed DeliveryStatus = "failed"
)

// Entry is one clipboard event as seen by this device.
type Entry struct {
	EventID        string                    `json:"event_id"`
	OriginDeviceID string                    `json:"origin_device_id"`
	FromDeviceID   string                    `json:"from_device_id,omitempty"`
	Direction      Direction                 `json:"direction"`
	Source         Source                    `json:"source"`
	ContentType    protocol.ContentType      `json:"content_type"`
	Text           string                    `json:"text"`
	Size           int64                     `json:"size"`
	Timestamp      time.Time                 `json:"timestamp"`
	RecordedAt     time.Time                 `json:"recorded_at"`
	Deliveries     map[string]DeliveryStatus `json:"deliveries,omitempty"`
}

// Options configures a Store.
type Options struct {
	Enabled    bool
	Persist    bool
	MaxEntries int
	Path       string
	Logger     *slog.Logger
}

// Store is a concurrency-safe bounded history, newest entry first.
type Store struct {
	mu      sync.Mutex
	opts    Options
	entries []Entry
}

type fileFormat struct {
	Version int     `json:"version"`
	Entries []Entry `json:"entries"`
}

// Open creates a store, loading persisted entries when persistence is enabled.
// When persistence is disabled any previously persisted file is removed so
// that turning the option off actually erases clipboard data from disk.
func Open(opts Options) (*Store, error) {
	s := &Store{opts: opts}
	if opts.Path == "" {
		return s, nil
	}
	if !opts.Persist || !opts.Enabled {
		if err := removeIfExists(opts.Path); err != nil {
			return nil, err
		}
		return s, nil
	}
	var f fileFormat
	if _, err := storage.ReadJSON(opts.Path, &f); err != nil {
		return nil, err
	}
	s.entries = f.Entries
	s.trimLocked()
	return s, nil
}

// Add records an entry.
func (s *Store) Add(e Entry) {
	if !s.opts.Enabled || s.opts.MaxEntries == 0 {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	e.Deliveries = cloneMap(e.Deliveries)
	s.entries = append([]Entry{e}, s.entries...)
	s.trimLocked()
	s.saveLocked()
}

// UpdateDelivery records the delivery status of an event for one device.
func (s *Store) UpdateDelivery(eventID, deviceID string, status DeliveryStatus) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.entries {
		if s.entries[i].EventID == eventID {
			if s.entries[i].Deliveries == nil {
				s.entries[i].Deliveries = make(map[string]DeliveryStatus)
			}
			s.entries[i].Deliveries[deviceID] = status
			s.saveLocked()
			return
		}
	}
}

// List returns up to limit entries (all when limit <= 0), newest first.
func (s *Store) List(limit int) []Entry {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := len(s.entries)
	if limit > 0 && limit < n {
		n = limit
	}
	out := make([]Entry, n)
	for i := 0; i < n; i++ {
		out[i] = s.entries[i]
		out[i].Deliveries = cloneMap(s.entries[i].Deliveries)
	}
	return out
}

// Clear deletes all entries from memory and disk.
func (s *Store) Clear() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.entries = nil
	if s.opts.Path != "" {
		return removeIfExists(s.opts.Path)
	}
	return nil
}

func (s *Store) trimLocked() {
	if len(s.entries) > s.opts.MaxEntries {
		s.entries = slices.Clip(s.entries[:s.opts.MaxEntries])
	}
}

func (s *Store) saveLocked() {
	if !s.opts.Persist || s.opts.Path == "" {
		return
	}
	if err := storage.WriteJSON(s.opts.Path, fileFormat{Version: 1, Entries: s.entries}); err != nil && s.opts.Logger != nil {
		s.opts.Logger.Warn("persist history", "error", err)
	}
}

func cloneMap(m map[string]DeliveryStatus) map[string]DeliveryStatus {
	if m == nil {
		return nil
	}
	out := make(map[string]DeliveryStatus, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}
