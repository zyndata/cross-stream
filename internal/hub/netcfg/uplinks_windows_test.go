package netcfg

import (
	"net/netip"
	"testing"

	"github.com/zyndata/cross-stream/internal/hub/uplink"
)

func TestUplinkOf(t *testing.T) {
	addr, gw := netip.MustParsePrefix("192.168.42.129/24"), netip.MustParseAddr("192.168.42.1")
	tests := []struct {
		name string
		a    adapter
		want uplink.Kind // "" = not an uplink
	}{
		{"android usb", adapter{Name: "Ethernet 3", Description: "Remote NDIS based Internet Sharing Device", IfType: 6, Up: true, Addr: addr, Gateway: gw}, uplink.KindUSB},
		{"iphone usb", adapter{Name: "Ethernet 4", Description: "Apple Mobile Device Ethernet", IfType: 6, Up: true, Addr: addr, Gateway: gw}, uplink.KindUSB},
		{"wwan", adapter{Name: "Komórkowe", Description: "Generic Mobile Broadband Adapter", IfType: 243, Up: true, Addr: addr, Gateway: gw}, uplink.KindModem},
		{"wifi client", adapter{Name: "Wi-Fi", Description: "Intel(R) Wi-Fi 6 AX201 160MHz", IfType: 71, Up: true, Addr: addr, Gateway: gw}, uplink.KindWiFi},
		{"bt pan", adapter{Name: "Połączenie sieciowe Bluetooth", Description: "Bluetooth Device (Personal Area Network)", IfType: 6, Up: true, Addr: addr, Gateway: gw}, uplink.KindBluetooth},
		{"ethernet", adapter{Name: "Ethernet", Description: "Realtek PCIe GbE Family Controller", IfType: 6, Up: true, Addr: addr, Gateway: gw}, uplink.KindEthernet},
		{"no gateway", adapter{Name: "Ethernet", Description: "Realtek", IfType: 6, Up: true, Addr: addr}, ""},
		{"down", adapter{Name: "Ethernet", Description: "Realtek", IfType: 6, Addr: addr, Gateway: gw}, ""},
		{"tunnel", adapter{Name: "xs0", Description: "WireGuard Tunnel", IfType: 53, Up: true, Addr: addr, Gateway: gw}, ""},
		{"hyper-v", adapter{Name: "vEthernet (WSL)", Description: "Hyper-V Virtual Ethernet Adapter", IfType: 6, Up: true, Addr: addr, Gateway: gw}, ""},
		{"hotspot", adapter{Name: "Połączenie lokalne* 10", Description: "Microsoft Wi-Fi Direct Virtual Adapter #2", IfType: 71, Up: true, Addr: addr, Gateway: gw}, ""},
		{"excluded", adapter{Name: "LAN", Description: "Realtek", IfType: 6, Up: true, Addr: addr, Gateway: gw}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			i, ok := uplinkOf(tt.a, []string{"LAN"})
			if ok != (tt.want != "") || (ok && i.Kind != tt.want) {
				t.Fatalf("uplinkOf = %+v %v, want kind %q", i, ok, tt.want)
			}
		})
	}
}

func TestScanUplinksRuns(t *testing.T) {
	ups, err := ScanUplinks(nil) // real adapters: must not fail
	if err != nil {
		t.Fatal(err)
	}
	for _, u := range ups {
		t.Logf("uplink %q kind=%s addr=%s gw=%s metric=%d", u.Name, u.Kind, u.Addr, u.Gateway, u.Metric)
	}
	as, _ := adapters()
	for _, a := range as {
		t.Logf("adapter %q %q type=%d up=%v addr=%s gw=%s", a.Name, a.Description, a.IfType, a.Up, a.Addr, a.Gateway)
	}
}
