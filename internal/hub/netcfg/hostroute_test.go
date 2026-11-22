package netcfg

import (
	"context"
	"net/netip"
	"runtime"
	"strings"
	"testing"
)

func TestHostRoute(t *testing.T) {
	if runtime.GOOS == "windows" {
		for _, r := range []HostRoute{{Dst: netip.MustParseAddr("2001:db8::1"), Iface: "x"},
			{Dst: netip.MustParseAddr("192.0.2.1")}, {Dst: netip.MustParseAddr("192.0.2.1"), Iface: "no such interface"}} {
			if err := r.Add(context.Background()); err == nil {
				t.Fatalf("%+v: no error", r)
			}
		}
		return
	}
	if runtime.GOOS != "linux" {
		if err := (HostRoute{Dst: netip.MustParseAddr("192.0.2.1"), Iface: "x"}).Add(context.Background()); err != ErrNoHostRoute {
			t.Fatalf("err %v", err)
		}
		return
	}
	tests := []struct {
		name    string
		r       HostRoute
		add     string
		del     string
		wantErr bool
	}{
		{"via gateway", HostRoute{Dst: netip.MustParseAddr("192.0.2.7"), Iface: "usb0", GW: netip.MustParseAddr("192.168.42.129")},
			"ip -4 route replace 192.0.2.7/32 via 192.168.42.129 dev usb0", "ip -4 route del 192.0.2.7/32 dev usb0", false},
		{"point to point", HostRoute{Dst: netip.MustParseAddr("198.51.100.1"), Iface: "wwan0"},
			"ip -4 route replace 198.51.100.1/32 dev wwan0", "ip -4 route del 198.51.100.1/32 dev wwan0", false},
		{"ipv6", HostRoute{Dst: netip.MustParseAddr("2001:db8::1"), Iface: "usb0"}, "", "", true},
		{"no iface", HostRoute{Dst: netip.MustParseAddr("192.0.2.7")}, "", "", true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			var got []string
			tc.r.Run = func(_ context.Context, _ string, name string, args ...string) (string, error) {
				got = append(got, name+" "+strings.Join(args, " "))
				return "", nil
			}
			errA := tc.r.Add(context.Background())
			errD := tc.r.Del(context.Background())
			if (errA != nil) != tc.wantErr || (errD != nil) != tc.wantErr {
				t.Fatalf("errors %v %v", errA, errD)
			}
			if !tc.wantErr && (len(got) != 2 || got[0] != tc.add || got[1] != tc.del) {
				t.Fatalf("commands %q", got)
			}
		})
	}
}
