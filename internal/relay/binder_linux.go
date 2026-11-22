package relay

import (
	"fmt"

	"golang.org/x/sys/unix"
)

// DeviceBinder binds sockets to network interfaces by name (SO_BINDTODEVICE,
// needs root or CAP_NET_RAW). Used by xs-relaysim.
type DeviceBinder struct {
	WiFi     string
	Cellular string
}

// Bind implements Binder.
func (d DeviceBinder) Bind(n Network, fd uintptr) error {
	iface := d.WiFi
	if n == NetCellular {
		iface = d.Cellular
	}
	if iface == "" {
		return nil
	}
	if err := unix.SetsockoptString(int(fd), unix.SOL_SOCKET, unix.SO_BINDTODEVICE, iface); err != nil {
		return fmt.Errorf("bind %s socket to %s: %w", n, iface, err)
	}
	return nil
}
