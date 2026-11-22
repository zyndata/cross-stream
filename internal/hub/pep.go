package hub

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"strings"

	"github.com/zyndata/cross-stream/internal/direct"
)

// PEP settings values (architecture.md "Performance Enhancing Proxy — PEP").
const (
	PEPOff = "off"
	PEPOn  = "on"
	// DefaultPEP is the setting before the owner changes it: on since the
	// phase 18 home measurement (decisions.md); a saved setting is kept.
	DefaultPEP = PEPOn
	// PEPRelayID is the netstack relay id of PEP flows.
	PEPRelayID = "pep"
)

// tunnelNet is the server's tunnel network (server.TunnelPrefix): hub-to-hub
// traffic stays in packet mode.
var tunnelNet = netip.MustParsePrefix("100.64.0.0/16")

// PEPStatus is the PEP part of Status.
type PEPStatus struct {
	On        bool `json:"on"`        // setting
	Available bool `json:"available"` // the server accepts streams
	Streams   int  `json:"streams"`   // open streams
	Flows     int  `json:"flows"`     // client TCP connections in the netstack
	Buffered  int  `json:"buffered"`  // bytes from the server not yet written to clients
	Backlog   int  `json:"backlog"`   // bytes from clients not yet sent
}

// pepRules are the parsed PEP exclusions: destination ports (ranges) and
// IPv4 networks whose TCP flows stay in packet mode.
type pepRules struct {
	ports [][2]uint16
	nets  []netip.Prefix
}

// parsePEPExclude parses exclusions: "22", "5000-5100", "10.0.0.0/8" or
// "192.0.2.7".
func parsePEPExclude(list []string) (pepRules, error) {
	var r pepRules
	for _, s := range list {
		s = strings.TrimSpace(s)
		if p, err := netip.ParsePrefix(s); err == nil && p.Addr().Is4() {
			r.nets = append(r.nets, p.Masked())
			continue
		}
		if a, err := netip.ParseAddr(s); err == nil && a.Is4() {
			r.nets = append(r.nets, netip.PrefixFrom(a, 32))
			continue
		}
		lo, hi, isRange := strings.Cut(s, "-")
		if !isRange {
			hi = lo
		}
		a, errA := strconv.ParseUint(lo, 10, 16)
		b, errB := strconv.ParseUint(hi, 10, 16)
		if errA != nil || errB != nil || a == 0 || b < a {
			return pepRules{}, fmt.Errorf("pep exclusion %q: want a port, a port range a-b or an IPv4 address/network", s)
		}
		r.ports = append(r.ports, [2]uint16{uint16(a), uint16(b)})
	}
	return r, nil
}

// excluded reports whether flows to dst stay in packet mode.
func (r *pepRules) excluded(dst netip.AddrPort) bool {
	a := dst.Addr()
	if !a.IsGlobalUnicast() || tunnelNet.Contains(a) {
		return true
	}
	for _, p := range r.ports {
		if dst.Port() >= p[0] && dst.Port() <= p[1] {
			return true
		}
	}
	for _, n := range r.nets {
		if n.Contains(a) {
			return true
		}
	}
	return false
}

// tcpSYN returns the destination of an IPv4 TCP SYN (no ACK): the first
// packet of a new connection.
func tcpSYN(pkt []byte) (netip.AddrPort, bool) {
	if len(pkt) < 20 || pkt[0]>>4 != 4 || pkt[9] != 6 || binary.BigEndian.Uint16(pkt[6:8])&0x3fff != 0 {
		return netip.AddrPort{}, false
	}
	ihl := int(pkt[0]&0x0f) * 4
	if ihl < 20 || len(pkt) < ihl+14 || pkt[ihl+13]&0x12 != 0x02 {
		return netip.AddrPort{}, false
	}
	return netip.AddrPortFrom(netip.AddrFrom4([4]byte(pkt[16:20])), binary.BigEndian.Uint16(pkt[ihl+2:])), true
}

// pepActive reports whether new TCP flows go through PEP now: the setting
// is on, the server accepts streams and the hub is not in DIRECT mode.
func (d *Daemon) pepActive() bool {
	if s := d.settings.Load(); s == nil || s.PEP != PEPOn || d.route.Load() == routeDirect {
		return false
	}
	c := d.client.Load()
	return c != nil && c.PEP() != nil
}

// pepPacket reports whether a TUN packet starts a PEP flow (it then goes to
// the netstack, which claims the flow with pepIntercept). Packets of flows
// already in the netstack are caught before (Owns); every other packet of
// a packet-mode flow keeps going to the tunnel, so toggling PEP leaves
// running flows in their mode.
func (d *Daemon) pepPacket(pkt []byte) bool {
	dst, ok := tcpSYN(pkt)
	if !ok || !d.pepActive() || d.bp.Contains(dst.Addr()) {
		return false
	}
	r := d.pepRules.Load()
	return r == nil || !r.excluded(dst)
}

// pepIntercept is the netstack's Intercept hook: a new TCP flow that is not
// a bypass flow becomes a PEP stream while PEP is active.
func (d *Daemon) pepIntercept(k direct.FlowKey) (direct.Relay, bool) {
	if d.bp.Contains(k.Dst.Addr()) || !d.pepActive() {
		return direct.Relay{}, false
	}
	if r := d.pepRules.Load(); r != nil && r.excluded(k.Dst) {
		return direct.Relay{}, false
	}
	dial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		c := d.client.Load()
		if c == nil {
			return nil, fmt.Errorf("pep: no tunnel")
		}
		ps := c.PEP()
		if ps == nil {
			return nil, fmt.Errorf("pep: server does not accept streams")
		}
		return ps.Open(ctx, k.Dst)
	}
	return direct.Relay{ID: PEPRelayID, Dial: dial}, true
}

// pepStatus returns the PEP part of Status.
func (d *Daemon) pepStatus() *PEPStatus {
	st := &PEPStatus{}
	if s := d.settings.Load(); s != nil {
		st.On = s.PEP == PEPOn
	}
	if c := d.client.Load(); c != nil {
		if ps := c.PEP(); ps != nil {
			x := ps.Stats()
			st.Available, st.Streams, st.Buffered, st.Backlog = true, x.Streams, x.Buffered, x.Backlog
		}
	}
	if ds := d.directStack(); ds != nil {
		st.Flows = ds.Stats().Intercepted
	}
	return st
}
