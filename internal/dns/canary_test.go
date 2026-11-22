package dns

import (
	"context"
	"encoding/binary"
	"sync/atomic"
	"testing"
	"time"
)

func TestCanary(t *testing.T) {
	up := newUpstream(t, func(q []byte, _ bool) []byte { return buildAnswer(q, rcodeOK, 300, 1, 0) })
	var on atomic.Bool
	s := newServer(&clock{t: time.Unix(1_000_000, 0)}, up.addr)
	s.cfg.Canary = on.Load
	tests := []struct {
		name   string
		qtype  uint16
		edns   uint16
		on     bool
		wantNX bool
	}{
		{"use-application-dns.net", 1, 0, true, true},
		{"USE-APPLICATION-DNS.NET.", 28, 0, true, true}, // AAAA, any case, trailing dot
		{"mask.icloud.com", 65, 1232, true, true},       // HTTPS with EDNS
		{"mask-h2.icloud.com", 1, 0, true, true},
		{"use-application-dns.net", 1, 0, false, false}, // empty bypass list: forwarded
		{"x.mask.icloud.com", 1, 0, true, false},        // subdomain: forwarded
		{"icloud.com", 1, 0, true, false},
		{"example.com", 1, 0, true, false},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			on.Store(tc.on)
			q := buildQuery(0x4242, tc.name, tc.edns)
			binary.BigEndian.PutUint16(q[len(q)-4-optLen(tc.edns):], tc.qtype)
			before := up.udpN.Load()
			r := s.Answer(context.Background(), q, false)
			if r == nil || binary.BigEndian.Uint16(r) != 0x4242 {
				t.Fatalf("answer %x", r)
			}
			asked := up.udpN.Load() > before
			if tc.wantNX {
				if rcode(r) != rcodeNX || asked || binary.BigEndian.Uint16(r[6:]) != 0 || !sameQuestion(q, len(r), r) {
					t.Fatalf("want local NXDOMAIN, rcode %d asked %v answer %x", rcode(r), asked, r)
				}
				if binary.BigEndian.Uint16(r[2:])&0x0100 == 0 {
					t.Fatal("RD bit not copied")
				}
				return
			}
			if rcode(r) != rcodeOK {
				t.Fatalf("rcode %d", rcode(r))
			}
		})
	}
}

// optLen is the length of the OPT record buildQuery adds for edns > 0.
func optLen(edns uint16) int {
	if edns > 0 {
		return 11
	}
	return 0
}
