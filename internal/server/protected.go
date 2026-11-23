// This file decides which connections count as local, for
// enable-protected-configs.
package server

import "net"

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
