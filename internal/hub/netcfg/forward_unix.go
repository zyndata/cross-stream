//go:build !windows

package netcfg

import (
	"net"
	"net/netip"
	"path/filepath"
)

// ResolveMAC returns the IPv4 address of the client with this MAC: from the
// hotspot DHCP leases, else from the neighbour (ARP) table.
func ResolveMAC(mac net.HardwareAddr) (netip.Addr, bool) {
	files, _ := filepath.Glob(LeaseFiles)
	for _, f := range files {
		if a, ok := leaseIP(readFile(f), mac); ok {
			return a, true
		}
	}
	return arpIP(readFile("/proc/net/arp"), mac)
}
