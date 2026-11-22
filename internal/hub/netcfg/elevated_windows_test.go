package netcfg

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/sys/windows"

	"github.com/zyndata/cross-stream/internal/iphlp"
	"github.com/zyndata/cross-stream/internal/tun"
)

// testAdapter creates a Wintun adapter for an elevated test; it skips when
// the process is not elevated or wintun.dll is not next to the test binary.
// The adapter (with its routes) goes away on Close or when the process ends.
func testAdapter(t *testing.T, name, prefix string) iphlp.Iface {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("needs an elevated (administrator) process")
	}
	dev, err := tun.Create(tun.Config{Name: name, MTU: 1380, Prefix: netip.MustParsePrefix(prefix)})
	if err != nil {
		t.Skipf("Wintun unavailable (wintun.dll next to the test binary?): %v", err)
	}
	t.Cleanup(func() { _ = dev.Close() })
	ifc, err := net.InterfaceByName(dev.Name())
	if err != nil {
		t.Fatal(err)
	}
	return iphlp.Iface{Index: uint32(ifc.Index)}
}

// TestEventsOnAddressChange: adding and removing an address on a test
// adapter raises an event within 2 s each (elevated only).
func TestEventsOnAddressChange(t *testing.T) {
	ifc := testAdapter(t, "xstest2", "100.127.11.1/24")
	ctx, cancel := context.WithCancel(context.Background())
	ev, err := netlinkEvents(ctx)
	if err != nil {
		t.Fatal(err)
	}
	// drain swallows events for at least d. A new address is tentative for
	// about two seconds; Windows reports removing a tentative address only
	// when that period ends, so the test lets the address settle first.
	drain := func(d time.Duration) {
		end := time.After(d)
		for {
			select {
			case <-ev:
			case <-end:
				return
			}
		}
	}
	wait := func(what string) {
		start := time.Now()
		select {
		case <-ev:
			t.Logf("%s: event after %v", what, time.Since(start))
		case <-time.After(2 * time.Second):
			t.Fatalf("%s: no event within 2 s", what)
		}
	}
	extra := netip.MustParsePrefix("100.127.12.1/24")
	drain(500 * time.Millisecond)
	if err := iphlp.AddAddress(ifc, extra); err != nil {
		t.Fatal(err)
	}
	wait("address added")
	drain(3 * time.Second)
	if err := iphlp.DeleteAddress(ifc, extra); err != nil {
		t.Fatal(err)
	}
	wait("address removed")
	cancel()
	for range ev { // closes after the handles are cancelled
	}
}

// TestLANRoutesAndForwarding runs Ensure on a test TUN and a test port:
// the 0/1 + 128/1 routes appear, forwarding is turned on, NoPath removes
// the routes, and a host route goes on and off (elevated only; the PC's
// internet is gone for the second the test routes exist).
func TestLANRoutesAndForwarding(t *testing.T) {
	tunIfc := testAdapter(t, "xstest4", "100.127.13.1/24")
	portIfc := testAdapter(t, "xstest5", "100.127.14.1/24")
	l := LAN{TunName: "xstest4", MTU: 1380, Forward: true,
		Ports: []Port{{Iface: "xstest5", Addr: netip.MustParsePrefix("100.127.14.1/24")}}}
	ctx := context.Background()
	fixed, err := l.Ensure(ctx)
	defer func() { _ = l.Cleanup(ctx) }()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("fixed: %v", fixed)
	have, err := routesOn(tunIfc)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range tunRoutes {
		if !have[p] {
			t.Errorf("route %v missing on the TUN (have %v)", p, have)
		}
	}
	for _, ifc := range []iphlp.Iface{tunIfc, portIfc} {
		if s, err := iphlp.Interface(ifc); err != nil || !s.Forwarding {
			t.Errorf("%v: forwarding %v, err %v", ifc, s.Forwarding, err)
		}
	}
	if again, err := l.Ensure(ctx); err != nil || len(again) != 0 {
		t.Errorf("second Ensure fixed %v, err %v; want nothing", again, err)
	}

	l.NoPath = true
	if _, err := l.Ensure(ctx); err != nil {
		t.Fatal(err)
	}
	have, _ = routesOn(tunIfc)
	for _, p := range tunRoutes {
		if have[p] {
			t.Errorf("route %v still there with NoPath", p)
		}
	}

	hr := HostRoute{Dst: netip.MustParseAddr("192.0.2.77"), Iface: "xstest5"}
	if err := hr.Add(ctx); err != nil {
		t.Fatal(err)
	}
	if have, _ = routesOn(portIfc); !have[netip.MustParsePrefix("192.0.2.77/32")] {
		t.Errorf("host route missing: %v", have)
	}
	if err := hr.Del(ctx); err != nil {
		t.Fatal(err)
	}
	if have, _ = routesOn(portIfc); have[netip.MustParsePrefix("192.0.2.77/32")] {
		t.Error("host route still there after Del")
	}
}
