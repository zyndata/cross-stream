//go:build !windows

package netcfg

import "github.com/zyndata/cross-stream/internal/hub/uplink"

// RPFilter is true where local uplinks need a loose reverse path filter.
const RPFilter = true

// ScanUplinks lists the interfaces that qualify as local uplinks (sysfs,
// /proc/net/route).
func ScanUplinks(exclude []string) ([]uplink.Iface, error) { return uplink.Scan(exclude) }
