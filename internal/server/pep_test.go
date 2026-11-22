package server

import (
	"net/netip"
	"testing"
)

func TestPEPAllowed(t *testing.T) {
	tests := []struct {
		dst  string
		want bool
	}{
		{"93.184.216.34:443", true},
		{"10.1.2.3:22", false},        // private: egress filter
		{"169.254.169.254:80", false}, // cloud metadata
		{"93.184.216.34:25", false},   // SMTP
		{"172.30.0.10:80", true},      // egress_allow below
		{"127.0.0.1:8080", false},
		{"127.5.5.5:80", false},
		{"0.0.0.0:80", false},
		{"224.0.0.1:80", false},
		{"255.255.255.255:80", false},
		{"100.64.0.3:80", false}, // another hub: packet mode
		{"8.8.8.8:0", false},
		{"[2001:db8::1]:443", false},
	}
	e, err := ParseEgress([]string{"172.30.0.0/24"})
	if err != nil {
		t.Fatal(err)
	}
	for _, tt := range tests {
		if got := pepAllowed(e, netip.MustParseAddrPort(tt.dst)); got != tt.want {
			t.Errorf("pepAllowed(%s) = %v, want %v", tt.dst, got, tt.want)
		}
	}
}
