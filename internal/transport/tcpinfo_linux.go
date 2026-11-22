package transport

import (
	"net"

	"golang.org/x/sys/unix"
)

// tcpCounters reads tcpi_total_retrans and tcpi_data_segs_out (Linux 4.6+,
// every Android kernel the relay app supports).
func tcpCounters(c net.Conn) (retrans, sent uint64, ok bool) {
	tc, isTCP := c.(*net.TCPConn)
	if !isTCP {
		return 0, 0, false
	}
	rc, err := tc.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	var info *unix.TCPInfo
	cerr := rc.Control(func(fd uintptr) {
		info, err = unix.GetsockoptTCPInfo(int(fd), unix.IPPROTO_TCP, unix.TCP_INFO)
	})
	if cerr != nil || err != nil || info == nil {
		return 0, 0, false
	}
	return uint64(info.Total_retrans), uint64(info.Data_segs_out), true
}
