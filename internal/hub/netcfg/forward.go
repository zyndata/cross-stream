package netcfg

import (
	"bufio"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strings"
)

// Forward is one port forward on the hub: packets arriving on the TUN for
// Port are DNATed to the client To (same port).
type Forward struct {
	Proto string // tcp or udp
	Port  uint16
	To    netip.Addr
}

// forwardChain returns the nft prerouting chain for the forwards ("" when
// there are none).
func (l LAN) forwardChain() string {
	if len(l.Forwards) == 0 {
		return ""
	}
	var b strings.Builder
	b.WriteString("\tchain prerouting {\n\t\ttype nat hook prerouting priority dstnat; policy accept;\n")
	for _, f := range l.Forwards {
		fmt.Fprintf(&b, "\t\tiifname %q %s dport %d dnat to %s:%d\n", l.TunName, f.Proto, f.Port, f.To, f.Port)
	}
	b.WriteString("\t}\n")
	return b.String()
}

// LeaseFiles are where NetworkManager's shared-mode dnsmasq keeps leases.
const LeaseFiles = "/var/lib/NetworkManager/dnsmasq-*.leases"

func readFile(p string) string {
	b, err := os.ReadFile(p)
	if err != nil {
		return ""
	}
	return string(b)
}

// leaseIP finds mac in dnsmasq lease lines: "expiry mac ip hostname clientid".
func leaseIP(leases string, mac net.HardwareAddr) (netip.Addr, bool) {
	sc := bufio.NewScanner(strings.NewReader(leases))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 {
			continue
		}
		if m, err := net.ParseMAC(f[1]); err == nil && m.String() == mac.String() {
			if a, err := netip.ParseAddr(f[2]); err == nil && a.Is4() {
				return a, true
			}
		}
	}
	return netip.Addr{}, false
}

// arpIP finds mac in /proc/net/arp ("IP type flags HW mask device"; flags
// 0x0 = incomplete).
func arpIP(table string, mac net.HardwareAddr) (netip.Addr, bool) {
	sc := bufio.NewScanner(strings.NewReader(table))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 4 || f[2] == "0x0" {
			continue
		}
		if m, err := net.ParseMAC(f[3]); err == nil && m.String() == mac.String() {
			if a, err := netip.ParseAddr(f[0]); err == nil && a.Is4() {
				return a, true
			}
		}
	}
	return netip.Addr{}, false
}
