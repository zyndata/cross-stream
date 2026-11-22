package tun

import (
	"encoding/binary"
	"net/netip"
	"sync"
	"time"
)

// NAT idle timeouts and the translated port (ICMP: echo id) range. The range
// stays below the Windows ephemeral ports (49152+) the host's own sockets on
// the tunnel address use, so a translated port never meets one of them.
const (
	NATTimeoutTCP     = 2 * time.Hour
	NATTimeoutTCPDone = time.Minute // after a FIN or RST
	NATTimeoutUDP     = 2 * time.Minute
	NATTimeoutICMP    = 30 * time.Second
	natPortLo         = 10000
	natPortHi         = 49151
)

const (
	protoICMP = 1
	protoTCP  = 6
	protoUDP  = 17
)

// NAT wraps dev with a userspace NAPT: IPv4 packets read from the TUN (sent
// by the host into the tunnel) whose source is in inside get the source
// outside:port; packets written to the TUN for outside on a translated port
// get the original client address back. TCP and UDP are mapped by port, ICMP
// echo by id; other packets from inside are dropped (they would leave with a
// private source). Used by the Windows hub when Mobile Hotspot cannot share
// the tunnel itself (Windows 10, decisions.md phase 18). now nil = time.Now.
func NAT(dev Device, inside netip.Prefix, outside netip.Addr, now func() time.Time) Device {
	if now == nil {
		now = time.Now
	}
	return &natDevice{Device: dev, t: newNATTable(inside, outside, now)}
}

type natDevice struct {
	Device
	t *natTable
}

// Read returns the next packet for the tunnel, translated; untranslatable
// packets from inside are skipped.
func (n *natDevice) Read(p []byte) (int, error) {
	for {
		k, err := n.Device.Read(p)
		if err != nil || n.t.out(p[:k]) {
			return k, err
		}
	}
}

// Write translates a packet from the tunnel back to its client.
func (n *natDevice) Write(p []byte) (int, error) {
	n.t.in(p)
	return n.Device.Write(p)
}

type natKey struct {
	proto uint8
	addr  netip.Addr
	port  uint16 // ICMP: echo id
}

type natEntry struct {
	key     natKey // inside address and port
	port    uint16 // outside port
	expires time.Time
	closing bool // TCP: FIN or RST seen
}

type natTable struct {
	mu      sync.Mutex
	inside  netip.Prefix
	outside netip.Addr
	now     func() time.Time
	byIn    map[natKey]*natEntry
	byOut   map[[2]uint16]*natEntry // {proto, outside port}
	next    [256]uint16             // next port to try per protocol
}

func newNATTable(inside netip.Prefix, outside netip.Addr, now func() time.Time) *natTable {
	return &natTable{inside: inside.Masked(), outside: outside, now: now,
		byIn: map[natKey]*natEntry{}, byOut: map[[2]uint16]*natEntry{}}
}

// Len returns the number of live mappings (tests, status).
func (t *natTable) Len() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.byIn)
}

// l4 returns the transport header of pkt, or ok false for a packet NAT
// cannot handle (not IPv4, short, a fragment, another protocol).
func l4(pkt []byte) ([]byte, bool) {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		return nil, false
	}
	ihl := int(pkt[0]&0x0f) * 4
	total := int(binary.BigEndian.Uint16(pkt[2:]))
	if ihl < 20 || total < ihl || total > len(pkt) {
		return nil, false
	}
	if binary.BigEndian.Uint16(pkt[6:])&0x3fff != 0 { // MF set or an offset: a fragment
		return nil, false
	}
	need := 0
	switch pkt[9] {
	case protoTCP:
		need = 20
	case protoUDP, protoICMP:
		need = 8
	}
	if need == 0 || total-ihl < need {
		return nil, false
	}
	return pkt[ihl:total], true
}

// portOff is the offset of the port (ICMP: echo id) to translate in the
// transport header: source for outbound, destination for inbound.
func portOff(proto uint8, outbound bool) int {
	switch {
	case proto == protoICMP:
		return 4
	case outbound:
		return 0
	}
	return 2
}

// csumOff is the transport checksum offset; ok false = none to fix (UDP 0).
func csumOff(proto uint8, h []byte) (int, bool) {
	switch proto {
	case protoTCP:
		return 16, true
	case protoUDP:
		return 6, binary.BigEndian.Uint16(h[6:]) != 0
	}
	return 2, true
}

// isEcho reports whether h is an ICMP echo request (outbound) or reply (inbound).
func isEcho(h []byte, outbound bool) bool {
	if outbound {
		return h[0] == 8 && h[1] == 0
	}
	return h[0] == 0 && h[1] == 0
}

