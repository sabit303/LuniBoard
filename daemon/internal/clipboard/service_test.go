package clipboard

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	adapter "github.com/luniboard/luniboard/daemon/adapters/clipboard"
	"github.com/luniboard/luniboard/daemon/adapters/clipboard/mock"
	"github.com/luniboard/luniboard/daemon/internal/logging"
	"github.com/luniboard/luniboard/daemon/internal/synchronization"
)

func fakeEnv(vars map[string]string, tools []string, sockets []string) Env {
	return Env{
		Getenv: func(k string) string { return vars[k] },
		LookPath: func(t string) (string, error) {
			for _, x := range tools {
				if x == t {
					return "/usr/bin/" + t, nil
				}
			}
			return "", errors.New("not found")
		},
		Glob: func(string) ([]string, error) { return sockets, nil },
		GOOS: "linux",
	}
}

func TestDetect(t *testing.T) {
	all := []string{"wl-copy", "wl-paste", "xclip", "xsel"}
	cases := []struct {
		name    string
		backend string
		vars    map[string]string
		tools   []string
		sockets []string
		want    string // substring of description, "" = error
	}{
		{"wayland session", "auto", map[string]string{"XDG_SESSION_TYPE": "wayland", "WAYLAND_DISPLAY": "wayland-1"}, []string{"wl-copy", "wl-paste"}, nil, "wayland (wl-clipboard, display wayland-1)"},
		{"wayland with xwayland fallback", "auto", map[string]string{"WAYLAND_DISPLAY": "wayland-0", "DISPLAY": ":0"}, all, nil, "polling fallback via XWayland"},
		{"x11 session", "auto", map[string]string{"XDG_SESSION_TYPE": "x11", "DISPLAY": ":0"}, all, nil, "x11 (xclip"},
		{"x11 with xsel only", "auto", map[string]string{"DISPLAY": ":0"}, []string{"xsel"}, nil, "x11 (xsel"},
		{"systemd without env finds socket", "auto", map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, all, []string{"/run/user/1000/wayland-1", "/run/user/1000/wayland-1.lock"}, "display wayland-1"},
		{"ambiguous sockets", "auto", map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"}, all, []string{"/run/user/1000/wayland-0", "/run/user/1000/wayland-1"}, ""},
		{"wayland tools missing", "auto", map[string]string{"WAYLAND_DISPLAY": "wayland-1"}, []string{"xclip"}, nil, ""},
		{"forced x11 in wayland", "x11", map[string]string{"WAYLAND_DISPLAY": "wayland-1", "DISPLAY": ":0"}, all, nil, "x11 (xclip"},
		{"forced wayland unavailable", "wayland", map[string]string{"DISPLAY": ":0"}, all, nil, ""},
		{"headless", "auto", map[string]string{}, all, nil, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			b, err := Detect(tc.backend, fakeEnv(tc.vars, tc.tools, tc.sockets), 1024)
			if tc.want == "" {
				if err == nil {
					t.Fatalf("expected error, got %q", b.Description)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(b.Description, tc.want) {
				t.Fatalf("description %q does not contain %q", b.Description, tc.want)
			}
		})
	}
	env := fakeEnv(map[string]string{"WAYLAND_DISPLAY": "w"}, all, nil)
	env.GOOS = "windows"
	if _, err := Detect("auto", env, 1024); !errors.Is(err, ErrNoBackend) {
		t.Fatalf("expected ErrNoBackend on windows, got %v", err)
	}
}

type recordingObserver struct {
	mu      sync.Mutex
	initial string
	seen    []synchronization.Content
}

func (o *recordingObserver) ObserveInitial(text string) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.initial = text
}

func (o *recordingObserver) ObserveLocal(read synchronization.ReadFunc) error {
	c, err := read()
	if err != nil {
		if errors.Is(err, synchronization.ErrNoContent) {
			return nil
		}
		return err
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if n := len(o.seen); n == 0 || o.seen[n-1] != c {
		o.seen = append(o.seen, c)
	}
	return nil
}

func (o *recordingObserver) snapshot() (string, []synchronization.Content) {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.initial, append([]synchronization.Content(nil), o.seen...)
}

func waitUntil(t *testing.T, cond func() bool) {
	t.Helper()
	for i := 0; i < 300; i++ {
		if cond() {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("condition not met")
}

// pollOnly hides the mock's Watch method.
type pollOnly struct{ *mock.Clipboard }

func TestServiceWatchAndPoll(t *testing.T) {
	for _, watch := range []bool{true, false} {
		m := mock.New()
		m.Copy(adapter.Content{Text: "startup"})
		b := Backend{Adapter: m, PollFallback: m, Description: "mock"}
		if watch {
			b.Watcher = m
		} else {
			b.Adapter, b.PollFallback = pollOnly{m}, pollOnly{m}
		}
		svc := NewService(Options{Backend: "auto", PollInterval: 20 * time.Millisecond, Logger: logging.Discard(),
			Detect: func() (Backend, error) { return b, nil }})
		obs := &recordingObserver{}
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { svc.Run(ctx, obs); close(done) }()

		waitUntil(t, func() bool { i, _ := obs.snapshot(); return i == "startup" })
		m.Copy(adapter.Content{Text: "new", Sensitive: true})
		waitUntil(t, func() bool {
			_, seen := obs.snapshot()
			return len(seen) > 0 && seen[len(seen)-1] == synchronization.Content{Text: "new", Sensitive: true}
		})
		wantMode := ModePoll
		if watch {
			wantMode = ModeWatch
		}
		if st := svc.Status(); st.Mode != wantMode {
			t.Fatalf("watch=%v: mode %s", watch, st.Mode)
		}
		if err := svc.WriteText(ctx, "written"); err != nil || m.Text() != "written" {
			t.Fatalf("write: %v", err)
		}
		cancel()
		<-done
	}
}

func TestServiceRetriesDetection(t *testing.T) {
	m := mock.New()
	var mu sync.Mutex
	attempts := 0
	svc := NewService(Options{Backend: "auto", PollInterval: 20 * time.Millisecond, RetryInterval: 10 * time.Millisecond,
		Logger: logging.Discard(),
		Detect: func() (Backend, error) {
			mu.Lock()
			defer mu.Unlock()
			attempts++
			if attempts < 3 {
				return Backend{}, ErrNoBackend
			}
			return Backend{Adapter: m, Watcher: m, PollFallback: m, Description: "mock"}, nil
		}})
	if err := svc.WriteText(context.Background(), "x"); !errors.Is(err, adapter.ErrUnavailable) {
		t.Fatalf("expected ErrUnavailable before detection, got %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go svc.Run(ctx, &recordingObserver{})
	waitUntil(t, func() bool { return svc.Status().Mode == ModeWatch })
}

func TestServiceDisabled(t *testing.T) {
	svc := NewService(Options{Backend: "none", Logger: logging.Discard()})
	if svc.Enabled() || svc.Status().Mode != ModeDisabled {
		t.Fatal("none backend should be disabled")
	}
}
