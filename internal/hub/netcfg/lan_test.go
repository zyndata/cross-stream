//go:build !windows

package netcfg

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func testLAN(run Runner) LAN {
	return LAN{
		Ports: []Port{
			{Iface: "wlan0", Addr: netip.MustParsePrefix("10.42.0.1/24")},
			{Iface: "eth0", Addr: netip.MustParsePrefix("10.42.1.1/24")},
		},
		TunName: "xs0-test-none", MTU: 1360, Run: run,
		Blocked: []netip.Addr{netip.MustParseAddr("203.0.113.7"), netip.MustParseAddr("198.51.100.1")},
	}
}

func TestRuleset(t *testing.T) {
	rs := testLAN(nil).Ruleset()
	for _, want := range []string{
		"delete table ip crossstream_hub",
		`ip saddr 10.42.0.0/24 oifname "xs0-test-none" masquerade`,
		`ip saddr 10.42.1.0/24 oifname "xs0-test-none" masquerade`,
		`oifname "xs0-test-none" tcp flags syn tcp option maxseg size > 1320 tcp option maxseg size set 1320`,
		`iifname "xs0-test-none" tcp flags syn`,
		`iifname { "wlan0", "eth0" } ip daddr { 203.0.113.7, 198.51.100.1 } drop`,
	} {
		if !strings.Contains(rs, want) {
			t.Fatalf("ruleset lacks %q:\n%s", want, rs)
		}
	}
	l := testLAN(nil)
	l.Blocked = nil
	if strings.Contains(l.Ruleset(), "loopguard") {
		t.Fatal("loop guard without server IPs")
	}
	if strings.Contains(l.Ruleset(), "ttl") {
		t.Fatal("TTL chain without uplinks")
	}
	l.TTL = []string{"usb0", "enx02"}
	if !strings.Contains(l.Ruleset(), `oifname { "usb0", "enx02" } ip ttl set 65`) {
		t.Fatalf("no TTL rule:\n%s", l.Ruleset())
	}
}

// recorder answers "show"/"list" commands with canned output and records
// every command.
type recorder struct {
	cmds  []string
	route string // ip route show table 100 ("" = table does not exist)
	rule  string // ip rule show
	nft   error  // nft list table
	del   int    // rule del calls that succeed before "no such rule"
}

func (r *recorder) run(_ context.Context, _, name string, args ...string) (string, error) {
	c := name + " " + strings.Join(args, " ")
	r.cmds = append(r.cmds, c)
	switch {
	case strings.HasPrefix(c, "ip route show"):
		if r.route == "" {
			return "", errors.New("ip route show table 100: exit status 2: Error: ipv4: FIB table does not exist")
		}
		return r.route, nil
	case strings.HasPrefix(c, "ip rule show"):
		return r.rule, nil
	case strings.HasPrefix(c, "nft list"):
		return "", r.nft
	case strings.HasPrefix(c, "ip rule del"):
		if r.del > 0 {
			r.del--
			return "", nil
		}
		return "", errors.New("no such rule")
	}
	return "", nil
}

func (r *recorder) changes() []string {
	var out []string
	for _, c := range r.cmds {
		if !strings.Contains(c, " show") && !strings.HasPrefix(c, "nft list") {
			out = append(out, c)
		}
	}
	return out
}

