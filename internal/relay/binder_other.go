//go:build !linux

package relay

import "fmt"

// DeviceBinder binds sockets to network interfaces by name; Linux only.
type DeviceBinder struct {
	WiFi     string
	Cellular string
}

// Bind implements Binder.
func (d DeviceBinder) Bind(n Network, _ uintptr) error {
	if (n == NetWiFi && d.WiFi == "") || (n == NetCellular && d.Cellular == "") {
		return nil
	}
	return fmt.Errorf("bind %s socket to an interface: %w", n, ErrNotSupported)
}
