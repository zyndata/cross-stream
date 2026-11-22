package relay

import "syscall"

// ipPMTUDiscProbe is IP_PMTUDISC_PROBE (linux/in.h), missing from syscall.
const ipPMTUDiscProbe = 3

// dontFragment sets DF on a cellular socket and ignores the cached path MTU,
// so the bond path MTU probes (padded PINGs) measure the real path. Best
// effort: an error leaves the OS default.
func dontFragment(fd uintptr) {
	_ = syscall.SetsockoptInt(int(fd), syscall.IPPROTO_IP, syscall.IP_MTU_DISCOVER, ipPMTUDiscProbe)
}
