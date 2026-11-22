package hub

import (
	"net/netip"
	"slices"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/bypass"
)

// ipv4To is a minimal IPv4 header to dst (enough for bypassPacket).
func ipv4To(dst string) []byte {
	pkt := make([]byte, 40)
	pkt[0] = 0x45
	pkt[9] = 6
	a := netip.MustParseAddr(dst).As4()
	copy(pkt[16:20], a[:])
	return pkt
}

func TestBypassProxyRole(t *testing.T) {
	d, r, port := startHubWithRelay(t)
	ds := d.directStack()
	if _, err := d.BypassSet(bypass.Rules{Domains: []string{"bad domain"}}); err == nil {
		t.Fatal("bad rule accepted")
	}
	info, err := d.BypassSet(bypass.Rules{Domains: []string{"*.Bank.Example"}, CIDRs: []string{"203.0.113.0/24"}})
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(info.Domains, []string{"bank.example"}) || info.Relays != 1 {
		t.Fatalf("bypass info %+v", info)
	}
	eventually(t, "relay proxy role on for bypass", func() bool { return r.Stats().ProxyPort == port })
	if m := d.modeMsg(); !m.Proxy || m.Mode != "bonded" {
		t.Fatalf("mode message %+v", m)
	}
	if !d.bypassPacket(ds, ipv4To("203.0.113.5")) || d.bypassPacket(ds, ipv4To("198.51.100.7")) {
		t.Fatal("CIDR dispatch")
	}
	d.learnBypass("online.bank.example", []netip.Addr{netip.MustParseAddr("198.51.100.7")}, time.Second)
	d.learnBypass("other.example", []netip.Addr{netip.MustParseAddr("198.51.100.8")}, time.Second)
	if !d.bypassPacket(ds, ipv4To("198.51.100.7")) || d.bypassPacket(ds, ipv4To("198.51.100.8")) {
		t.Fatal("DNS-learned dispatch")
	}
	if d.BypassGet().Learned != 1 {
		t.Fatalf("learned %d", d.BypassGet().Learned)
	}

	// The list is saved and survives a restart.
	var saved bypass.Rules
	if ok, err := loadJSON(d.cfg.Dir, BypassFile, &saved); !ok || err != nil || len(saved.CIDRs) != 1 {
		t.Fatalf("bypass.json %+v %v %v", saved, ok, err)
	}

	// Leaving DIRECT keeps the proxy role while the list is not empty.
	d.applyMode(time.Now(), ModeConnecting, ModeDirect)
	d.applyMode(time.Now(), ModeDirect, ModeBonded)
	if d.route.Load() != routeTunnel || d.proxyTok.Load() == nil {
		t.Fatalf("after DIRECT: route %d, proxy %v", d.route.Load(), d.proxyTok.Load() != nil)
	}

	// No relay can proxy → tunnel.
	off := "off"
	if _, err := d.SetPath("anna", PathParams{Up: &off, Down: &off}); err != nil {
		t.Fatal(err)
	}
	d.bypassTick(time.Now())
	if d.bypassPacket(ds, ipv4To("203.0.113.5")) {
		t.Fatal("bypass without a proxy relay")
	}

	// Empty list → proxy role off.
	if _, err := d.BypassSet(bypass.Rules{}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "relay proxy role off", func() bool { return r.Stats().ProxyPort == 0 })
	if d.proxyTok.Load() != nil {
		t.Fatal("proxy token kept")
	}
}
