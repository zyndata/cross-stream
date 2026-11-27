package transport

import (
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// notSentLowat bounds the unsent bytes the kernel keeps for a stream
// connection: the rest waits in our send queue, where a stalled connection's
// packets can still go to another one (TCP_NOTSENT_LOWAT).
const notSentLowat = 128 << 10

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
			_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_TCP, unix.TCP_NOTSENT_LOWAT, notSentLowat)
		})
	}
}
