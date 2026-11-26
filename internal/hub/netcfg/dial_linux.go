package netcfg

import (
	"context"
	"fmt"
	"net"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// DialVia returns a dial function whose sockets are bound to iface
// (SO_BINDTODEVICE, needs root): traffic goes out of iface or fails, it
// never falls back to the default route. Used for DNS through the tunnel.
func DialVia(iface string) func(ctx context.Context, network, addr string) (net.Conn, error) {
	d := net.Dialer{Timeout: 5 * time.Second, Control: BindDevice(iface, false)}
	return d.DialContext
}

// BindDevice returns a socket Control function that binds sockets to iface
// (SO_BINDTODEVICE), gives them the hub mark and, for UDP when df is set,
// forbids fragmentation (path MTU probes). Used by local uplink paths.
func BindDevice(iface string, df bool) ControlFunc {
	return func(network, _ string, c syscall.RawConn) error {
		var serr error
		if err := c.Control(func(fd uintptr) {
			serr = unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface)
			if serr == nil {
				serr = setMark(int(fd))
			}
			if serr == nil && df && len(network) >= 3 && network[:3] == "udp" {
				serr = unix.SetsockoptInt(int(fd), unix.IPPROTO_IP, unix.IP_MTU_DISCOVER, unix.IP_PMTUDISC_PROBE)
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
