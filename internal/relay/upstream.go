package relay

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/transport"
	"github.com/zyndata/cross-stream/internal/wire"
)

// Reconnect backoff for the server connection: exponential from EndpointRetry
// to MaxEndpointBackoff (MaxResolveBackoff while the name does not resolve,
// so a relay comes back soon after DNS does).
const (
	EndpointRetry      = time.Second
	MaxEndpointBackoff = 5 * time.Minute
	MaxResolveBackoff  = 30 * time.Second
	// stableConn resets the backoff: a connection that lived this long
	// was not a failed attempt.
	stableConn = 30 * time.Second
)

// upstream is the current connection to the server endpoint.
type upstream struct {
	uri  string
	conn transport.Conn
}

// useEndpoint switches to the server endpoint the hub sent: the previous
// connection is closed and a new one is dialed (and redialed with backoff
// while it fails) until the hub sends another endpoint or the link ends.
// Until it is up, hub packets are dropped.
func (l *link) useEndpoint(ctx context.Context, uri string) {
	ep, err := wire.ParseEndpoint(uri)
	if err != nil {
		l.r.cfg.Log.Warn("server endpoint", "ep", uri, "err", err)
		return
	}
	ectx, cancel := context.WithCancel(ctx)
	l.epMu.Lock()
	if l.epCancel != nil {
		l.epCancel()
	}
	l.epCancel, l.epURI = cancel, uri
	l.epMu.Unlock()
	go l.serveEndpoint(ectx, ep)
}

// onEndpoint handles an endpoint message: the endpoint in use with another
// number of TCP connections resizes the live connection set; anything else
// (a new endpoint, or the same one again: the hub's fallback kicks a redial
// that way) restarts the dial loop.
func (l *link) onEndpoint(ctx context.Context, m *control.Endpoint) {
	old := l.conns.Swap(int32(m.TCPConns))
	l.epMu.Lock()
	same := l.epURI == m.ServerEP
	l.epMu.Unlock()
	if u := l.up.Load(); same && old != int32(m.TCPConns) && u != nil {
		if rz, ok := u.conn.(transport.Resizer); ok {
			n := l.tcpConns()
			rz.SetConns(n)
			l.r.cfg.Log.Info("server connections", "ep", u.uri, "tcp_conns", n)
			return
		}
	}
	l.useEndpoint(ctx, m.ServerEP)
}

// tcpConns is the number of TCP connections for a tcp:// endpoint: the
// local override (tests), else the hub's choice (an older hub sends none: 1).
func (l *link) tcpConns() int {
	if n := l.r.cfg.TCPConns; n > 0 {
		return n
	}
	return max(int(l.conns.Load()), 1)
}

// dialer binds every server socket to cellular; only UDP gets DF (TCP does
// its own path MTU discovery).
func (l *link) dialer() transport.Dialer {
	b := l.r.cfg.Binder
	return transport.Dialer{
		Control: func(network, address string, c syscall.RawConn) error {
			return bindFunc(b, NetCellular, strings.HasPrefix(network, "udp"))(network, address, c)
		},
		Lookup:      l.r.cfg.lookup,
		StreamConns: l.tcpConns(),
	}
}

func (l *link) serveEndpoint(ctx context.Context, ep wire.Endpoint) {
	backoff := EndpointRetry
	if l.r.cfg.endpointRetry > 0 {
		backoff = l.r.cfg.endpointRetry
	}
	first := backoff
	for {
		conn, err := l.dialer().Dial(ctx, ep)
		if err == nil {
			started := time.Now()
			l.runUpstream(ctx, &upstream{uri: ep.String(), conn: conn})
			if time.Since(started) >= stableConn {
				backoff = first
			}
		}
		if ctx.Err() != nil {
			return
		}
		limit := MaxEndpointBackoff
		if errors.Is(err, transport.ErrResolve) {
			limit = MaxResolveBackoff
		}
		backoff = min(backoff, limit)
		l.r.cfg.Log.Warn("server connection, retrying", "ep", ep.String(), "in", backoff, "err", err)
		select {
		case <-ctx.Done():
			return
		case <-l.done:
			return
		case <-time.After(backoff):
		}
		backoff = min(2*backoff, limit)
	}
}

// runUpstream makes u the current server connection and forwards what the
// server sends until the connection fails or ctx ends.
func (l *link) runUpstream(ctx context.Context, u *upstream) {
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
		case <-l.done:
		case <-stop:
		}
		_ = u.conn.Close()
	}()
	if ctx.Err() != nil {
		return
	}
	l.up.Store(u)
	defer l.up.CompareAndSwap(u, nil)
	l.r.cfg.Log.Info("server endpoint set", "ep", u.uri)
	l.fromServer(u)
}

// fromServer wraps packets from the server connection as [B2][path][outer]
// and sends them to the hub.
func (l *link) fromServer(u *upstream) {
	buf := make([]byte, transport.MaxPacket+wire.RelayPathHeaderLen)
	buf[0], buf[1] = byte(wire.RelayFromSrv), l.welcome.PathID
	for {
		n, err := u.conn.Recv(buf[wire.RelayPathHeaderLen:])
		if err != nil {
			if !errors.Is(err, net.ErrClosed) {
				l.r.cfg.Log.Debug("server connection", "ep", u.uri, "err", err)
			}
			return
		}
		if n < wire.HeaderLen || !l.r.meter.Allow(n) {
			l.r.dropped.Add(1)
			continue
		}
		if _, err := l.wifi.WriteToUDPAddrPort(buf[:wire.RelayPathHeaderLen+n], l.hub); err != nil {
			l.r.cfg.Log.Debug("to hub", "err", err)
			continue
		}
		l.r.down.Add(uint64(n))
	}
}
