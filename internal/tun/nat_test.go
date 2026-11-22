package tun

import (
	"encoding/binary"
	"net/netip"
	"testing"
	"time"
)

var (
	natInside  = netip.MustParsePrefix("192.168.137.0/24")
	natOutside = netip.MustParseAddr("100.64.0.7")
	natClient  = netip.MustParseAddr("192.168.137.23")
	natRemote  = netip.MustParseAddr("203.0.113.9")
)

// ipPacket builds an IPv4 packet with valid checksums; flags = TCP flags or
// ICMP type.
func ipPacket(proto uint8, src, dst netip.Addr, sport, dport uint16, flags byte, payload int) []byte {
	hl := map[uint8]int{protoTCP: 20, protoUDP: 8, protoICMP: 8}[proto]
	if hl == 0 {
		hl = 8
	}
	p := make([]byte, 20+hl+payload)
	p[0], p[8], p[9] = 0x45, 64, proto
	binary.BigEndian.PutUint16(p[2:], uint16(len(p)))
	s, d := src.As4(), dst.As4()
	copy(p[12:], s[:])
	copy(p[16:], d[:])
	h := p[20:]
	for i := range payload {
		h[hl+i] = byte(i * 7)
	}
	switch proto {
	case protoTCP:
		binary.BigEndian.PutUint16(h[0:], sport)
		binary.BigEndian.PutUint16(h[2:], dport)
		h[12], h[13] = 5<<4, flags
	case protoUDP:
		binary.BigEndian.PutUint16(h[0:], sport)
		binary.BigEndian.PutUint16(h[2:], dport)
		binary.BigEndian.PutUint16(h[4:], uint16(hl+payload))
	case protoICMP:
		h[0] = flags
		binary.BigEndian.PutUint16(h[4:], sport) // echo id
		binary.BigEndian.PutUint16(h[6:], 1)
	}
	fixChecksums(p)
	return p
}

func sum16(b []byte, s uint32) uint32 {
	for i := 0; i+1 < len(b); i += 2 {
		s += uint32(binary.BigEndian.Uint16(b[i:]))
	}
	if len(b)%2 == 1 {
		s += uint32(b[len(b)-1]) << 8
	}
	return s
}

func fold(s uint32) uint16 {
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	return ^uint16(s)
}

// l4Sum returns the transport checksum over pkt as it is (0 when valid,
// except UDP 0 = none).
func l4Sum(p []byte) uint16 {
	h := p[20:]
	s := uint32(0)
	if p[9] != protoICMP {
		s = sum16(p[12:20], uint32(p[9])+uint32(len(h)))
	}
	return fold(sum16(h, s))
}

func fixChecksums(p []byte) {
	p[10], p[11] = 0, 0
	binary.BigEndian.PutUint16(p[10:], fold(sum16(p[:20], 0)))
	off := map[uint8]int{protoTCP: 16, protoUDP: 6, protoICMP: 2}[p[9]]
	if off == 0 {
		return
	}
	h := p[20:]
	h[off], h[off+1] = 0, 0
	binary.BigEndian.PutUint16(h[off:], l4Sum(p))
}

func checkSums(t *testing.T, what string, p []byte) {
	t.Helper()
	if fold(sum16(p[:20], 0)) != 0 {
		t.Errorf("%s: bad IP checksum", what)
	}
	if p[9] == protoUDP && binary.BigEndian.Uint16(p[26:]) == 0 {
		return
	}
	if l4Sum(p) != 0 {
		t.Errorf("%s: bad transport checksum", what)
	}
}

func addrAt(p []byte, off int) netip.Addr {
	a, _ := netip.AddrFromSlice(p[off : off+4])
	return a
}

