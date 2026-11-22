package netcfg

import (
	"fmt"
	"net"
	"net/netip"

	"github.com/zyndata/cross-stream/internal/iphlp"
)

// native adds ("replace") or removes ("del") the route with the IP Helper
// API (active store only, so a reboot drops a leftover).
func (r HostRoute) native(op string) error {
	if !r.Dst.Is4() || r.Iface == "" {
		return fmt.Errorf("netcfg: host route %v dev %q: want an IPv4 address and an interface", r.Dst, r.Iface)
	}
	ifc, err := net.InterfaceByName(r.Iface)
	if err != nil {
		return fmt.Errorf("netcfg: host route: %w", err)
	}
	id := iphlp.Iface{Index: uint32(ifc.Index)}
	rt := iphlp.Route{Dst: netip.PrefixFrom(r.Dst, 32), NextHop: r.GW}
	if op == "del" {
		err = iphlp.DeleteRoute(id, rt)
	} else {
		err = iphlp.AddRoute(id, rt)
	}
	if err != nil {
		return fmt.Errorf("netcfg: host route: %w", err)
	}
	return nil
}
