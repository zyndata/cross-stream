package server

import "syscall"

// dontFragment sets DF on the server's UDP sockets and ignores the cached
// path MTU (IP_PMTUDISC_PROBE), so bond path MTU probes measure the real
// path. Best effort: errors are ignored.
func dontFragment(_, _ string, c syscall.RawConn) error {
	return c.Control(func(fd uintptr) {
		_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MTU_DISCOVER, ipPMTUDiscProbe)
		_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IPV6, syscall.IPV6_MTU_DISCOVER, ipPMTUDiscProbe)
	})
}

// ipPMTUDiscProbe is IP_PMTUDISC_PROBE (linux/in.h), missing from syscall.
const ipPMTUDiscProbe = 3
