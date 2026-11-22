package hub

import (
	"context"
	"errors"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/zyndata/cross-stream/internal/datacap"
	"github.com/zyndata/cross-stream/internal/hub/uplink"
	"github.com/zyndata/cross-stream/internal/transport"
	"github.com/zyndata/cross-stream/internal/wire"
)

// Local path redial backoff: from LocalRetry doubling to MaxLocalBackoff.
const (
	LocalRetry      = time.Second
	MaxLocalBackoff = 30 * time.Second
)

var (
	errLocalDown = errors.New("hub: local uplink not connected")
	errOverCap   = errors.New("hub: local uplink over its data cap")
)

// localPath is a hub interface used as a bond path: outer packets go to the
// server on a socket bound to the interface (SO_BINDTODEVICE), no relay hop.
type localPath struct {
	d     *Daemon
	id    string // link id (u1, …)
	pid   uint8
	iface string
	kind  uplink.Kind
	addr  netip.AddrPort // the path's address in the session (fixed, synthetic)
	meter *datacap.Meter
	since time.Time

	conn    atomic.Pointer[transport.Conn]
	up      atomic.Bool   // connected and added to the session
	tx, rx  atomic.Uint64 // outer bytes since start
	kick    chan struct{} // endpoint changed: redial
	cancel  context.CancelFunc
	done    chan struct{}
	stopWhy atomic.Pointer[string] // why the manager stopped it (event reason)

	// manager (uplinkTick) only
	usedTx, usedRx uint64    // tx/rx already counted in the usage history
	badSince       time.Time // first tick the path was not alive (zero = alive)
	failProbed     bool      // probed for a captive portal since it was last alive

	epTrack // guarded by Daemon.mu
}

// PathID implements PathConn.
func (lp *localPath) PathID() uint8 { return lp.pid }

func (lp *localPath) fallbackKey() string { return lp.id }
func (lp *localPath) track() *epTrack     { return &lp.epTrack }
func (lp *localPath) endpointMoved()      { lp.redial() }

// redial makes the path reconnect to its (new) endpoint.
func (lp *localPath) redial() {
	select {
	case lp.kick <- struct{}{}:
	default:
	}
	if c := lp.conn.Load(); c != nil {
		_ = (*c).Close()
	}
}

// WriteToUDPAddrPort implements session.Sender; the address is ignored (the
// connection goes to the path's server endpoint).
func (lp *localPath) WriteToUDPAddrPort(b []byte, _ netip.AddrPort) (int, error) {
	c := lp.conn.Load()
	if c == nil {
		return 0, errLocalDown
	}
	if !lp.meter.Allow(len(b)) {
		return 0, errOverCap
	}
	if err := (*c).Send(b); err != nil {
		return 0, err
	}
	lp.tx.Add(uint64(len(b)))
	return len(b), nil
}

// control binds a socket to the path's interface (DF on UDP).
func (lp *localPath) control() func(network, address string, c syscall.RawConn) error {
	return lp.d.cfg.UplinkControl(lp.iface)
}

// dialDirect dials a DIRECT flow destination through the interface; its
// bytes count against the link's cap.
func (lp *localPath) dialDirect(ctx context.Context, network, addr string) (net.Conn, error) {
	nd := net.Dialer{Control: lp.control(), Timeout: 10 * time.Second}
	c, err := nd.DialContext(ctx, network, addr)
	if err != nil {
		return nil, err
	}
	return &countedConn{Conn: c, m: lp.meter}, nil
}

// countedConn counts a DIRECT flow's bytes against a link's cap.
type countedConn struct {
	net.Conn
	m *datacap.Meter
}

func (c *countedConn) Read(b []byte) (int, error) {
	n, err := c.Conn.Read(b)
	c.m.Add(n)
	return n, err
}

func (c *countedConn) Write(b []byte) (int, error) {
	n, err := c.Conn.Write(b)
	c.m.Add(n)
	return n, err
}

// CloseWrite half-closes a TCP flow (direct splice).
func (c *countedConn) CloseWrite() error {
	if hc, ok := c.Conn.(interface{ CloseWrite() error }); ok {
		return hc.CloseWrite()
	}
	return c.Close()
}

