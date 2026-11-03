//go:build linux

package api

import (
	"log/slog"
	"net"
	"os"
	"syscall"

	"golang.org/x/sys/unix"
)

func umask(mask int) int { return syscall.Umask(mask) }

// peerCheckListener rejects connections from other users even if the socket
// permissions were loosened, by checking SO_PEERCRED.
func peerCheckListener(ln net.Listener, log *slog.Logger) net.Listener {
	return &credListener{Listener: ln, log: log, uid: os.Getuid()}
}

type credListener struct {
	net.Listener
	log *slog.Logger
	uid int
}

func (l *credListener) Accept() (net.Conn, error) {
	for {
		c, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if l.allowed(c) {
			return c, nil
		}
		l.log.Warn("rejected local API connection from another user")
		c.Close()
	}
}

func (l *credListener) allowed(c net.Conn) bool {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return false
	}
	raw, err := uc.SyscallConn()
	if err != nil {
		return false
	}
	var cred *unix.Ucred
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, credErr = unix.GetsockoptUcred(int(fd), unix.SOL_SOCKET, unix.SO_PEERCRED)
	}); err != nil || credErr != nil {
		return false
	}
	return int(cred.Uid) == l.uid
}