func TestRoutesAndCleanup(t *testing.T) {
	var rec recorder
	l := testLAN(rec.run)
	var got []string
	for _, r := range l.routes(true) {
		got = append(got, "ip "+strings.Join(r.args, " "))
	}
	want := []string{
		"ip route replace 10.42.0.0/24 dev wlan0 table 100",
		"ip route replace 10.42.1.0/24 dev eth0 table 100",
		"ip route replace unreachable default metric 9999 table 100",
		"ip route replace default dev xs0-test-none table 100",
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("routes %q", got)
	}
	if len(l.routes(false)) != 3 {
		t.Fatal("tun default route without a tun")
	}
	rec.del = 2
	if err := l.Cleanup(context.Background()); err != nil {
		t.Fatal(err)
	}
	wantCleanup := []string{
		"ip rule del from 10.42.0.0/24 iif wlan0 lookup 100",
		"ip rule del from 10.42.0.0/24 iif wlan0 lookup 100",
		"ip rule del from 10.42.0.0/24 iif wlan0 lookup 100",
		"ip rule del from 10.42.1.0/24 iif eth0 lookup 100",
		"ip route flush table 100",
		"nft delete table ip crossstream_hub",
	}
	if strings.Join(rec.cmds, "|") != strings.Join(wantCleanup, "|") {
		t.Fatalf("cleanup ran %q", rec.cmds)
	}
	if err := (LAN{}).Setup(context.Background()); err == nil {
		t.Fatal("empty config accepted")
	}
}

func TestEnsure(t *testing.T) {
	if !forwardingOn() {
		t.Skip("ip_forward is off here; Ensure would try to turn it on")
	}
	allRoutes := "10.42.0.0/24 dev wlan0 scope link \n10.42.1.0/24 dev eth0 scope link\nunreachable default metric 9999\n"
	allRules := "0:\tfrom all lookup local\n100:\tfrom 10.42.0.0/24 iif wlan0 lookup 100\n" +
		"100:\tfrom 10.42.1.0/24 iif eth0 [detached] lookup 100\n32766:\tfrom all lookup main\n"
	tests := []struct {
		name  string
		rec   recorder
		fixed []string
	}{
		{"all present", recorder{route: allRoutes, rule: allRules}, nil},
		{"route and rule missing", recorder{route: "10.42.0.0/24 dev wlan0 scope link\n",
			rule: "100:\tfrom 10.42.0.0/24 iif wlan0 lookup 100\n"}, []string{
			"ip route replace 10.42.1.0/24 dev eth0 table 100",
			"ip route replace unreachable default metric 9999 table 100",
			"ip rule add from 10.42.1.0/24 iif eth0 lookup 100 priority 100",
		}},
		{"empty table", recorder{rule: allRules}, []string{
			"ip route replace 10.42.0.0/24 dev wlan0 table 100",
			"ip route replace 10.42.1.0/24 dev eth0 table 100",
			"ip route replace unreachable default metric 9999 table 100",
		}},
		{"nft table gone", recorder{route: allRoutes, rule: allRules, nft: errors.New("no such table")},
			[]string{"nft -f -"}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			rec := tc.rec
			fixed, err := testLAN(rec.run).Ensure(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			if got := rec.changes(); strings.Join(got, "|") != strings.Join(tc.fixed, "|") {
				t.Fatalf("ran %q, want %q", got, tc.fixed)
			}
			if len(fixed) != len(tc.fixed) {
				t.Fatalf("reported %q", fixed)
			}
		})
	}
}

func TestInterfaceIPv4(t *testing.T) {
	if _, err := InterfaceIPv4("no-such-iface0"); err == nil {
		t.Fatal("missing interface accepted")
	}
}

func TestSetRPFilter(t *testing.T) {
	root := ProcSys(t.TempDir())
	if err := os.MkdirAll(filepath.Dir(root.rpPath("usb0")), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(root.rpPath("usb0"), []byte("1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if old, err := root.SetRPFilter("usb0", 2); err != nil || old != 1 {
		t.Fatalf("old %d err %v", old, err)
	}
	if b, _ := os.ReadFile(root.rpPath("usb0")); string(b) != "2\n" {
		t.Fatalf("rp_filter %q", b)
	}
	if _, err := root.SetRPFilter("../all", 0); err == nil {
		t.Fatal("bad name accepted")
	}
	if _, err := root.SetRPFilter("gone0", 2); err == nil {
		t.Fatal("missing interface accepted")
	}
}
