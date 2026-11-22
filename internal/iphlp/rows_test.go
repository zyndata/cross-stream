package iphlp

import (
	"net/netip"
	"testing"
)

func img(family byte, a, b, c, d byte) sockaddrInet {
	var s sockaddrInet
	s[0], s[4], s[5], s[6], s[7] = family, a, b, c, d
	return s
}

func TestInet4(t *testing.T) {
	tests := []struct {
		name    string
		in      netip.Addr
		want    sockaddrInet
		wantErr bool
	}{
		{"plain", netip.MustParseAddr("100.64.0.1"), img(2, 100, 64, 0, 1), false},
		{"mapped", netip.MustParseAddr("::ffff:192.0.2.7"), img(2, 192, 0, 2, 7), false},
		{"unspecified", netip.IPv4Unspecified(), img(2, 0, 0, 0, 0), false},
		{"ipv6", netip.MustParseAddr("2001:db8::1"), sockaddrInet{}, true},
		{"invalid", netip.Addr{}, sockaddrInet{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := inet4(tt.in)
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got % x, want % x", got, tt.want)
			}
			if err == nil {
				if back, ok := addr4(got); !ok || back != tt.in.Unmap() {
					t.Fatalf("addr4 = %v %v, want %v", back, ok, tt.in.Unmap())
				}
			}
		})
	}
	if _, ok := addr4(sockaddrInet{23}); ok { // AF_INET6 image
		t.Fatal("addr4 accepted a non-IPv4 family")
	}
}

func TestRouteFields(t *testing.T) {
	tests := []struct {
		name    string
		r       Route
		want    routeFields
		wantErr bool
	}{
		{"tun half on-link", Route{Dst: netip.MustParsePrefix("128.0.0.0/1")},
			routeFields{dst: img(2, 128, 0, 0, 0), dstLen: 1, nextHop: img(2, 0, 0, 0, 0)}, false},
		{"unmasked prefix", Route{Dst: netip.MustParsePrefix("10.1.2.3/8"), Metric: 7},
			routeFields{dst: img(2, 10, 0, 0, 0), dstLen: 8, nextHop: img(2, 0, 0, 0, 0), metric: 7}, false},
		{"host via gateway", Route{Dst: netip.MustParsePrefix("192.0.2.7/32"), NextHop: netip.MustParseAddr("192.168.42.129")},
			routeFields{dst: img(2, 192, 0, 2, 7), dstLen: 32, nextHop: img(2, 192, 168, 42, 129)}, false},
		{"ipv6 dst", Route{Dst: netip.MustParsePrefix("2001:db8::/32")}, routeFields{}, true},
		{"ipv6 next hop", Route{Dst: netip.MustParsePrefix("0.0.0.0/1"), NextHop: netip.MustParseAddr("fe80::1")}, routeFields{}, true},
		{"no dst", Route{}, routeFields{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := tt.r.fields()
			if (err != nil) != tt.wantErr {
				t.Fatalf("err = %v, wantErr %v", err, tt.wantErr)
			}
			if got != tt.want {
				t.Fatalf("got %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestAddressFields(t *testing.T) {
	tests := []struct {
		name    string
		in      netip.Prefix
		want    addrFields
		wantErr bool
	}{
		{"tun /16", netip.MustParsePrefix("100.64.0.3/16"), addrFields{addr: img(2, 100, 64, 0, 3), onLink: 16}, false},
		{"host /32", netip.MustParsePrefix("10.9.8.7/32"), addrFields{addr: img(2, 10, 9, 8, 7), onLink: 32}, false},
		{"ipv6", netip.MustParsePrefix("fd00::1/64"), addrFields{}, true},
		{"invalid", netip.Prefix{}, addrFields{}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := addressFields(tt.in)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("got %+v, %v; want %+v, wantErr %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}

func TestSettingsApply(t *testing.T) {
	auto := ifaceFields{nlMtu: 1500, useAutomaticMetric: 1, metric: 25}
	tests := []struct {
		name    string
		s       Settings
		in      ifaceFields
		want    ifaceFields
		changed bool
	}{
		{"nothing", Settings{}, auto, auto, false},
		{"mtu", Settings{MTU: 1380}, auto, ifaceFields{nlMtu: 1380, useAutomaticMetric: 1, metric: 25}, true},
		{"same mtu", Settings{MTU: 1500}, auto, auto, false},
		{"metric turns automatic off", Settings{Metric: 5}, auto, ifaceFields{nlMtu: 1500, metric: 5}, true},
		{"automatic with the same value", Settings{Metric: 25}, auto, ifaceFields{nlMtu: 1500, metric: 25}, true},
		{"metric already manual", Settings{Metric: 5}, ifaceFields{nlMtu: 1500, metric: 5}, ifaceFields{nlMtu: 1500, metric: 5}, false},
		{"forwarding on", Settings{Forwarding: true}, auto, ifaceFields{nlMtu: 1500, useAutomaticMetric: 1, metric: 25, forwardingEnabled: 1}, true},
		{"forwarding already on", Settings{Forwarding: true}, ifaceFields{forwardingEnabled: 1}, ifaceFields{forwardingEnabled: 1}, false},
		{"forwarding never turned off", Settings{}, ifaceFields{forwardingEnabled: 1}, ifaceFields{forwardingEnabled: 1}, false},
		{"all", Settings{MTU: 1380, Metric: 5, Forwarding: true}, auto, ifaceFields{nlMtu: 1380, metric: 5, forwardingEnabled: 1}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := tt.in
			if changed := tt.s.apply(&f); changed != tt.changed || f != tt.want {
				t.Fatalf("got %+v changed=%v, want %+v changed=%v", f, changed, tt.want, tt.changed)
			}
		})
	}
}

func TestNameServers(t *testing.T) {
	tests := []struct {
		name    string
		in      []netip.Addr
		want    string
		wantErr bool
	}{
		{"none", nil, "", false},
		{"one", []netip.Addr{netip.MustParseAddr("1.1.1.1")}, "1.1.1.1", false},
		{"two, mapped", []netip.Addr{netip.MustParseAddr("1.1.1.1"), netip.MustParseAddr("::ffff:9.9.9.9")}, "1.1.1.1,9.9.9.9", false},
		{"ipv6", []netip.Addr{netip.MustParseAddr("2606:4700::1111")}, "", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := nameServers(tt.in)
			if (err != nil) != tt.wantErr || got != tt.want {
				t.Fatalf("got %q, %v; want %q, wantErr %v", got, err, tt.want, tt.wantErr)
			}
		})
	}
}
