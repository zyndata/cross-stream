package netcfg

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"syscall"
	"time"

	"golang.org/x/sys/windows"
)

// Socket options of the Windows IPv4 stack (ws2ipdef.h).
const (
	ipUnicastIf    = 31 // IP_UNICAST_IF: send through this interface index
	ipDontFragment = 14 // IP_DONTFRAGMENT
)

// DialVia returns a dial function whose sockets send through iface
// (IP_UNICAST_IF) whatever the routing table says. Used for DNS through the
// tunnel.
func DialVia(iface string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: 5 * time.Second, Control: BindDevice(iface, false)}
	return d.DialContext
}

// BindDevice returns a socket Control function that sends through iface
// (IP_UNICAST_IF; the interface index is looked up per socket, so a
// re-plugged adapter works) and, for UDP when df is set, sets the
// don't-fragment bit (path MTU probes). Used by local uplink paths.
func BindDevice(iface string, df bool) func(network, address string, c syscall.RawConn) error {
	return func(network, _ string, c syscall.RawConn) error {
		ifc, err := net.InterfaceByName(iface)
		if err != nil {
			return fmt.Errorf("bind to %s: %w", iface, err)
		}
		// IPv4 wants the index in network byte order in a DWORD.
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(ifc.Index))
		idx := int(binary.NativeEndian.Uint32(b[:]))
		var serr error
		if err := c.Control(func(fd uintptr) {
			h := windows.Handle(fd)
			serr = windows.SetsockoptInt(h, windows.IPPROTO_IP, ipUnicastIf, idx)
			if serr == nil && df && len(network) >= 3 && network[:3] == "udp" {
				serr = windows.SetsockoptInt(h, windows.IPPROTO_IP, ipDontFragment, 1)
			}
		}); err != nil {
			return err
		}
		if serr != nil {
			return fmt.Errorf("bind to %s: %w", iface, serr)
		}
		return nil
	}
}
