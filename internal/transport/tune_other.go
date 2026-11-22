//go:build !linux

package transport

import "net"

// tuneTCP sets TCP_NODELAY (BBR is Linux only).
func tuneTCP(c net.Conn) {
	if tc, ok := c.(*net.TCPConn); ok {
		_ = tc.SetNoDelay(true)
	}
}
