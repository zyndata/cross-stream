package tun

import (
	"net"
	"net/netip"
	"testing"
	"time"

	"golang.org/x/sys/windows"
	wgtun "golang.zx2c4.com/wireguard/tun"

	"github.com/zyndata/cross-stream/internal/iphlp"
)

// requireElevatedWintun skips unless the test runs elevated; the Wintun
// driver also needs wintun.dll next to the test binary (skipped otherwise).
func requireElevatedWintun(t *testing.T) {
	t.Helper()
	if !windows.GetCurrentProcessToken().IsElevated() {
		t.Skip("needs an elevated (administrator) process")
	}
}

// TestCreateConfiguresAdapter creates a test Wintun adapter and reads its
// address and MTU back from the IP Helper API (elevated only).
func TestCreateConfiguresAdapter(t *testing.T) {
	requireElevatedWintun(t)
	cfg := Config{Name: "xstest0", MTU: 1380, Prefix: netip.MustParsePrefix("100.127.9.1/24"),
		DNS: []netip.Addr{netip.MustParseAddr("192.0.2.53"), netip.MustParseAddr("198.51.100.53")}}
	start := time.Now()
	dev, err := Create(cfg)
	if err != nil {
		t.Skipf("Wintun unavailable (wintun.dll next to the test binary?): %v", err)
	}
	t.Logf("adapter up and configured in %v", time.Since(start))
	defer func() { _ = dev.Close() }()

	s, addrs := readBack(t, dev.Name())
	if s.MTU != uint32(cfg.MTU) || s.Metric != TunMetric {
		t.Errorf("MTU, metric = %d, %d; want %d, %d", s.MTU, s.Metric, cfg.MTU, TunMetric)
	}
	if len(addrs) != 1 || addrs[0] != cfg.Prefix {
		t.Errorf("addresses = %v, want [%v]", addrs, cfg.Prefix)
	}
	ifc, _ := net.InterfaceByName(dev.Name())
	if dns, err := iphlp.DNS(iphlp.Iface{Index: uint32(ifc.Index)}); err != nil || len(dns) != 2 ||
		dns[0] != cfg.DNS[0] || dns[1] != cfg.DNS[1] {
		t.Errorf("DNS = %v, %v; want %v", dns, err, cfg.DNS)
	}
	if err := SetMTU(dev.Name(), 1300); err != nil {
		t.Fatalf("SetMTU: %v", err)
	}
	if s, _ := readBack(t, dev.Name()); s.MTU != 1300 {
		t.Errorf("MTU after SetMTU = %d, want 1300", s.MTU)
	}
}

func readBack(t *testing.T, alias string) (iphlp.Settings, []netip.Prefix) {
	t.Helper()
	ifc, err := net.InterfaceByName(alias)
	if err != nil {
		t.Fatalf("interface %s: %v", alias, err)
	}
	id := iphlp.Iface{Index: uint32(ifc.Index)}
	s, err := iphlp.Interface(id)
	if err != nil {
		t.Fatal(err)
	}
	addrs, err := iphlp.Addresses(id)
	if err != nil {
		t.Fatal(err)
	}
	return s, addrs
}

// TestAdapterSetupTime splits the adapter start into Wintun creation and the
// address/MTU/metric configuration (elevated only; numbers for decisions.md).
func TestAdapterSetupTime(t *testing.T) {
	requireElevatedWintun(t)
	cfg := Config{Name: "xstest1", MTU: 1380, Prefix: netip.MustParsePrefix("100.127.10.1/24")}
	guid := AdapterGUID(cfg.Name)
	start := time.Now()
	dev, err := wgtun.CreateTUNWithRequestedGUID(cfg.Name, &guid, cfg.MTU)
	if err != nil {
		t.Skipf("Wintun unavailable: %v", err)
	}
	defer func() { _ = dev.Close() }()
	created := time.Since(start)
	nt := dev.(*wgtun.NativeTun)
	start = time.Now()
	if err := configure(iphlp.Iface{LUID: nt.LUID()}, cfg); err != nil {
		t.Fatal(err)
	}
	t.Logf("wintun create %v, configure %v", created, time.Since(start))
}
