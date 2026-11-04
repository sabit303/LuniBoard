package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/luniboard/luniboard/daemon/internal/history"
	"github.com/luniboard/luniboard/daemon/internal/pairing"
	"github.com/luniboard/luniboard/daemon/internal/synchronization"
)

// Backend is implemented by the application.
type Backend interface {
	Status() StatusResponse
	Devices() []DeviceView
	UpdateDevice(id string, req UpdateDeviceRequest) (DeviceView, error)
	RemoveDevice(id string) error
	Discovered() []DiscoveredView
	StartPairing() pairing.Invitation
	CancelPairing()
	PairingState() pairing.State
	DecidePairing(requestID string, approve bool) error
	ConnectPairing(ctx context.Context, uri string) (ConnectResponse, error)
	History(limit int) []history.Entry
	ClearHistory() error
	SetPaused(paused bool)
	Send(ctx context.Context, text string, devices []string) (synchronization.SendResult, error)
	ConfigTOML() (string, error)
	RequestShutdown()
}

const maxBodyBytes = 32 << 20

// Server serves the local API on a Unix socket.
type Server struct {
	backend Backend
	log     *slog.Logger
	ln      net.Listener
	srv     *http.Server
	path    string
}

// NewServer creates a server for backend.
func NewServer(b Backend, log *slog.Logger) *Server {
	return &Server{backend: b, log: log}
}

// ErrAlreadyRunning means another daemon owns the socket.
var ErrAlreadyRunning = errors.New("another daemon instance is already running")

// Listen creates the socket with owner-only permissions. A stale socket left
// by a crashed daemon is replaced; a live one yields ErrAlreadyRunning.
func (s *Server) Listen(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	if _, err := os.Stat(path); err == nil {
		if c, err := net.DialTimeout("unix", path, time.Second); err == nil {
			c.Close()
			return fmt.Errorf("%w (socket %s)", ErrAlreadyRunning, path)
		}
		if err := os.Remove(path); err != nil {
			return fmt.Errorf("remove stale socket: %w", err)
		}
	}
	// Restrict permissions from the moment the socket file is created.
	old := umask(0o177)
	ln, err := net.Listen("unix", path)
	umask(old)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", path, err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		ln.Close()
		return err
	}
	s.ln, s.path = peerCheckListener(ln, s.log), path
	s.srv = &http.Server{
		Handler:           s.routes(),
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(s.log.Handler(), slog.LevelDebug),
	}
	return nil
}

// Serve blocks serving requests until Close.
func (s *Server) Serve() {
	if err := s.srv.Serve(s.ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
		s.log.Error("local API stopped", "error", err)
	}
}

// Close shuts the server down and removes the socket.
func (s *Server) Close() error {
	if s.srv == nil {
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	err := s.srv.Shutdown(ctx)
	_ = os.Remove(s.path)
	return err
}

func (s *Server) routes() http.Handler {
	b := s.backend
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, b.Status()) })
	mux.HandleFunc("GET /v1/devices", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, b.Devices()) })
	mux.HandleFunc("PATCH /v1/devices/{id}", func(w http.ResponseWriter, r *http.Request) {
		var req UpdateDeviceRequest
		if !readJSON(w, r, &req) {
			return
		}
		d, err := b.UpdateDevice(r.PathValue("id"), req)
		respond(w, d, err)
	})
	mux.HandleFunc("DELETE /v1/devices/{id}", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]bool{"removed": true}, b.RemoveDevice(r.PathValue("id")))
	})
	mux.HandleFunc("GET /v1/discovered", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, b.Discovered()) })
	mux.HandleFunc("GET /v1/pairing", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, b.PairingState()) })
	mux.HandleFunc("POST /v1/pairing", func(w http.ResponseWriter, r *http.Request) { writeJSON(w, b.StartPairing()) })
	mux.HandleFunc("DELETE /v1/pairing", func(w http.ResponseWriter, r *http.Request) {
		b.CancelPairing()
		writeJSON(w, b.PairingState())
	})
	mux.HandleFunc("POST /v1/pairing/requests/{id}/approve", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]bool{"approved": true}, b.DecidePairing(r.PathValue("id"), true))
	})
	mux.HandleFunc("POST /v1/pairing/requests/{id}/reject", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]bool{"rejected": true}, b.DecidePairing(r.PathValue("id"), false))
	})
	mux.HandleFunc("POST /v1/pairing/connect", func(w http.ResponseWriter, r *http.Request) {
		var req ConnectRequest
		if !readJSON(w, r, &req) {
			return
		}
		res, err := b.ConnectPairing(r.Context(), req.URI)
		respond(w, res, err)
	})
	mux.HandleFunc("GET /v1/history", func(w http.ResponseWriter, r *http.Request) {
		limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
		writeJSON(w, b.History(limit))
	})
	mux.HandleFunc("DELETE /v1/history", func(w http.ResponseWriter, r *http.Request) {
		respond(w, map[string]bool{"cleared": true}, b.ClearHistory())
	})
	mux.HandleFunc("POST /v1/sync/pause", func(w http.ResponseWriter, r *http.Request) {
		b.SetPaused(true)
		writeJSON(w, map[string]bool{"paused": true})
	})
	mux.HandleFunc("POST /v1/sync/resume", func(w http.ResponseWriter, r *http.Request) {
		b.SetPaused(false)
		writeJSON(w, map[string]bool{"paused": false})
	})
	mux.HandleFunc("POST /v1/send", func(w http.ResponseWriter, r *http.Request) {
		var req SendRequest
		if !readJSON(w, r, &req) {
			return
		}
		res, err := b.Send(r.Context(), req.Text, req.Devices)
		respond(w, res, err)
	})
	mux.HandleFunc("GET /v1/config", func(w http.ResponseWriter, r *http.Request) {
		cfg, err := b.ConfigTOML()
		respond(w, map[string]string{"toml": cfg}, err)
	})
	mux.HandleFunc("POST /v1/shutdown", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, map[string]bool{"stopping": true})
		go b.RequestShutdown()
	})
	return mux
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func respond(w http.ResponseWriter, v any, err error) {
	if err == nil {
		writeJSON(w, v)
		return
	}
	status := http.StatusInternalServerError
	switch {
	case errors.Is(err, ErrNotFound):
		status = http.StatusNotFound
	case errors.Is(err, ErrBadRequest), errors.Is(err, synchronization.ErrEmpty),
		errors.Is(err, synchronization.ErrTooLarge), errors.Is(err, synchronization.ErrExcluded):
		status = http.StatusBadRequest
	case errors.Is(err, ErrConflict):
		status = http.StatusConflict
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(ErrorBody{Error: err.Error()})
}

func readJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxBodyBytes)).Decode(v); err != nil {
		respond(w, nil, fmt.Errorf("%w: invalid JSON body: %v", ErrBadRequest, err))
		return false
	}
	return true
}
