package direct

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"
)

func TestSelectorPick(t *testing.T) {
	t0 := time.Unix(1000, 0)
	ip := func(s string) netip.Addr { return netip.MustParseAddr(s) }
	a, b, c := Relay{ID: "a"}, Relay{ID: "b"}, Relay{ID: "c"}
	type step struct {
		at     time.Duration
		dst    string
		relays []Relay
		flows  map[string]int
		want   string // "" = none
	}
	tests := []struct {
		name  string
		steps []step
	}{
		{"least flows", []step{
			{0, "1.1.1.1", []Relay{a, b, c}, map[string]int{"a": 2, "b": 0, "c": 1}, "b"},
		}},
		{"tie goes to lowest id", []step{
			{0, "1.1.1.1", []Relay{c, b, a}, nil, "a"},
		}},
		{"weighted", []step{ // a: 3 flows at 8 Mb/s beats b: 1 flow at 1 Mb/s
			{0, "1.1.1.1", []Relay{{ID: "a", Weight: 1e6}, b}, map[string]int{"a": 3, "b": 1}, "a"},
		}},
		{"sticky per destination", []step{
			{0, "1.1.1.1", []Relay{a, b}, nil, "a"},
			{time.Minute, "1.1.1.1", []Relay{a, b}, map[string]int{"a": 5}, "a"},
			{2 * time.Minute, "2.2.2.2", []Relay{a, b}, map[string]int{"a": 5}, "b"},
		}},
		{"sticky expires", []step{
			{0, "1.1.1.1", []Relay{a, b}, nil, "a"},
			{StickyFor + time.Second, "1.1.1.1", []Relay{a, b}, map[string]int{"a": 1}, "b"},
		}},
		{"sticky refreshed by new flows", []step{
			{0, "1.1.1.1", []Relay{a, b}, nil, "a"},
			{StickyFor - time.Second, "1.1.1.1", []Relay{a, b}, map[string]int{"a": 1}, "a"},
			{2*StickyFor - 2*time.Second, "1.1.1.1", []Relay{a, b}, map[string]int{"a": 1}, "a"},
		}},
		{"sticky relay gone", []step{
			{0, "1.1.1.1", []Relay{a, b}, nil, "a"},
			{time.Second, "1.1.1.1", []Relay{b, c}, nil, "b"},
		}},
		{"backup only without normal", []step{
			{0, "1.1.1.1", []Relay{{ID: "a", Backup: true}, b}, map[string]int{"b": 9}, "b"},
			{time.Second, "2.2.2.2", []Relay{{ID: "a", Backup: true}}, nil, "a"},
		}},
		{"sticky to backup leaves when normal returns", []step{
			{0, "1.1.1.1", []Relay{{ID: "a", Backup: true}}, nil, "a"},
			{time.Second, "1.1.1.1", []Relay{{ID: "a", Backup: true}, b}, nil, "b"},
		}},
		{"no relay", []step{
			{0, "1.1.1.1", nil, nil, ""},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := newSelector()
			for i, st := range tt.steps {
				r, ok := s.pick(t0.Add(st.at), ip(st.dst), st.relays, st.flows)
				if ok != (st.want != "") || r.ID != st.want {
					t.Fatalf("step %d: pick = %q, %v; want %q", i, r.ID, ok, st.want)
				}
			}
		})
	}
}

func TestSelectorExpire(t *testing.T) {
	s := newSelector()
	t0 := time.Unix(1000, 0)
	s.pick(t0, netip.MustParseAddr("1.1.1.1"), []Relay{{ID: "a"}}, nil)
	s.expire(t0.Add(StickyFor - time.Second))
	if len(s.sticky) != 1 {
		t.Fatal("entry expired early")
	}
	s.expire(t0.Add(StickyFor))
	if len(s.sticky) != 0 {
		t.Fatal("entry not expired")
	}
}

// ipv4Packet builds a minimal IPv4 header + 4 port bytes.
func ipv4Packet(proto uint8, src, dst string, sport, dport uint16, fragOff uint16) []byte {
	p := make([]byte, 28)
	p[0] = 0x45
	binary.BigEndian.PutUint16(p[6:], fragOff)
	p[9] = proto
	s4, d4 := netip.MustParseAddr(src).As4(), netip.MustParseAddr(dst).As4()
	copy(p[12:], s4[:])
	copy(p[16:], d4[:])
	binary.BigEndian.PutUint16(p[20:], sport)
	binary.BigEndian.PutUint16(p[22:], dport)
	return p
}

func TestParseKey(t *testing.T) {
	tcp := ipv4Packet(protoTCP, "10.0.0.2", "1.2.3.4", 40000, 443, 0)
	k, ok := ParseKey(tcp)
	want := FlowKey{Proto: protoTCP, Src: netip.MustParseAddrPort("10.0.0.2:40000"), Dst: netip.MustParseAddrPort("1.2.3.4:443")}
	if !ok || k != want {
		t.Fatalf("ParseKey = %+v, %v", k, ok)
	}
	opts := append([]byte{0x46}, tcp[1:20]...) // IHL 6: 4 option bytes before the ports
	opts = append(append(opts, 0, 0, 0, 0), tcp[20:]...)
	if k, ok := ParseKey(opts); !ok || k != want {
		t.Fatalf("with options: %+v, %v", k, ok)
	}
	bad := []struct {
		name string
		pkt  []byte
	}{
		{"short", tcp[:19]},
		{"ipv6", append([]byte{0x60}, tcp[1:]...)},
		{"icmp", ipv4Packet(1, "10.0.0.2", "1.2.3.4", 0, 0, 0)},
		{"later fragment", ipv4Packet(protoUDP, "10.0.0.2", "1.2.3.4", 1, 2, 100)},
		{"no ports", tcp[:22]},
	}
	for _, b := range bad {
		if _, ok := ParseKey(b.pkt); ok {
			t.Errorf("%s: parsed", b.name)
		}
	}
	if k, ok := ParseKey(ipv4Packet(protoUDP, "10.0.0.2", "1.2.3.4", 5353, 53, 0x2000)); !ok || k.Proto != protoUDP {
		t.Errorf("first fragment (MF set) not parsed: %+v %v", k, ok)
	}
}
