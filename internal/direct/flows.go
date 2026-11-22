package direct

import (
	"encoding/binary"
	"net/netip"
	"sync/atomic"
	"time"
)

// IP protocol numbers of proxied flows.
const (
	protoTCP = 6
	protoUDP = 17
)

// FlowKey identifies a flow by the client's packets: protocol, source and
// destination (after the hub's masquerade: source = tunnel address).
type FlowKey struct {
	Proto uint8
	Src   netip.AddrPort
	Dst   netip.AddrPort
}

// flow is one proxied TCP connection or UDP flow.
type flow struct {
	key   FlowKey
	relay Relay
	id    uint32 // UDP flow id on the data channel
	start time.Time
	last  atomic.Int64 // unix nanoseconds of the last datagram (UDP)
	// abort ends the flow at once (TCP: RST to the client); set once the
	// flow is established, nil while it is being set up.
	abort func()
	udp   udpWriter
	// intercepted: claimed by Config.Intercept (PEP), not a DIRECT flow
	intercepted bool
}

// ParseKey returns the flow key of an IPv4 TCP or UDP packet. Fragments
// after the first and other protocols return false.
func ParseKey(pkt []byte) (FlowKey, bool) {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		return FlowKey{}, false
	}
	ihl := int(pkt[0]&0x0f) * 4
	proto := pkt[9]
	if (proto != protoTCP && proto != protoUDP) || ihl < 20 || len(pkt) < ihl+4 ||
		binary.BigEndian.Uint16(pkt[6:8])&0x1fff != 0 {
		return FlowKey{}, false
	}
	src := netip.AddrFrom4([4]byte(pkt[12:16]))
	dst := netip.AddrFrom4([4]byte(pkt[16:20]))
	return FlowKey{
		Proto: proto,
		Src:   netip.AddrPortFrom(src, binary.BigEndian.Uint16(pkt[ihl:])),
		Dst:   netip.AddrPortFrom(dst, binary.BigEndian.Uint16(pkt[ihl+2:])),
	}, true
}

// Stats counts the open DIRECT flows.
type Stats struct {
	TCP         int            `json:"tcp"`
	UDP         int            `json:"udp"`
	Relays      map[string]int `json:"relays"`      // DIRECT flows per relay id
	Intercepted int            `json:"intercepted"` // TCP flows claimed by Config.Intercept
}

// Stats returns the open flow counts.
func (s *Stack) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Stats{Relays: make(map[string]int, len(s.perRelay))}
	for k, f := range s.flows {
		switch {
		case f.intercepted:
			st.Intercepted++
			continue
		case s.perRelay[f.relay.ID] > 0:
			st.Relays[f.relay.ID] = s.perRelay[f.relay.ID]
		}
		if k.Proto == protoTCP {
			st.TCP++
		} else {
			st.UDP++
		}
	}
	return st
}

// Owns reports whether pkt belongs to an open DIRECT flow (draining: such
// packets stay on the netstack, every other packet goes to the tunnel).
func (s *Stack) Owns(pkt []byte) bool {
	k, ok := ParseKey(pkt)
	if !ok {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok = s.flows[k]
	return ok
}

// OwnsIntercepted reports whether pkt belongs to an open intercepted (PEP)
// flow: such packets must reach the netstack whatever the bypass state.
func (s *Stack) OwnsIntercepted(pkt []byte) bool {
	if s.icp.Load() == 0 {
		return false
	}
	k, ok := ParseKey(pkt)
	if !ok || k.Proto != protoTCP {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	f := s.flows[k]
	return f != nil && f.intercepted
}

// add assigns a new flow to a relay and registers it; false when no relay
// is available or the flow exists.
func (s *Stack) add(k FlowKey) (*flow, bool) {
	var ir Relay
	icp := false
	if s.cfg.Intercept != nil && k.Proto == protoTCP {
		ir, icp = s.cfg.Intercept(k)
	}
	var relays []Relay
	if !icp {
		relays = s.cfg.Relays()
	}
	now := s.cfg.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, dup := s.flows[k]; dup || s.closed {
		return nil, false
	}
	r, ok := ir, icp
	if !icp {
		r, ok = s.sel.pick(now, k.Dst.Addr(), relays, s.perRelay)
	}
	if !ok {
		return nil, false
	}
	f := &flow{key: k, relay: r, start: now, intercepted: icp}
	f.last.Store(now.UnixNano())
	if k.Proto == protoUDP {
		s.nextID++
		f.id = s.nextID
		s.byID[f.id] = f
	}
	s.flows[k] = f
	s.perRelay[r.ID]++
	if icp {
		s.icp.Add(1)
	}
	return f, true
}

// remove unregisters f (idempotent).
func (s *Stack) remove(f *flow) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.flows[f.key] != f {
		return
	}
	s.forgetLocked(f)
}

// forgetLocked drops f from the tables. Caller holds s.mu.
func (s *Stack) forgetLocked(f *flow) {
	delete(s.flows, f.key)
	if f.id != 0 {
		delete(s.byID, f.id)
	}
	if s.perRelay[f.relay.ID]--; s.perRelay[f.relay.ID] <= 0 {
		delete(s.perRelay, f.relay.ID)
	}
	if f.intercepted {
		s.icp.Add(-1)
	}
}

// setAbort records how to end f; if f was reset meanwhile it is ended now.
func (s *Stack) setAbort(f *flow, abort func()) bool {
	s.mu.Lock()
	live := s.flows[f.key] == f
	if live {
		f.abort = abort
	}
	s.mu.Unlock()
	if !live {
		abort()
	}
	return live
}

// ResetRelay ends every flow of a relay (it died: flows cannot move) and
// returns how many there were.
func (s *Stack) ResetRelay(id string) int {
	return s.reset(func(f *flow) bool { return f.relay.ID == id })
}

// ResetAll ends every DIRECT flow (drain over); intercepted flows stay.
func (s *Stack) ResetAll() int { return s.reset(func(f *flow) bool { return !f.intercepted }) }

// ResetExcept ends every DIRECT flow whose destination keep does not accept
// (bypass flows outlive a DIRECT drain) and returns how many were ended.
func (s *Stack) ResetExcept(keep func(dst netip.Addr) bool) int {
	return s.reset(func(f *flow) bool { return !f.intercepted && !keep(f.key.Dst.Addr()) })
}

func (s *Stack) reset(match func(*flow) bool) int {
	s.mu.Lock()
	var aborts []func()
	n := 0
	for _, f := range s.flows {
		if !match(f) {
			continue
		}
		n++
		s.forgetLocked(f)
		if f.abort != nil {
			aborts = append(aborts, f.abort)
		}
	}
	s.mu.Unlock()
	for _, a := range aborts {
		a()
	}
	return n
}
