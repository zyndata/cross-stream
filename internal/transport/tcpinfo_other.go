//go:build !linux && !windows

package transport

import "net"

// tcpCounters is not implemented on this OS.
func tcpCounters(net.Conn) (retrans, sent uint64, ok bool) { return 0, 0, false }
