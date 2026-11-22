package server

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/zyndata/cross-stream/internal/pep"
)

// OriginKeepAlive is the TCP keepalive of PEP origin connections
// (architecture.md: idle streams live up to 2 h).
const OriginKeepAlive = 30 * time.Second

// errPEPDst is a PEP destination the server never dials.
var errPEPDst = errors.New("server: pep destination not allowed")

// pepAllowed reports whether a hub may open a PEP stream to dst: unicast
// IPv4 the egress filter allows, not the server's loopback, the tunnel network
// (hub-to-hub stays in packet mode), multicast or broadcast.
func pepAllowed(e Egress, dst netip.AddrPort) bool {
	a := dst.Addr()
	return a.Is4() && dst.Port() != 0 && !a.IsLoopback() && !a.IsUnspecified() && !a.IsMulticast() &&
		a != netip.AddrFrom4([4]byte{255, 255, 255, 255}) && !TunnelPrefix.Contains(a) && e.Allowed(ProtoTCP, dst)
}

// ownAddr reports whether a is one of this host's interface addresses. A
// PEP stream to it would be a local connection with a server source
// address, past the packet filter and the panel's tunnel-prefix check.
func ownAddr(a netip.Addr) bool {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return false
	}
	for _, ia := range addrs {
		if p, err := netip.ParsePrefix(ia.String()); err == nil && p.Addr().Unmap() == a {
			return true
		}
	}
	return false
}

// dialOrigin connects a PEP stream to its destination. The socket is an
// ordinary one of the server, so the origin sees the same address as
// masqueraded packet-mode traffic; congestion control is BBR where the
// kernel has it (decisions.md phase 13).
func (s *Server) dialOrigin(ctx context.Context, dst netip.AddrPort) (net.Conn, error) {
	if s.o.DialOrigin != nil {
		return s.o.DialOrigin(ctx, dst)
	}
	if !pepAllowed(s.egress, dst) || ownAddr(dst.Addr()) {
		return nil, fmt.Errorf("%w: %s", errPEPDst, dst)
	}
	d := net.Dialer{KeepAlive: OriginKeepAlive, Control: tcpBBR}
	c, err := d.DialContext(ctx, "tcp4", dst.String())
	if err != nil {
		return nil, fmt.Errorf("dial origin %s: %w", dst, err)
	}
	return c, nil
}

// servePEP splices an opened stream with its origin connection.
func (s *Server) servePEP(st *pep.Stream, conn net.Conn) {
	abort := func() {
		if tc, ok := conn.(*net.TCPConn); ok {
			_ = tc.SetLinger(0) // RST to the origin
		}
		_ = conn.Close()
	}
	pep.Pipe(st, conn, abort)
}
