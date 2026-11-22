package uplink

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
)

func TestClassify(t *testing.T) {
	tests := []struct {
		name, driver, devtype string
		want                  Kind
	}{
		{"usb0", "rndis_host", "", KindUSB},
		{"enx0a1b2c3d4e5f", "cdc_ncm", "", KindUSB},
		{"eth1", "ipheth", "", KindUSB},
		{"bnep0", "", "bluetooth", KindBluetooth},
		{"wwan0", "", "wwan", KindModem},
		{"wwp0s20f0u6", "cdc_mbim", "", KindModem},
		{"enp0s31f6", "e1000e", "", KindEthernet},
		{"eth1", "veth", "", KindEthernet},
		{"wlan1", "iwlwifi", "wlan", KindWiFi},
		{"wlx001122334455", "rtl8xxxu", "", KindWiFi},
	}
	for _, tt := range tests {
		if got := Classify(tt.name, tt.driver, tt.devtype); got != tt.want {
			t.Errorf("%s/%s/%s: %s, want %s", tt.name, tt.driver, tt.devtype, got, tt.want)
		}
	}
	for name, want := range map[string]bool{"xs0": true, "docker0": true, "veth12": true, "eth0": false, "apple0": false} {
		if Virtual(name) != want {
			t.Errorf("Virtual(%s) != %v", name, want)
		}
	}
}

const routeTable = `Iface	Destination	Gateway 	Flags	RefCnt	Use	Metric	Mask		MTU	Window	IRTT
usb0	00000000	012AA8C0	0003	0	0	100	00000000	0	0	0
usb0	002AA8C0	00000000	0001	0	0	100	00FFFFFF	0	0	0
eth1	00000000	0100A8C0	0003	0	0	20100	00000000	0	0	0
eth1	00000000	FE00A8C0	0003	0	0	50	00000000	0	0	0
wwan0	00000000	00000000	0001	0	0	700	00000000	0	0	0
wlan0	0000A8C0	00000000	0001	0	0	600	0000FFFF	0	0	0
down0	00000000	0100A8C0	0002	0	0	1	00000000	0	0	0
`

func TestParseDefaultRoutes(t *testing.T) {
	got := ParseDefaultRoutes(routeTable)
	want := map[string]Route{
		"usb0":  {Gateway: netip.MustParseAddr("192.168.42.1"), Metric: 100},
		"eth1":  {Gateway: netip.MustParseAddr("192.168.0.254"), Metric: 50},
		"wwan0": {Metric: 700},
	}
	if len(got) != len(want) {
		t.Fatalf("got %v", got)
	}
	for k, w := range want {
		if got[k] != w {
			t.Errorf("%s: %+v, want %+v", k, got[k], w)
		}
	}
}

func TestScan(t *testing.T) {
	root := t.TempDir()
	write := func(p, s string) {
		t.Helper()
		p = filepath.Join(root, p)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("proc/net/route", routeTable)
	write("sys/class/net/wwan0/uevent", "INTERFACE=wwan0\nDEVTYPE=wwan\n")
	mac, _ := net.ParseMAC("02:00:00:00:00:01")
	ifs := []net.Interface{
		{Name: "usb0", Flags: net.FlagUp, HardwareAddr: mac},
		{Name: "eth1", Flags: net.FlagUp},  // excluded below
		{Name: "wwan0", Flags: net.FlagUp}, // raw IP, no MAC
		{Name: "wlan0", Flags: net.FlagUp}, // no default route
		{Name: "down0", Flags: 0},          // down
		{Name: "xs0", Flags: net.FlagUp},   // virtual
	}
	addrs := func(i net.Interface) ([]net.Addr, error) {
		if i.Name == "wwan0" {
			return []net.Addr{&net.IPNet{IP: net.ParseIP("fe80::1"), Mask: net.CIDRMask(64, 128)},
				&net.IPNet{IP: net.ParseIP("10.64.1.2").To4(), Mask: net.CIDRMask(30, 32)}}, nil
		}
		return []net.Addr{&net.IPNet{IP: net.ParseIP("192.168.42.7").To4(), Mask: net.CIDRMask(24, 32)}}, nil
	}
	got, err := SysRoot(root).scan([]string{"eth1"}, func() ([]net.Interface, error) { return ifs, nil }, addrs)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].Name != "usb0" || got[0].Kind != KindUSB || got[0].Key() != "02:00:00:00:00:01" ||
		got[1].Name != "wwan0" || got[1].Kind != KindModem || got[1].Key() != "if:wwan0" ||
		got[1].Addr != netip.MustParsePrefix("10.64.1.2/30") || got[0].Metric != 100 {
		t.Fatalf("scan %+v", got)
	}
}

