// This file decides which connections count as local, for
// enable-protected-configs and for protected mode.
package server

import (
	"net"

	"github.com/rabbicse/distcache/internal/config"
)

// protectedModeError is Redis's DENIED reply for protected mode, adapted to
// name DistCache.
const protectedModeError = "-DENIED DistCache is running in protected mode because protected mode is enabled and " +
	"no password is set for the default user. In this mode connections are only accepted from the loopback " +
	"interface. If you want to connect from external computers to DistCache you may adopt one of the following " +
	"solutions: 1) Just disable protected mode sending the command 'CONFIG SET protected-mode no' from the " +
	"loopback interface by connecting to DistCache from the same host the server is running, however MAKE SURE " +
	"DistCache is not publicly accessible from internet if you do so. Use CONFIG REWRITE to make this change " +
	"permanent. 2) Alternatively you can just disable the protected mode by editing the DistCache configuration " +
	"file, and setting the protected mode option to 'no', and then restarting the server. 3) If you started the " +
	"server manually just for testing, restart it with the '--protected-mode no' option. 4) Set up an " +
	"authentication password for the default user. NOTE: You only need to do one of the above things in order " +
	"for the server to start accepting connections from the outside.\r\n"

// protectedModeDenies reports whether protected mode refuses a peer: it is on,
// no password is set, and the peer is not local.
func protectedModeDenies(cfg *config.Config, peer net.Addr) bool {
	return cfg.ProtectedMode.Load() && !cfg.RequirePassSet.Load() && !isLocalAddr(peer)
}

// rejectProtected answers and closes a freshly accepted connection that
// protected mode refuses, before any command is read. It reports whether the
// connection was rejected.
func (s *Server) rejectProtected(conn net.Conn) bool {
	if !protectedModeDenies(s.cfg, conn.RemoteAddr()) {
		return false
	}
	s.stats.rejected.Add(1)
	_, _ = conn.Write([]byte(protectedModeError)) // best-effort: the peer is being turned away
	_ = conn.Close()                              // best-effort: cleanup after rejection
	return true
}

// isLocalAddr reports whether a peer address is a loopback TCP address or a
// Unix socket, the connections Redis treats as local.
func isLocalAddr(a net.Addr) bool {
	switch a := a.(type) {
	case *net.UnixAddr:
		return true
	case *net.TCPAddr:
		return a.IP.IsLoopback()
	}
	return false
}

// isLocal reports whether the client connected from this host. A client
// without a connection (an internal caller) is not local.
func (c *Client) isLocal() bool {
	if c == nil || c.conn == nil {
		return false
	}
	return isLocalAddr(c.conn.RemoteAddr())
}
