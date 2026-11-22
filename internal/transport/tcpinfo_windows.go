package transport

import (
	"encoding/binary"
	"net"
	"unsafe"

	"golang.org/x/sys/windows"
)

// sioTCPInfo is SIO_TCP_INFO (_WSAIORW(IOC_VENDOR, 39), Windows 10 1703+).
const sioTCPInfo = 0xD8000027

// tcpInfoV0Len is sizeof(TCP_INFO_v0); Mss is at offset 4 (ULONG),
// BytesOut at 48 (ULONG64) and BytesRetrans at 68 (ULONG).
const (
	tcpInfoV0Len     = 88
	tcpInfoMss       = 4
	tcpInfoBytesOut  = 48
	tcpInfoBytesRetx = 68
)

// tcpCounters reads BytesRetrans and BytesOut of TCP_INFO_v0 and turns them
// into segments of the connection's MSS (Linux reports segments).
func tcpCounters(c net.Conn) (retrans, sent uint64, ok bool) {
	tc, isTCP := c.(*net.TCPConn)
	if !isTCP {
		return 0, 0, false
	}
	rc, err := tc.SyscallConn()
	if err != nil {
		return 0, 0, false
	}
	var version uint32 // TCP_INFO_v0
	var out [tcpInfoV0Len]byte
	var n uint32
	cerr := rc.Control(func(fd uintptr) {
		err = windows.WSAIoctl(windows.Handle(fd), sioTCPInfo, (*byte)(unsafe.Pointer(&version)), 4,
			&out[0], uint32(len(out)), &n, nil, 0)
	})
	if cerr != nil || err != nil || n < tcpInfoBytesRetx+4 {
		return 0, 0, false
	}
	mss := uint64(max(binary.LittleEndian.Uint32(out[tcpInfoMss:]), 1))
	retx := uint64(binary.LittleEndian.Uint32(out[tcpInfoBytesRetx:]))
	return (retx + mss - 1) / mss, (binary.LittleEndian.Uint64(out[tcpInfoBytesOut:]) + mss - 1) / mss, true
}
