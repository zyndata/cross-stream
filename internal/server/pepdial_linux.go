package server

import "syscall"

// tcpBBR selects BBR congestion control for an origin socket. Best effort:
// without the tcp_bbr module the kernel default stays.
func tcpBBR(_, _ string, c syscall.RawConn) error {
	return c.Control(func(fd uintptr) {
		_ = syscall.SetsockoptString(int(fd), syscall.IPPROTO_TCP, syscall.TCP_CONGESTION, "bbr")
	})
}
