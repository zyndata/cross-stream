package netcfg

import (
	"bufio"
	"net"
	"net/netip"
	"os/exec"
	"strings"
)

// ResolveMAC returns the IPv4 address of the client with this MAC from the
// neighbour table (arp -a; ICS keeps its DHCP leases to itself).
func ResolveMAC(mac net.HardwareAddr) (netip.Addr, bool) {
	out, err := exec.Command("arp", "-a").Output()
	if err != nil {
		return netip.Addr{}, false
	}
	return arpWinIP(string(out), mac)
}

// arpWinIP finds mac in arp -a output ("  10.42.0.23   aa-bb-cc-dd-ee-ff   dynamic").
func arpWinIP(table string, mac net.HardwareAddr) (netip.Addr, bool) {
	sc := bufio.NewScanner(strings.NewReader(table))
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 3 {
			continue
		}
		m, err := net.ParseMAC(f[1])
		if err != nil || m.String() != mac.String() {
			continue
		}
		if a, err := netip.ParseAddr(f[0]); err == nil && a.Is4() {
			return a, true
		}
	}
	return netip.Addr{}, false
}
