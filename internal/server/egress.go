package server

import (
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// IP protocol numbers used by the egress filter.
const (
	ProtoICMP = 1
	ProtoTCP  = 6
	ProtoUDP  = 17
)

// Destinations hubs never reach through the server (setup-cases.md section 4):
// private and link-local ranges (cloud metadata 169.254.169.254, the Docker
// API on 172.17.0.0/16) unless allowed in the config, and spam, SMB and DHCP
// ports everywhere.
var (
	egressPrivate = []netip.Prefix{
		netip.MustParsePrefix("10.0.0.0/8"),
		netip.MustParsePrefix("172.16.0.0/12"),
		netip.MustParsePrefix("192.168.0.0/16"),
		netip.MustParsePrefix("169.254.0.0/16"),
	}
	egressDockerNet   = netip.MustParsePrefix("172.17.0.0/16")
	egressDockerPorts = []uint16{2375, 2376}
	egressTCPPorts    = []uint16{25, 445}
	egressUDPPorts    = []uint16{67, 68, 445}
)

// Egress is the filter on traffic from the tunnel to the outside. The same
// rules drive the nft forward chain (packet mode) and the PEP origin dial.
type Egress struct {
	Allow []netip.Prefix // private ranges hubs may still reach (config egress_allow)
}

// ParseEgress builds the filter from the config's egress_allow list.
func ParseEgress(allow []string) (Egress, error) {
	var e Egress
	for _, s := range allow {
		p, err := netip.ParsePrefix(s)
		if err != nil || !p.Addr().Is4() {
			return Egress{}, fmt.Errorf("config: egress_allow %q is not an IPv4 prefix", s)
		}
		e.Allow = append(e.Allow, p.Masked())
	}
	return e, nil
}

// Allowed reports whether a hub may send proto traffic to dst.
func (e Egress) Allowed(proto uint8, dst netip.AddrPort) bool {
	a, port := dst.Addr().Unmap(), dst.Port()
	switch proto {
	case ProtoTCP:
		if slices.Contains(egressTCPPorts, port) || (egressDockerNet.Contains(a) && slices.Contains(egressDockerPorts, port)) {
			return false
		}
	case ProtoUDP:
		if slices.Contains(egressUDPPorts, port) {
			return false
		}
	}
	for _, p := range e.Allow {
		if p.Contains(a) {
			return true
		}
	}
	for _, p := range egressPrivate {
		if p.Contains(a) {
			return false
		}
	}
	return true
}

// nftChain returns the filter chain for the crossstream table: packets from
// tun that Allowed would refuse are dropped before they are forwarded.
func (e Egress) nftChain(tun string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "\tchain forward {\n\t\ttype filter hook forward priority filter; policy accept;\n")
	rule := func(format string, args ...any) {
		fmt.Fprintf(&b, "\t\tiifname \"%s\" "+format+"\n", append([]any{tun}, args...)...)
	}
	rule("tcp dport { %s } drop", portList(egressTCPPorts))
	rule("udp dport { %s } drop", portList(egressUDPPorts))
	rule("ip daddr %s tcp dport { %s } drop", egressDockerNet, portList(egressDockerPorts))
	if len(e.Allow) > 0 {
		rule("ip daddr { %s } accept", prefixList(e.Allow))
	}
	rule("ip daddr { %s } drop", prefixList(egressPrivate))
	b.WriteString("\t}\n")
	// Packets for the server host itself never pass the forward hook: hubs
	// get no TCP or UDP service of the host (ping still works), except
	// addresses listed in egress_allow.
	fmt.Fprintf(&b, "\tchain input {\n\t\ttype filter hook input priority filter; policy accept;\n")
	if len(e.Allow) > 0 {
		rule("ip daddr { %s } accept", prefixList(e.Allow))
	}
	rule("meta l4proto { tcp, udp } drop")
	b.WriteString("\t}\n")
	return b.String()
}

func portList(ports []uint16) string {
	s := make([]string, len(ports))
	for i, p := range ports {
		s[i] = fmt.Sprint(p)
	}
	return strings.Join(s, ", ")
}

func prefixList(ps []netip.Prefix) string {
	s := make([]string, len(ps))
	for i, p := range ps {
		s[i] = p.String()
	}
	return strings.Join(s, ", ")
}
