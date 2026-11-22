package hub

import (
	"context"
	"net/netip"
	"slices"
	"testing"

	"github.com/zyndata/cross-stream/internal/hub/netcfg"
)

func TestServerIPs(t *testing.T) {
	tests := []struct {
		name string
		eps  []string
		want []netip.Addr
	}{
		{"none", nil, nil},
		{"literal once", []string{"udp://192.0.2.7:443", "tcp://192.0.2.7:443"}, []netip.Addr{netip.MustParseAddr("192.0.2.7")}},
		{"ipv6 and junk skipped", []string{"udp://[2001:db8::1]:443", "::bad", "udp://198.51.100.2:443"}, []netip.Addr{netip.MustParseAddr("198.51.100.2")}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := serverIPs(context.Background(), tt.eps)
			want := tt.want
			if !netcfg.LoopGuard {
				want = nil // Windows: nothing to guard, nothing resolved
			}
			if !slices.Equal(got, want) {
				t.Fatalf("serverIPs(%v) = %v, want %v", tt.eps, got, want)
			}
		})
	}
}
