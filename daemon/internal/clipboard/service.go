package clipboard

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	adapter "github.com/universal-clipboard/universal-clipboard/daemon/adapters/clipboard"
	"github.com/universal-clipboard/universal-clipboard/daemon/adapters/clipboard/cmdutil"
	"github.com/universal-clipboard/universal-clipboard/daemon/internal/synchronization"
)

// Observer is the part of the synchronization engine the monitor drives.
type Observer interface {
	ObserveLocal(read synchronization.ReadFunc) error
	ObserveInitial(text string)
}

// Mode describes how clipboard changes are detected.
type Mode string

const (
	ModeWatch       Mode = "watch"
	ModePoll        Mode = "poll"
	ModeUnavailable Mode = "unavailable"
	ModeDisabled    Mode = "disabled"
)

// Status is reported through the local API.
type Status struct {
	Backend string `json:"backend"`
	Mode    Mode   `json:"mode"`
	Detail  string `json:"detail,omitempty"`
}

// Options configures a Service.
type Options struct {
	// Backend is auto, wayland, x11 or none.
	Backend      string
	PollInterval time.Duration
	MaxBytes     int64
	Logger       *slog.Logger
	// Detect overrides backend detection (tests).
	Detect func() (Backend, error)
	// RetryInterval controls re-detection after failures.
	RetryInterval time.Duration
	// WatchGrace is how long a watcher must run before an exit is treated as a
	// transient failure rather than "watching is unsupported".
	WatchGrace time.Duration
}

// Service owns the active clipboard backend. It implements
// synchronization.Clipboard for writes and monitors local changes.
type Service struct {
	opts Options
	log  *slog.Logger

	mu      sync.RWMutex
	backend *Backend
	status  Status
}

