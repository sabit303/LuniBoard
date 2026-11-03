//go:build !linux

package api

import (
	"log/slog"
	"net"
)

// On non-Linux platforms access control relies on the socket's directory and
// file permissions (owner-only).
func peerCheckListener(ln net.Listener, _ *slog.Logger) net.Listener { return ln }