// out translates an outbound packet in place. It returns false when the
// packet must not reach the tunnel (from inside, but untranslatable).
func (t *natTable) out(pkt []byte) bool {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		return true // IPv6 and junk: not ours to judge
	}
	src, _ := netip.AddrFromSlice(pkt[12:16])
	if !t.inside.Contains(src) {
		return true // the host's own traffic
	}
	h, ok := l4(pkt)
	if !ok {
		return false
	}
	proto := pkt[9]
	if proto == protoICMP && !isEcho(h, true) {
		return false
	}
	off := portOff(proto, true)
	key := natKey{proto: proto, addr: src, port: binary.BigEndian.Uint16(h[off:])}
	t.mu.Lock()
	e := t.lookupOut(key)
	if e == nil {
		t.mu.Unlock()
		return false // port pool full
	}
	t.touch(e, proto, h)
	port := e.port
	t.mu.Unlock()
	rewrite(pkt, h, 12, t.outside, off, port)
	return true
}

// in translates an inbound packet in place when it is for a mapping.
func (t *natTable) in(pkt []byte) {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		return
	}
	dst, _ := netip.AddrFromSlice(pkt[16:20])
	if dst != t.outside {
		return
	}
	h, ok := l4(pkt)
	if !ok {
		return
	}
	proto := pkt[9]
	if proto == protoICMP && !isEcho(h, false) {
		return
	}
	off := portOff(proto, false)
	t.mu.Lock()
	e := t.byOut[[2]uint16{uint16(proto), binary.BigEndian.Uint16(h[off:])}]
	if e == nil || !t.now().Before(e.expires) {
		t.mu.Unlock()
		return // the host's own traffic (or a closed mapping)
	}
	t.touch(e, proto, h)
	key := e.key
	t.mu.Unlock()
	rewrite(pkt, h, 16, key.addr, off, key.port)
}

// lookupOut returns the live mapping for key, making one if needed (nil when
// every port is taken). Called with t.mu held.
func (t *natTable) lookupOut(key natKey) *natEntry {
	now := t.now()
	if e := t.byIn[key]; e != nil {
		if now.Before(e.expires) {
			return e
		}
		t.drop(e)
	}
	n := natPortHi - natPortLo + 1
	for range n {
		p := t.next[key.proto]
		if p < natPortLo || p > natPortHi {
			p = natPortLo
		}
		t.next[key.proto] = p + 1
		ok := [2]uint16{uint16(key.proto), p}
		if old := t.byOut[ok]; old != nil {
			if now.Before(old.expires) {
				continue
			}
			t.drop(old)
		}
		e := &natEntry{key: key, port: p}
		t.byIn[key], t.byOut[ok] = e, e
		return e
	}
	return nil
}

func (t *natTable) drop(e *natEntry) {
	delete(t.byIn, e.key)
	delete(t.byOut, [2]uint16{uint16(e.key.proto), e.port})
}

// touch extends e's idle timeout. Once a TCP FIN or RST passed, the mapping
// only lives NATTimeoutTCPDone past the last packet.
func (t *natTable) touch(e *natEntry, proto uint8, h []byte) {
	d := NATTimeoutICMP
	switch proto {
	case protoTCP:
		e.closing = e.closing || h[13]&0x05 != 0
		d = NATTimeoutTCP
		if e.closing {
			d = NATTimeoutTCPDone
		}
	case protoUDP:
		d = NATTimeoutUDP
	}
	e.expires = t.now().Add(d)
}

// rewrite sets the IPv4 address at addrOff (12 = source, 16 = destination)
// and the port at h[portOff], fixing the IP and transport checksums
// incrementally (RFC 1624).
func rewrite(pkt, h []byte, addrOff int, addr netip.Addr, portOff int, port uint16) {
	a := addr.As4()
	var old [4]byte
	copy(old[:], pkt[addrOff:addrOff+4])
	oldPort := binary.BigEndian.Uint16(h[portOff:])
	copy(pkt[addrOff:], a[:])
	binary.BigEndian.PutUint16(h[portOff:], port)
	adjust(pkt[10:12], old[:], a[:])
	proto := pkt[9]
	off, ok := csumOff(proto, h)
	if !ok {
		return
	}
	if proto != protoICMP { // the pseudo-header holds the addresses
		adjust(h[off:off+2], old[:], a[:])
	}
	var op, np [2]byte
	binary.BigEndian.PutUint16(op[:], oldPort)
	binary.BigEndian.PutUint16(np[:], port)
	adjust(h[off:off+2], op[:], np[:])
	if proto == protoUDP && binary.BigEndian.Uint16(h[off:]) == 0 {
		binary.BigEndian.PutUint16(h[off:], 0xffff) // 0 means "no checksum"
	}
}

// adjust updates the 16-bit one's complement checksum at sum for the bytes
// old replaced by nw (same even length): HC' = ~(~HC + ~m + m').
func adjust(sum, old, nw []byte) {
	s := uint32(^binary.BigEndian.Uint16(sum))
	for i := 0; i+1 < len(old); i += 2 {
		s += uint32(^binary.BigEndian.Uint16(old[i:])) + uint32(binary.BigEndian.Uint16(nw[i:]))
	}
	for s>>16 != 0 {
		s = s&0xffff + s>>16
	}
	binary.BigEndian.PutUint16(sum, ^uint16(s))
}