// NewService creates a clipboard service.
func NewService(opts Options) *Service {
	if opts.RetryInterval == 0 {
		opts.RetryInterval = 10 * time.Second
	}
	if opts.WatchGrace == 0 {
		opts.WatchGrace = 3 * time.Second
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	if opts.Detect == nil {
		opts.Detect = func() (Backend, error) { return Detect(opts.Backend, OSEnv(), opts.MaxBytes) }
	}
	s := &Service{opts: opts, log: opts.Logger}
	if opts.Backend == "none" {
		s.status = Status{Backend: "none", Mode: ModeDisabled, Detail: "clipboard access disabled by configuration"}
	} else {
		s.status = Status{Backend: opts.Backend, Mode: ModeUnavailable, Detail: "not started"}
	}
	return s
}

// Status returns the current backend status.
func (s *Service) Status() Status {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.status
}

// Enabled reports whether clipboard access is configured at all.
func (s *Service) Enabled() bool { return s.opts.Backend != "none" }

func (s *Service) set(b *Backend, st Status) {
	s.mu.Lock()
	s.backend, s.status = b, st
	s.mu.Unlock()
}

// WriteText implements synchronization.Clipboard.
func (s *Service) WriteText(ctx context.Context, text string) error {
	s.mu.RLock()
	b := s.backend
	s.mu.RUnlock()
	if b == nil {
		return adapter.ErrUnavailable
	}
	return b.Adapter.WriteText(ctx, text)
}

// Run detects the backend and monitors it until ctx is done, re-detecting
// after failures (e.g. the compositor restarted).
func (s *Service) Run(ctx context.Context, obs Observer) {
	if !s.Enabled() {
		<-ctx.Done()
		return
	}
	for ctx.Err() == nil {
		b, err := s.opts.Detect()
		if err != nil {
			s.set(nil, Status{Backend: s.opts.Backend, Mode: ModeUnavailable, Detail: err.Error()})
			s.log.Warn("clipboard unavailable; retrying", "error", err, "retry_in", s.opts.RetryInterval)
			sleep(ctx, s.opts.RetryInterval)
			continue
		}
		s.log.Info("clipboard backend selected", "backend", b.Description)
		err = s.monitor(ctx, &b, obs)
		if ctx.Err() != nil {
			return
		}
		s.set(nil, Status{Backend: b.Adapter.Name(), Mode: ModeUnavailable, Detail: err.Error()})
		s.log.Warn("clipboard monitoring stopped; re-detecting", "error", err)
		sleep(ctx, s.opts.RetryInterval)
	}
}

func (s *Service) monitor(ctx context.Context, b *Backend, obs Observer) error {
	read := func(a adapter.Adapter) synchronization.ReadFunc {
		return func() (synchronization.Content, error) {
			c, err := a.ReadText(ctx)
			switch {
			case err == nil:
				return synchronization.Content{Text: c.Text, Sensitive: c.Sensitive}, nil
			case errors.Is(err, adapter.ErrNoText):
				return synchronization.Content{}, synchronization.ErrNoContent
			case errors.Is(err, cmdutil.ErrOutputTooLarge):
				s.log.Debug("clipboard content exceeds max_payload_bytes; ignored")
				return synchronization.Content{}, synchronization.ErrNoContent
			}
			return synchronization.Content{}, err
		}
	}

	if c, err := b.Adapter.ReadText(ctx); err == nil {
		obs.ObserveInitial(c.Text)
	} else if !errors.Is(err, adapter.ErrNoText) && !errors.Is(err, cmdutil.ErrOutputTooLarge) {
		return fmt.Errorf("initial read: %w", err)
	}

	if b.Watcher != nil {
		s.set(b, Status{Backend: b.Adapter.Name(), Mode: ModeWatch, Detail: b.Description})
		started := time.Now()
		var lastErr error
		err := b.Watcher.Watch(ctx, func() {
			if err := obs.ObserveLocal(read(b.Adapter)); err != nil && err.Error() != errString(lastErr) {
				s.log.Debug("read clipboard", "error", err)
				lastErr = err
			}
		})
		if ctx.Err() != nil {
			return nil
		}
		if time.Since(started) > s.opts.WatchGrace {
			return fmt.Errorf("watcher exited: %w", err)
		}
		s.log.Info("clipboard change notifications unsupported by this session; polling instead", "reason", err)
	}

	poller := s.choosePoller(ctx, b)
	s.set(b, Status{Backend: b.Adapter.Name(), Mode: ModePoll,
		Detail: fmt.Sprintf("%s; polling %s every %s", b.Description, poller.Name(), s.opts.PollInterval)})
	t := time.NewTicker(s.opts.PollInterval)
	defer t.Stop()
	failures := 0
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-t.C:
		}
		if err := obs.ObserveLocal(read(poller)); err != nil {
			failures++
			if failures == 1 {
				s.log.Debug("read clipboard", "error", err)
			}
			if failures >= 20 {
				return fmt.Errorf("clipboard reads keep failing: %w", err)
			}
			continue
		}
		failures = 0
	}
}

// choosePoller validates the poll fallback: it is only used when it reports
// the same content as the primary adapter (XWayland does not mirror the
// Wayland clipboard on every compositor).
func (s *Service) choosePoller(ctx context.Context, b *Backend) adapter.Adapter {
	if b.PollFallback == nil || b.PollFallback == b.Adapter {
		return b.Adapter
	}
	primary, err1 := b.Adapter.ReadText(ctx)
	fallback, err2 := b.PollFallback.ReadText(ctx)
	if err1 == nil && err2 == nil && primary.Text == fallback.Text {
		return b.PollFallback
	}
	if errors.Is(err1, adapter.ErrNoText) && errors.Is(err2, adapter.ErrNoText) {
		return b.PollFallback
	}
	s.log.Info("poll fallback does not mirror the primary clipboard; polling primary backend", "fallback", b.PollFallback.Name())
	return b.Adapter
}

func errString(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func sleep(ctx context.Context, d time.Duration) {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-t.C:
	}
}
