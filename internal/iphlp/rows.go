// Package iphlp wraps the Windows IP Helper API (iphlpapi.dll) calls the hub
// needs instead of netsh: interface addresses, routes and the IPv4 interface
// settings (MTU, metric, forwarding). The row filling is portable so it is
// tested on every OS; only the calls themselves are Windows-only.
package iphlp

import (
	"encoding/binary"
	"fmt"
	"net/netip"
)

// afInet is AF_INET on Windows (ws2def.h).
const afInet = 2

// sockaddrInet is the memory image of a SOCKADDR_INET union (28 bytes)
// holding an IPv4 SOCKADDR_IN: family (little-endian), port 0, address.
type sockaddrInet [28]byte

// inet4 returns the SOCKADDR_INET image of an IPv4 address.
func inet4(a netip.Addr) (sockaddrInet, error) {
	var s sockaddrInet
	a = a.Unmap()
	if !a.Is4() {
		return s, fmt.Errorf("iphlp: %v is not an IPv4 address", a)
	}
	binary.LittleEndian.PutUint16(s[0:2], afInet)
	b := a.As4()
	copy(s[4:8], b[:])
	return s, nil
}

// addr4 reads an IPv4 address back from a SOCKADDR_INET image.
func addr4(s sockaddrInet) (netip.Addr, bool) {
	if binary.LittleEndian.Uint16(s[0:2]) != afInet {
		return netip.Addr{}, false
	}
	return netip.AddrFrom4([4]byte(s[4:8])), true
}

// Route is an IPv4 route through one interface. An invalid or unspecified
// NextHop means on-link (point-to-point or a TUN).
type Route struct {
	Dst     netip.Prefix
	NextHop netip.Addr
	Metric  uint32
}

// routeFields are the MIB_IPFORWARD_ROW2 fields a route sets.
type routeFields struct {
	dst     sockaddrInet
	dstLen  uint8
	nextHop sockaddrInet
	metric  uint32
}

func (r Route) fields() (routeFields, error) {
	var f routeFields
	if !r.Dst.IsValid() || !r.Dst.Addr().Unmap().Is4() {
		return f, fmt.Errorf("iphlp: route %v: want an IPv4 prefix", r.Dst)
	}
	p := netip.PrefixFrom(r.Dst.Addr().Unmap(), r.Dst.Bits()).Masked()
	dst, err := inet4(p.Addr())
	if err != nil {
		return f, err
	}
	nh := r.NextHop
	if !nh.IsValid() {
		nh = netip.IPv4Unspecified()
	}
	hop, err := inet4(nh)
	if err != nil {
		return f, fmt.Errorf("iphlp: route %v next hop: %w", p, err)
	}
	return routeFields{dst: dst, dstLen: uint8(p.Bits()), nextHop: hop, metric: r.Metric}, nil
}

// addrFields are the MIB_UNICASTIPADDRESS_ROW fields an address sets.
type addrFields struct {
	addr   sockaddrInet
	onLink uint8 // prefix length (the netmask)
}

func addressFields(p netip.Prefix) (addrFields, error) {
	if !p.IsValid() {
		return addrFields{}, fmt.Errorf("iphlp: invalid address prefix")
	}
	a, err := inet4(p.Addr())
	if err != nil {
		return addrFields{}, err
	}
	return addrFields{addr: a, onLink: uint8(p.Bits())}, nil
}

// Settings are IPv4 interface settings; a zero field is left as it is.
type Settings struct {
	MTU        uint32 // NlMtu
	Metric     uint32 // interface metric; also turns the automatic metric off
	Forwarding bool   // turn IPv4 forwarding on (never turned off here)
}

// ifaceFields mirrors the MIB_IPINTERFACE_ROW fields Settings touches.
type ifaceFields struct {
	nlMtu              uint32
	useAutomaticMetric uint8
	metric             uint32
	forwardingEnabled  uint8
}

// apply changes f to match s and reports whether anything changed.
func (s Settings) apply(f *ifaceFields) bool {
	changed := false
	if s.MTU != 0 && f.nlMtu != s.MTU {
		f.nlMtu, changed = s.MTU, true
	}
	if s.Metric != 0 && (f.metric != s.Metric || f.useAutomaticMetric != 0) {
		f.metric, f.useAutomaticMetric, changed = s.Metric, 0, true
	}
	if s.Forwarding && f.forwardingEnabled == 0 {
		f.forwardingEnabled, changed = 1, true
	}
	return changed
}

// nameServers is the NameServer string of DNS_INTERFACE_SETTINGS: IPv4
// addresses separated by commas; an empty list clears the setting.
func nameServers(servers []netip.Addr) (string, error) {
	out := ""
	for i, a := range servers {
		a = a.Unmap()
		if !a.Is4() {
			return "", fmt.Errorf("iphlp: DNS server %v: want an IPv4 address", a)
		}
		if i > 0 {
			out += ","
		}
		out += a.String()
	}
	return out, nil
}
