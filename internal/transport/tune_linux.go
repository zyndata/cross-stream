package transport

import (
	"net"
	"syscall"
)

// tuneTCP sets TCP_NODELAY and, when the kernel has it, BBR congestion
// control (TCP-over-TCP suffers less with a non-loss-based outer CC).
// Best effort: errors leave the defaults.
func tuneTCP(c net.Conn) {
	tc, ok := c.(*net.TCPConn)
	if !ok {
		return
	}
	_ = tc.SetNoDelay(true)
	if rc, err := tc.SyscallConn(); err == nil {
		_ = rc.Control(func(fd uintptr) {
			_ = syscall.SetsockoptString(int(fd), syscall.IPPROTO_TCP, syscall.TCP_CONGESTION, "bbr")
		})
	}
}
