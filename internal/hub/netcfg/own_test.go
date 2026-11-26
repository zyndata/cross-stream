//go:build linux

package netcfg

import (
	"context"
	"net/netip"
	"strings"
	"testing"
)

// ownLAN is a LAN whose "TUN" is lo (it exists, so own traffic can be active).
func ownLAN(run Runner, on, noPath bool) LAN {
	return LAN{
		Ports:   []Port{{Iface: "wlan0", Addr: netip.MustParsePrefix("10.42.0.1/24")}},
		TunName: "lo", MTU: 1360, Run: run, OwnTraffic: on, NoPath: noPath,
		TunAddr: netip.MustParseAddr("100.64.0.2"),
	}
}

func TestOwnTrafficRuleset(t *testing.T) {
	rs := ownLAN(nil, true, false).Ruleset()
	for _, want := range []string{
		`iifname "lo" meta l4proto { tcp, udp } th dport 53 drop`,
		"table ip6 crossstream_hub {",
		"meta mark 0x5853 accept",
		`oifname "lo" accept`,
		"ip6 daddr { fe80::/10, ff00::/8 } accept",
		"reject with icmpv6 type admin-prohibited",
	} {
		if !strings.Contains(rs, want) {
			t.Fatalf("ruleset lacks %q:\n%s", want, rs)
		}
	}
	for _, l := range []LAN{ownLAN(nil, false, false), ownLAN(nil, true, true)} {
		if strings.Contains(l.Ruleset(), "ip6") {
			t.Fatalf("IPv6 reject while own traffic is not active (on %v, no path %v)", l.OwnTraffic, l.NoPath)
		}
	}
}

func TestOwnTrafficEnsure(t *testing.T) {
	if !forwardingOn() {
		t.Skip("ip_forward is off here; Ensure would try to turn it on")
	}
	routes := "10.42.0.0/24 dev wlan0 scope link\nunreachable default metric 9999\ndefault dev lo scope link\n"
	base := "0:\tfrom all lookup local\n100:\tfrom 10.42.0.0/24 iif wlan0 lookup 100\n"
	own := base + "101:\tfrom all lookup main suppress_prefixlength 0\n102:\tnot from all fwmark 0x5853 lookup 100\n"
	// the tunnel's own default (lo here) gets no bound rule
	defs := "default via 192.168.1.1 dev eth0 proto dhcp metric 20100\n" +
		"default via 10.0.0.1 dev wwan0 proto static metric 1050\ndefault dev lo scope link\n"
	bound := func(dev string) string { return "99:\tfrom all oif " + dev + " lookup main\n" }
	tests := []struct {
		name       string
		on, noPath bool
		rec        recorder
		want       []string
	}{
		{"on, nothing yet", true, false, recorder{route: routes, rule: base, dns: "Link 1 (lo):\n"}, []string{
			"ip rule add lookup main suppress_prefixlength 0 priority 101",
			"ip rule add not fwmark 0x5853 lookup 100 priority 102",
			"nft -f -",
			"resolvectl dns lo 100.64.0.2",
			"resolvectl domain lo ~.",
			"resolvectl default-route lo yes",
		}},
		{"on, all present", true, false, recorder{route: routes, rule: own, nft6: true, dns: "Link 1 (lo): 100.64.0.2\n"}, nil},
		{"on, no resolved", true, false, recorder{route: routes, rule: own, nft6: true}, nil},
		{"on, no path (fallback)", true, true, recorder{route: routes, rule: own, nft6: true, dns: "Link 1 (lo): 100.64.0.2\n"}, []string{
			"ip rule del lookup main suppress_prefixlength 0 priority 101",
			"ip rule del not fwmark 0x5853 lookup 100 priority 102",
			"nft delete table ip6 crossstream_hub",
			"resolvectl revert lo",
		}},
		{"off, leftovers", false, false, recorder{route: routes, rule: own, nft6: true, dns: "Link 1 (lo): 100.64.0.2\n"}, []string{
			"ip rule del lookup main suppress_prefixlength 0 priority 101",
			"ip rule del not fwmark 0x5853 lookup 100 priority 102",
			"nft delete table ip6 crossstream_hub",
			"resolvectl revert lo",
		}},
		{"off, clean", false, false, recorder{route: routes, rule: base, dns: "Link 1 (lo):\n"}, nil},
		{"on, links with a default route", true, false, recorder{route: routes, rule: own, nft6: true,
			dns: "Link 1 (lo): 100.64.0.2\n", def: defs}, []string{
			"ip rule add oif eth0 lookup main priority 99",
			"ip rule add oif wwan0 lookup main priority 99",
		}},
		{"on, bound rules kept, gone link dropped", true, false, recorder{route: routes, nft6: true,
			rule: own + bound("eth0") + bound("wwan0") + bound("usb0"), dns: "Link 1 (lo): 100.64.0.2\n", def: defs}, []string{
			"ip rule del oif usb0 lookup main priority 99",
		}},
		{"off, bound leftovers", false, false, recorder{route: routes, rule: base + bound("eth0"),
			dns: "Link 1 (lo):\n", def: defs}, []string{
			"ip rule del oif eth0 lookup main priority 99",
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.rec
			if _, err := ownLAN(rec.run, tc.on, tc.noPath).Ensure(context.Background()); err != nil {
				t.Fatal(err)
			}
			if got := rec.changes(); strings.Join(got, "|") != strings.Join(tc.want, "|") {
				t.Fatalf("ran %q,\nwant %q", got, tc.want)
			}
		})
	}
}

func TestOwnTrafficSetupRemovesLeftovers(t *testing.T) {
	rec := recorder{rule: "99:\tfrom all oif eth0 [detached] lookup main\n"}
	l := ownLAN(rec.run, true, false)
	l.deleteOwn(context.Background())
	want := "ip rule del lookup main suppress_prefixlength 0 priority 101|" +
		"ip rule del not fwmark 0x5853 lookup 100 priority 102|" +
		"ip rule del oif eth0 lookup main priority 99|nft delete table ip6 crossstream_hub"
	if got := strings.Join(rec.changes(), "|"); got != want {
		t.Fatalf("ran %q", got)
	}
}

func TestBoundAndDefaultDevs(t *testing.T) {
	rules := "0:\tfrom all lookup local\n99:\tfrom all oif eth0 lookup main\n" +
		"99:\tfrom all oif usb0 [detached] lookup main\n99:\tfrom all lookup 7\n101:\tfrom all oif x lookup main\n"
	if got := strings.Join(boundDevs(rules), ","); got != "eth0,usb0" {
		t.Errorf("boundDevs = %q", got)
	}
	routes := "default via 192.168.1.1 dev eth0 metric 100\ndefault dev xs0 scope link\n" +
		"default via 192.168.1.1 dev eth0 metric 200\ndefault via 10.0.0.1 dev wwan0\n"
	if got := strings.Join(defaultDevs(routes, "xs0"), ","); got != "eth0,wwan0" {
		t.Errorf("defaultDevs = %q", got)
	}
}

func TestResolvedServers(t *testing.T) {
	for in, want := range map[string]string{
		"Link 7 (xs0): 100.64.0.2\n": "100.64.0.2",
		"Link 7 (xs0):\n":            "",
		"":                           "",
		"Global: 1.1.1.1":            "",
	} {
		if got := resolvedServers(in); got != want {
			t.Errorf("resolvedServers(%q) = %q, want %q", in, got, want)
		}
	}
}