func TestNATRoundTrip(t *testing.T) {
	tests := []struct {
		name  string
		proto uint8
		flags byte // TCP flags / ICMP type of the request
		reply byte // ICMP type of the reply
		nosum bool // UDP without checksum
	}{
		{"tcp", protoTCP, 0x02, 0, false},
		{"udp", protoUDP, 0, 0, false},
		{"udp no checksum", protoUDP, 0, 0, true},
		{"icmp echo", protoICMP, 8, 0, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tb := newNATTable(natInside, natOutside, time.Now)
			p := ipPacket(tt.proto, natClient, natRemote, 40001, 443, tt.flags, 33)
			if tt.nosum {
				binary.BigEndian.PutUint16(p[26:], 0)
			}
			if !tb.out(p) {
				t.Fatal("outbound packet dropped")
			}
			if addrAt(p, 12) != natOutside {
				t.Fatalf("source %v, want %v", addrAt(p, 12), natOutside)
			}
			checkSums(t, "outbound", p)
			if tt.nosum && binary.BigEndian.Uint16(p[26:]) != 0 {
				t.Error("UDP checksum 0 changed")
			}
			port := binary.BigEndian.Uint16(p[20:])
			if tt.proto == protoICMP {
				port = binary.BigEndian.Uint16(p[24:])
			}
			if port < natPortLo || port > natPortHi {
				t.Errorf("outside port %d outside the pool", port)
			}
			r := ipPacket(tt.proto, natRemote, natOutside, 443, port, tt.reply, 21)
			if tt.proto == protoICMP {
				r = ipPacket(tt.proto, natRemote, natOutside, port, 0, tt.reply, 21)
			}
			tb.in(r)
			if addrAt(r, 16) != natClient {
				t.Fatalf("reply destination %v, want %v", addrAt(r, 16), natClient)
			}
			want, at := uint16(40001), 22
			if tt.proto == protoICMP {
				at = 24
			}
			if got := binary.BigEndian.Uint16(r[20+at-20:]); got != want {
				t.Errorf("reply port/id %d, want %d", got, want)
			}
			checkSums(t, "inbound", r)
		})
	}
}

func TestNATLeavesOtherTraffic(t *testing.T) {
	tb := newNATTable(natInside, natOutside, time.Now)
	own := ipPacket(protoTCP, natOutside, natRemote, 50000, 443, 0x02, 0)
	orig := append([]byte(nil), own...)
	if !tb.out(own) || string(own) != string(orig) {
		t.Error("the host's own packet was changed or dropped")
	}
	in := ipPacket(protoTCP, natRemote, natOutside, 443, 50000, 0x12, 0)
	orig = append([]byte(nil), in...)
	tb.in(in)
	if string(in) != string(orig) {
		t.Error("a reply to the host's own connection was translated")
	}
	drops := map[string][]byte{
		"icmp error from inside": ipPacket(protoICMP, natClient, natRemote, 1, 0, 3, 8),
		"other protocol":         ipPacket(47, natClient, natRemote, 0, 0, 0, 8),
	}
	frag := ipPacket(protoUDP, natClient, natRemote, 1000, 53, 0, 8)
	binary.BigEndian.PutUint16(frag[6:], 0x2000) // MF
	drops["fragment"] = frag
	for name, p := range drops {
		if tb.out(p) {
			t.Errorf("%s: passed with a private source", name)
		}
	}
}

func TestNATTimeouts(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tb := newNATTable(natInside, natOutside, func() time.Time { return now })
	tests := []struct {
		name  string
		proto uint8
		flags byte
		idle  time.Duration
		alive bool
	}{
		{"udp fresh", protoUDP, 0, NATTimeoutUDP - time.Second, true},
		{"udp idle", protoUDP, 0, NATTimeoutUDP + time.Second, false},
		{"tcp open", protoTCP, 0x10, time.Hour, true},
		{"tcp after FIN", protoTCP, 0x11, NATTimeoutTCPDone + time.Second, false},
		{"icmp idle", protoICMP, 8, NATTimeoutICMP + time.Second, false},
	}
	for i, tt := range tests {
		sport := uint16(30000 + i)
		p := ipPacket(tt.proto, natClient, natRemote, sport, 80, tt.flags, 0)
		if !tb.out(p) {
			t.Fatalf("%s: dropped", tt.name)
		}
		port := binary.BigEndian.Uint16(p[20:])
		if tt.proto == protoICMP {
			port = binary.BigEndian.Uint16(p[24:])
		}
		now = now.Add(tt.idle)
		r := ipPacket(tt.proto, natRemote, natOutside, 80, port, 0x10, 0)
		if tt.proto == protoICMP {
			r = ipPacket(tt.proto, natRemote, natOutside, port, 0, 0, 0)
		}
		tb.in(r)
		if got := addrAt(r, 16) == natClient; got != tt.alive {
			t.Errorf("%s: reply translated %v, want %v", tt.name, got, tt.alive)
		}
	}
}

