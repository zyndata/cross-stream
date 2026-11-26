package relay

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/transport"
	"github.com/zyndata/cross-stream/internal/wire"
)

// link is one accepted hub session: the control connection plus a Wi-Fi
// socket (to the hub) and a cellular socket (to the server).
type link struct {
	r       *Relay
	ctl     *control.Conn
	welcome control.Welcome
	hub     netip.AddrPort // hub data socket: control peer IP + data_port
	wifi    *net.UDPConn
	up      atomic.Pointer[upstream] // server connection, nil while down
	proxy   atomic.Pointer[proxy]    // DIRECT-mode proxy role, nil while off
	done    chan struct{}            // closed by close()

	conns atomic.Int32 // TCP connections per tcp:// endpoint, from the hub

	epMu     sync.Mutex
	epCancel context.CancelFunc // ends the current endpoint's dial loop
	epURI    string             // the endpoint the hub gave last
}

func (r *Relay) openLink(ctx context.Context, ctl *control.Conn, w control.Welcome) (*link, error) {
	ta, ok := ctl.RemoteAddr().(*net.TCPAddr)
	if !ok {
		return nil, fmt.Errorf("relay: control peer address %v", ctl.RemoteAddr())
	}
	hubIP := ta.AddrPort().Addr().Unmap()
	l := &link{r: r, ctl: ctl, welcome: w, hub: netip.AddrPortFrom(hubIP, uint16(w.DataPort)), done: make(chan struct{})}
	var err error
	if l.wifi, err = listen(ctx, r.cfg.Binder, NetWiFi, r.cfg.ListenIP); err != nil {
		return nil, err
	}
	l.conns.Store(int32(w.TCPConns))
	if w.ServerEP != "" {
		l.useEndpoint(ctx, w.ServerEP)
	}
	return l, nil
}

func listen(ctx context.Context, b Binder, n Network, ip netip.Addr) (*net.UDPConn, error) {
	lc := net.ListenConfig{Control: controlFunc(b, n)}
	pc, err := lc.ListenPacket(ctx, "udp4", listenAddr(ip, 0))
	if err != nil {
		return nil, fmt.Errorf("relay %s socket: %w", n, err)
	}
	return pc.(*net.UDPConn), nil
}

func (l *link) close() {
	close(l.done)
	l.epMu.Lock()
	if l.epCancel != nil {
		l.epCancel()
	}
	l.epMu.Unlock()
	if p := l.proxy.Swap(nil); p != nil {
		p.close()
	}
	_ = l.wifi.Close()
}

// run forwards data and talks to the hub until the session ends.
func (l *link) run(ctx context.Context) error {
	errc := make(chan error, 1)
	go l.ctlLoop(ctx, errc)
	go l.fromHub()
	l.keepalive()
	stats := time.NewTicker(StatsInterval)
	defer stats.Stop()
	lastKA := l.r.cfg.Now()
	for {
		select {
		case <-ctx.Done(): // session() tells the hub and closes the channel
			return ctx.Err()
		case reason := <-l.r.stop:
			_ = l.ctl.Send(&control.Leaving{Reason: reason})
			return &StoppedError{Reason: reason}
		case err := <-errc:
			return err
		case now := <-stats.C:
			if reason := l.limitReached(); reason != "" {
				l.r.cfg.Log.Info("relay limit reached, leaving", "reason", reason)
				_ = l.ctl.Send(&control.Leaving{Reason: reason})
				return &StoppedError{Reason: reason}
			}
			if err := l.sendStats(); err != nil {
				return err
			}
			if now.Sub(lastKA) >= KeepaliveInterval {
				l.keepalive()
				lastKA = now
			}
		}
	}
}

func (l *link) limitReached() string {
	if _, stop := l.r.meter.State(); stop {
		return control.LeavingLimit
	}
	if bmin := l.r.meter.Limits().BatteryMin; bmin > 0 && l.r.cfg.Device().Battery < bmin {
		return control.LeavingBattery
	}
	return ""
}

func (l *link) sendStats() error {
	d := l.r.cfg.Device()
	m := &control.Stats{BytesUp: l.r.up.Load(), BytesDown: l.r.down.Load(), Battery: d.Battery,
		SignalDBm: d.SignalDBm, NetType: d.NetType, CellularOK: d.CellularOK, WifiRSSI: d.WifiRSSI,
		Dropped: l.r.dropped.Load()}
	if u := l.up.Load(); u != nil {
		if c, ok := u.conn.(transport.Counter); ok {
			tc := c.TCPCounters()
			m.TCPConns, m.TCPRetrans, m.TCPSent = tc.Conns, tc.Retrans, tc.Sent
		}
	}
	return l.ctl.Send(m)
}

func (l *link) keepalive() {
	b, _ := wire.AppendRelay(nil, wire.RelayMsg{Kind: wire.RelayKeepalive, PathID: l.welcome.PathID})
	if _, err := l.wifi.WriteToUDPAddrPort(b, l.hub); err != nil {
		l.r.cfg.Log.Debug("keepalive", "err", err)
	}
}

// ctlLoop handles hub messages after welcome.
func (l *link) ctlLoop(ctx context.Context, errc chan<- error) {
	for {
		m, err := l.ctl.Recv()
		if err != nil {
			errc <- err
			return
		}
		switch m := m.(type) {
		case *control.Endpoint:
			l.onEndpoint(ctx, m)
		case *control.Mode:
			l.setProxy(ctx, m)
		case *control.Forget:
			errc <- ErrForgotten
			return
		case *control.Reject:
			errc <- &RejectedError{Reason: m.Reason}
			return
		}
	}
}

// readErrPause is the wait after a failed socket read that did not end the
// loop (battery: such errors can repeat without blocking).
const readErrPause = 50 * time.Millisecond

// fromHub forwards [B1][path][outer] from the hub to the server endpoint.
// Only datagrams from the hub's data socket are accepted.
func (l *link) fromHub() {
	buf := make([]byte, 65535)
	for {
		n, from, err := l.wifi.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(readErrPause) // a persistent socket error must not spin a core
			continue
		}
		if netip.AddrPortFrom(from.Addr().Unmap(), from.Port()) != l.hub {
			l.r.dropped.Add(1)
			continue
		}
		m, err := wire.ParseRelay(buf[:n])
		if err == nil && m.Kind == wire.RelayUDPOut {
			if p := l.proxy.Load(); p != nil {
				p.udpOut(m)
			} else {
				l.r.dropped.Add(1)
			}
			continue
		}
		u := l.up.Load()
		if err != nil || m.Kind != wire.RelayToServer || m.PathID != l.welcome.PathID || u == nil ||
			!l.r.meter.Allow(len(m.Payload)) {
			l.r.dropped.Add(1)
			continue
		}
		if err := u.conn.Send(m.Payload); err != nil {
			l.r.cfg.Log.Debug("to server", "err", err)
			continue
		}
		l.r.up.Add(uint64(len(m.Payload)))
	}
}

// listenAddr is ip:port, or :port for all addresses when ip is zero.
func listenAddr(ip netip.Addr, port int) string {
	if !ip.IsValid() {
		return fmt.Sprintf(":%d", port)
	}
	return netip.AddrPortFrom(ip, uint16(port)).String()
}
