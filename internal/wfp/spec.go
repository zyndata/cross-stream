// Package wfp keeps the Windows hub's DNS and IPv6 leak filters (phase 21
// W2): Windows Filtering Platform filters in one dynamic session, so they
// vanish when xs-hubd exits or crashes. Wanted is the pure filter set (all
// OSes, tested everywhere); Session (Windows) installs it.
package wfp

import "net/netip"

// Layer is the WFP layer a filter sits in.
type Layer uint8

// Layers used: outbound connects and first UDP sends.
const (
	ConnectV4 Layer = iota // FWPM_LAYER_ALE_AUTH_CONNECT_V4
	ConnectV6              // FWPM_LAYER_ALE_AUTH_CONNECT_V6
)

func (l Layer) String() string {
	if l == ConnectV6 {
		return "connect-v6"
	}
	return "connect-v4"
}

// Filter is one wanted filter. Exactly one condition field is set; none =
// match everything. Permits outweigh blocks inside the sublayer.
type Filter struct {
	Name   string
	Layer  Layer
	Permit bool
	Weight uint8 // 0-15 inside the sublayer

	App        string       // ALE app ID of this executable path
	Loopback   bool         // FWP_CONDITION_FLAG_IS_LOOPBACK
	LocalLUID  uint64       // local interface (the tunnel)
	RemotePort uint16       // remote port
	RemoteNet  netip.Prefix // remote IPv6 network
}

// Filter weights.
const (
	permitWeight = 12
	blockWeight  = 4
)

// Linklocal and multicast IPv6 stay allowed: neighbour discovery, DHCPv6,
// mDNS — nothing that leaves the local link.
var (
	linkLocal6 = netip.MustParsePrefix("fe80::/10")
	multicast6 = netip.MustParsePrefix("ff00::/8")
)

// Wanted returns the filters for the hub state: none while no path carries
// traffic (active false: the split routes are gone too, the PC uses its own
// links); otherwise permit xs-hubd (app), loopback and the tunnel (tunLUID),
// block DNS (remote port 53) everywhere else and every other IPv6 connect,
// so apps fall back to IPv4 at once (WSAEACCES) instead of leaking.
func Wanted(active bool, tunLUID uint64, app string) []Filter {
	if !active || tunLUID == 0 {
		return nil
	}
	var fs []Filter
	for _, l := range []Layer{ConnectV4, ConnectV6} {
		if app != "" {
			fs = append(fs, Filter{Name: "permit xs-hubd", Layer: l, Permit: true, Weight: permitWeight, App: app})
		}
		fs = append(fs,
			Filter{Name: "permit loopback", Layer: l, Permit: true, Weight: permitWeight, Loopback: true},
			Filter{Name: "permit tunnel", Layer: l, Permit: true, Weight: permitWeight, LocalLUID: tunLUID})
	}
	fs = append(fs,
		Filter{Name: "block DNS outside the tunnel", Layer: ConnectV4, Weight: blockWeight, RemotePort: 53},
		Filter{Name: "permit link-local IPv6", Layer: ConnectV6, Permit: true, Weight: permitWeight, RemoteNet: linkLocal6},
		Filter{Name: "permit multicast IPv6", Layer: ConnectV6, Permit: true, Weight: permitWeight, RemoteNet: multicast6},
		Filter{Name: "block IPv6 outside the tunnel", Layer: ConnectV6, Weight: blockWeight})
	return fs
}
