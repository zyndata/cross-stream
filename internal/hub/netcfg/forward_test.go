//go:build !windows

package netcfg

import (
	"net"
	"net/netip"
	"strings"
	"testing"
)

func TestForwardChain(t *testing.T) {
	l := testLAN(nil)
	if strings.Contains(l.Ruleset(), "prerouting") {
		t.Fatal("prerouting chain without forwards")
	}
	l.Forwards = []Forward{{Proto: "tcp", Port: 8080, To: netip.MustParseAddr("10.42.0.20")},
		{Proto: "udp", Port: 41820, To: netip.MustParseAddr("10.42.0.21")}}
	rs := l.Ruleset()
	for _, want := range []string{"type nat hook prerouting priority dstnat",
		`iifname "xs0-test-none" tcp dport 8080 dnat to 10.42.0.20:8080`, `iifname "xs0-test-none" udp dport 41820 dnat to 10.42.0.21:41820`} {
		if !strings.Contains(rs, want) {
			t.Errorf("ruleset lacks %q:\n%s", want, rs)
		}
	}
}

func TestResolveTables(t *testing.T) {
	mac, _ := net.ParseMAC("AA:BB:CC:00:11:22")
	leases := "1731500000 aa:bb:cc:00:11:21 10.42.0.150 laptop *\n1731500000 aa:bb:cc:00:11:22 10.42.0.151 nas 01:aa\nbad\n"
	if a, ok := leaseIP(leases, mac); !ok || a.String() != "10.42.0.151" {
		t.Fatalf("lease %v %v", a, ok)
	}
	arp := "IP address       HW type     Flags       HW address            Mask     Device\n" +
		"10.42.0.30       0x1         0x0         aa:bb:cc:00:11:22     *        wlan0\n" +
		"10.42.0.31       0x1         0x2         aa:bb:cc:00:11:22     *        wlan0\n"
	if a, ok := arpIP(arp, mac); !ok || a.String() != "10.42.0.31" {
		t.Fatalf("arp %v %v", a, ok)
	}
	other, _ := net.ParseMAC("00:00:00:00:00:01")
	if _, ok := leaseIP(leases, other); ok {
		t.Fatal("unknown MAC found in leases")
	}
	if _, ok := arpIP(arp, other); ok {
		t.Fatal("unknown MAC found in ARP")
	}
}