func TestRegistry(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 11, 14, 10, 0, 0, 0, time.UTC)
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	usb := Iface{Name: "usb0", MAC: "02:00:00:00:00:01", Kind: KindUSB}
	l, isNew, err := r.Seen(usb, now)
	if err != nil || !isNew || l.ID != "u1" || l.PathID != FirstPathID || l.Enabled || !l.TTL || l.Name != "USB usb0" {
		t.Fatalf("new usb %+v %v %v", l, isNew, err)
	}
	bt, _, _ := r.Seen(Iface{Name: "bnep0", MAC: "02:00:00:00:00:02", Kind: KindBluetooth}, now)
	if bt.PathID != FirstPathID+1 || bt.Path.Priority != control.PriorityBackup || bt.TTL {
		t.Fatalf("bt %+v", bt)
	}
	// Same MAC on another interface name: the same link.
	usb.Name = "enx020000000001"
	if l, isNew, _ = r.Seen(usb, now); isNew || l.ID != "u1" || l.Iface != "enx020000000001" {
		t.Fatalf("replug %+v", l)
	}
	on, name, prio := true, "Pixel", "secondary"
	if _, err := r.Update(Params{Uplink: "u1", Enabled: &on, Name: &name, Priority: &prio,
		Limits: &control.Limits{CapMB: 50, CapPeriod: control.PeriodDay, BatteryMin: 20}}, now); err != nil {
		t.Fatal(err)
	}
	bad := "fast"
	if _, err := r.Update(Params{Uplink: "Pixel", Priority: &bad}, now); err == nil {
		t.Fatal("bad priority accepted")
	}
	if _, err := r.Update(Params{Uplink: "u2", Name: &name}, now); err == nil {
		t.Fatal("duplicate name accepted")
	}
	if _, err := r.Update(Params{Uplink: "nope"}, now); !errors.Is(err, ErrNotFound) {
		t.Fatalf("err %v", err)
	}
	r.SetUsed("u1", 1234, now)
	if err := r.Save(); err != nil {
		t.Fatal(err)
	}
	r2, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	l, ok := r2.Get("Pixel")
	if !ok || !l.Enabled || l.Path.Priority != "secondary" || l.Limits.CapMB != 50 || l.Limits.BatteryMin != 0 || l.Used != 1234 {
		t.Fatalf("reloaded %+v", l)
	}
	if l, _ = r2.Update(Params{Uplink: "u1", ResetUsage: true}, now.Add(time.Hour)); l.Used != 0 || !l.UsedSince.Equal(now.Add(time.Hour)) {
		t.Fatalf("reset %+v", l)
	}
	if err := r2.Forget("u2"); err != nil || len(r2.List()) != 1 {
		t.Fatalf("forget %v %v", err, r2.List())
	}
}

const mmInfo = `modem.generic.ports.length                      : 3
modem.generic.ports.value[1]                    : cdc-wdm0 (mbim)
modem.generic.ports.value[2]                    : wwan0 (net)
modem.generic.ports.value[3]                    : ttyUSB0 (at)
modem.generic.access-technologies.length        : 1
modem.generic.access-technologies.value[1]      : lte
modem.generic.signal-quality.value              : 67
modem.generic.signal-quality.recent             : yes
modem.3gpp.operator-name                        : Orange
`

const mmSignal = `modem.signal.lte.rssi : -71.00
modem.signal.lte.rsrp : -101.00
modem.signal.5g.rsrp  : --
`

func TestModems(t *testing.T) {
	run := func(_ context.Context, name string, args ...string) (string, error) {
		switch strings.Join(args, " ") {
		case "-L -K":
			return "modem-list.length : 1\nmodem-list.value[1] : /org/freedesktop/ModemManager1/Modem/0\n", nil
		case "-m /org/freedesktop/ModemManager1/Modem/0 -K":
			return mmInfo, nil
		case "-m /org/freedesktop/ModemManager1/Modem/0 --signal-get -K":
			return mmSignal, nil
		}
		return "", errors.New("unexpected " + name)
	}
	got := Modems(context.Background(), run)
	want := Modem{Tech: "lte", Operator: "Orange", Quality: 67, RSRP: -101}
	if len(got) != 1 || got["wwan0"] != want {
		t.Fatalf("modems %+v", got)
	}
	none := Modems(context.Background(), func(context.Context, string, ...string) (string, error) { return "", errors.New("no mmcli") })
	if len(none) != 0 {
		t.Fatal("modems without mmcli")
	}
}
