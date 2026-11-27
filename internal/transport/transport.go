// Package transport carries outer packets between a relay and the server over
// UDP, TCP or WebSocket-over-TLS (architecture.md "Server transports").
// Stream transports frame each packet as `len u16 | packet` (TCP) or one
// binary WebSocket message per packet (WSS).
package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"syscall"
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

// MaxPacket is the largest outer packet a stream transport carries.
const MaxPacket = 0xFFFF

// SendQueue is how many packets a stream connection buffers before it drops
// (a stalled TCP connection must not block the bond engine). Senders pick
// the connection with the shorter queue (Backlogger), so a stalled one's
// queue stops growing long before it is full; 256 measured no better in
// e2e tcploss and cost one connection ~1 Mb/s at 0 % loss (2026-11-27).
const SendQueue = 1024

// Errors.
var (
	// ErrResolve marks a failed name resolution (worth retrying later).
	ErrResolve = errors.New("transport: resolve")
	// ErrQueueFull is returned when a stream connection's send queue is full.
	ErrQueueFull = errors.New("transport: send queue full")
)

// Conn is a client connection to one server endpoint. Send and Recv may be
// used concurrently; Recv returns one outer packet per call.
type Conn interface {
	Send(pkt []byte) error
	Recv(buf []byte) (int, error)
	Close() error
	Transport() wire.Transport
}

// Backlogger is a stream connection that reports how many packets wait in
// its send queue (a stalled TCP connection's grows; senders prefer a shorter
// one, 2026-11-27).
type Backlogger interface{ Backlog() int }

// Lookup resolves a host name to IPv4 addresses.
type Lookup func(ctx context.Context, network, host string) ([]netip.Addr, error)

// Dialer opens client connections.
type Dialer struct {
	// Control runs on every new socket before it connects (relay: bind to
	// the cellular network). network is "udp4" or "tcp4".
	Control func(network, address string, c syscall.RawConn) error
	// Lookup resolves names (default net.DefaultResolver.LookupNetIP).
	Lookup Lookup
	// Timeout bounds name resolution, TCP connect and the TLS/WebSocket
	// handshake (default 10 s).
	Timeout time.Duration
	// StreamConns is the number of TCP connections a tcp:// endpoint is
	// spread over (0 = 1, at most MaxStreamConns); the returned Conn
	// implements Resizer and Counter.
	StreamConns int
}

// Resolve returns the IPv4 address and port of ep.
func (d Dialer) Resolve(ctx context.Context, ep wire.Endpoint) (netip.AddrPort, error) {
	if a, err := netip.ParseAddr(ep.Host); err == nil {
		return netip.AddrPortFrom(a.Unmap(), ep.Port), nil
	}
	lookup := d.Lookup
	if lookup == nil {
		lookup = net.DefaultResolver.LookupNetIP
	}
	ctx, cancel := context.WithTimeout(ctx, d.timeout())
	defer cancel()
	ips, err := lookup(ctx, "ip4", ep.Host)
	if err != nil || len(ips) == 0 {
		return netip.AddrPort{}, fmt.Errorf("%w %s: %v", ErrResolve, ep.Host, err)
	}
	return netip.AddrPortFrom(ips[0].Unmap(), ep.Port), nil
}

func (d Dialer) timeout() time.Duration {
	if d.Timeout > 0 {
		return d.Timeout
	}
	return 10 * time.Second
}

// Dial connects to ep.
func (d Dialer) Dial(ctx context.Context, ep wire.Endpoint) (Conn, error) {
	addr, err := d.Resolve(ctx, ep)
	if err != nil {
		return nil, err
	}
	nd := net.Dialer{Control: d.Control, Timeout: d.timeout()}
	switch ep.Transport {
	case wire.TransportUDP:
		c, err := nd.DialContext(ctx, "udp4", addr.String())
		if err != nil {
			return nil, fmt.Errorf("transport: dial %s: %w", ep, err)
		}
		return &udpConn{c: c.(*net.UDPConn)}, nil
	case wire.TransportTCP:
		n := max(d.StreamConns, 1)
		if n > MaxStreamConns {
			return nil, errStreamConns
		}
		c, err := dialSet(ctx, n, func(ctx context.Context) (*tcpConn, error) {
			c, err := nd.DialContext(ctx, "tcp4", addr.String())
			if err != nil {
				return nil, err
			}
			tuneTCP(c)
			return newTCPConn(c), nil
		})
		if err != nil {
			return nil, fmt.Errorf("transport: dial %s: %w", ep, err)
		}
		return c, nil
	case wire.TransportWSS:
		return dialWSS(ctx, nd, addr, ep, d.timeout())
	}
	return nil, fmt.Errorf("transport: %q: %w", ep.Transport, wire.ErrEndpoint)
}

// udpConn is a connected UDP socket: the kernel drops datagrams from other
// sources.
type udpConn struct{ c *net.UDPConn }

func (u *udpConn) Send(pkt []byte) error {
	_, err := u.c.Write(pkt)
	return err
}

// Recv returns the next datagram. Errors other than a closed socket (ICMP
// errors reported on the connected socket while the server or its network
// is away) are skipped: a UDP "connection" has nothing to reconnect, and
// ending it would make the caller redial with a growing backoff.
func (u *udpConn) Recv(buf []byte) (int, error) {
	for {
		n, err := u.c.Read(buf)
		if err == nil || errors.Is(err, net.ErrClosed) {
			return n, err
		}
		if !isTransient(err) {
			time.Sleep(udpErrorPause) // unknown error: do not spin
		}
	}
}

// udpErrorPause throttles a UDP socket that keeps reporting errors.
const udpErrorPause = 20 * time.Millisecond

func (u *udpConn) Close() error              { return u.c.Close() }
func (u *udpConn) Transport() wire.Transport { return wire.TransportUDP }