// run dials the server endpoint and forwards what it sends to the session
// until ctx ends, redialing with backoff.
func (lp *localPath) run(ctx context.Context) {
	defer close(lp.done)
	backoff := LocalRetry
	for ctx.Err() == nil {
		lp.d.mu.Lock()
		uri := lp.ep
		lp.d.mu.Unlock()
		ep, err := wire.ParseEndpoint(uri)
		if uri == "" || err != nil {
			lp.wait(ctx, MaxLocalBackoff) // not enrolled yet: wait for an endpoint
			continue
		}
		dl := transport.Dialer{Control: lp.control(), StreamConns: lp.tcpConns()}
		conn, err := dl.Dial(ctx, ep)
		if err != nil {
			if ctx.Err() == nil {
				lp.d.log.Debug("local uplink dial", "uplink", lp.id, "iface", lp.iface, "ep", uri, "err", err)
			}
			if !lp.wait(ctx, backoff) {
				backoff = min(2*backoff, MaxLocalBackoff)
			}
			continue
		}
		lp.d.mu.Lock()
		moved := lp.ep != uri
		lp.d.mu.Unlock()
		if moved { // the endpoint changed while dialling: do not serve the old one
			_ = conn.Close()
			select {
			case <-lp.kick:
			default:
			}
			continue
		}
		started := time.Now()
		lp.serve(ctx, conn, uri)
		if time.Since(started) >= stableLocal {
			backoff = LocalRetry
		}
		if ctx.Err() == nil && !lp.wait(ctx, backoff) {
			backoff = min(2*backoff, MaxLocalBackoff)
		}
	}
}

// tickConns feeds the connection set's counters to the policy and resizes
// the set when the choice changes (manager only).
func (lp *localPath) tickConns(now time.Time) {
	c := lp.conn.Load()
	if c == nil {
		return
	}
	ctr, ok := (*c).(transport.Counter)
	if !ok {
		return
	}
	tc := ctr.TCPCounters()
	if n, changed := lp.observeConns(now, tc); changed {
		lp.d.log.Info("local uplink tcp connections", "uplink", lp.id, "tcp_conns", n, "retrans", tc.Retrans, "sent", tc.Sent)
		if rz, ok := (*c).(transport.Resizer); ok {
			rz.SetConns(n)
		}
	}
}

// liveConns is the live number of TCP connections (0 = not a tcp:// path).
func (lp *localPath) liveConns() int {
	if c := lp.conn.Load(); c != nil {
		if ctr, ok := (*c).(transport.Counter); ok {
			return ctr.TCPCounters().Conns
		}
	}
	return 0
}

// stableLocal resets the backoff: a connection that lived this long was not
// a failed attempt.
const stableLocal = 30 * time.Second

// wait sleeps for d; true when woken early by a kick (new endpoint).
func (lp *localPath) wait(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
	case <-lp.kick:
		return true
	case <-t.C:
	}
	return false
}

// serve makes conn the path's connection and reads from it until it fails.
func (lp *localPath) serve(ctx context.Context, conn transport.Conn, uri string) {
	var once sync.Once
	closeConn := func() { once.Do(func() { _ = conn.Close() }) }
	stop := context.AfterFunc(ctx, closeConn)
	defer stop()
	defer closeConn()
	lp.conn.Store(&conn)
	lp.d.localUp(lp)
	lp.d.log.Info("local uplink connected", "uplink", lp.id, "iface", lp.iface, "ep", uri)
	defer func() {
		lp.conn.Store(nil)
		lp.d.localDown(lp, ctx.Err() != nil)
	}()
	buf := make([]byte, transport.MaxPacket)
	for {
		n, err := conn.Recv(buf)
		if err != nil {
			if ctx.Err() == nil && !errors.Is(err, net.ErrClosed) {
				lp.d.log.Debug("local uplink receive", "uplink", lp.id, "err", err)
			}
			return
		}
		if n < wire.HeaderLen || !lp.meter.Allow(n) {
			continue
		}
		lp.rx.Add(uint64(n))
		if c := lp.d.client.Load(); c != nil {
			c.HandlePacket(buf[:n], lp.addr, lp)
		}
	}
}