func TestNATPortReuseAndPool(t *testing.T) {
	now := time.Unix(1_700_000_000, 0)
	tb := newNATTable(natInside, natOutside, func() time.Time { return now })
	a := ipPacket(protoUDP, natClient, natRemote, 5000, 53, 0, 0)
	b := ipPacket(protoUDP, natClient, natRemote, 5000, 53, 0, 0)
	tb.out(a)
	tb.out(b)
	if binary.BigEndian.Uint16(a[20:]) != binary.BigEndian.Uint16(b[20:]) {
		t.Error("the same flow got two outside ports")
	}
	pool := natPortHi - natPortLo + 1
	for i := 1; i < pool; i++ {
		c := netip.AddrFrom4([4]byte{192, 168, 137, byte(1 + i%250)})
		p := ipPacket(protoUDP, c, natRemote, uint16(1000+i/250), 53, 0, 0)
		if !tb.out(p) {
			t.Fatalf("flow %d of %d dropped", i, pool)
		}
	}
	if tb.Len() != pool {
		t.Fatalf("%d mappings, want %d", tb.Len(), pool)
	}
	if tb.out(ipPacket(protoUDP, natClient, natRemote, 9, 53, 0, 0)) {
		t.Error("a flow passed with the pool full")
	}
	now = now.Add(NATTimeoutUDP + time.Second)
	if !tb.out(ipPacket(protoUDP, natClient, natRemote, 9, 53, 0, 0)) {
		t.Error("expired ports are not reused")
	}
	if !tb.out(ipPacket(protoTCP, natClient, natRemote, 9, 53, 0x02, 0)) {
		t.Error("TCP has its own pool")
	}
}

// pipeDev is a Device for the wrapper test: Read returns queued packets.
type pipeDev struct {
	Device
	rx      [][]byte
	written [][]byte
}

func (d *pipeDev) Read(p []byte) (int, error) {
	n := copy(p, d.rx[0])
	d.rx = d.rx[1:]
	return n, nil
}

func (d *pipeDev) Write(p []byte) (int, error) {
	d.written = append(d.written, append([]byte(nil), p...))
	return len(p), nil
}

func TestNATDeviceSkipsDropped(t *testing.T) {
	bad := ipPacket(47, natClient, natRemote, 0, 0, 0, 8)
	good := ipPacket(protoUDP, natClient, natRemote, 7000, 53, 0, 4)
	pd := &pipeDev{rx: [][]byte{bad, good}}
	dev := NAT(pd, natInside, natOutside, nil)
	buf := make([]byte, 1500)
	n, err := dev.Read(buf)
	if err != nil || n != len(good) || addrAt(buf, 12) != natOutside {
		t.Fatalf("read %d %v src %v, want the translated UDP packet", n, err, addrAt(buf, 12))
	}
	port := binary.BigEndian.Uint16(buf[20:])
	if _, err := dev.Write(ipPacket(protoUDP, natRemote, natOutside, 53, port, 0, 4)); err != nil {
		t.Fatal(err)
	}
	if addrAt(pd.written[0], 16) != natClient {
		t.Error("write was not translated back")
	}
}

func FuzzNAT(f *testing.F) {
	f.Add(ipPacket(protoTCP, natClient, natRemote, 1, 2, 0x02, 3))
	f.Add(ipPacket(protoUDP, natRemote, natOutside, 1, 10000, 0, 3))
	f.Add(ipPacket(protoICMP, natClient, natRemote, 1, 0, 8, 3))
	f.Add([]byte{0x45, 0, 0, 20})
	f.Fuzz(func(t *testing.T, p []byte) {
		tb := newNATTable(natInside, natOutside, time.Now)
		q := append([]byte(nil), p...)
		if tb.out(q) && len(p) >= 20 && p[0]>>4 == 4 {
			src := addrAt(p, 12)
			if natInside.Contains(src) && addrAt(q, 12) != natOutside {
				t.Fatalf("passed with source %v", addrAt(q, 12))
			}
		}
		tb.in(append([]byte(nil), p...))
	})
}
