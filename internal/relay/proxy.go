package relay

import (
	"context"
	"crypto/subtle"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/wire"
)

// Proxy role timers and limits (protocol.md section 3, decisions.md phase 06).
const (
	ProxyDialTimeout = 10 * time.Second
	ProxyTCPIdle     = 30 * time.Minute
	ProxyUDPIdle     = 60 * time.Second
	ProxyMaxUDPFlows = 256
	proxyBufLen      = 16 << 10 // ≤ the throttle burst, so a chunk always fits
	capWait          = 20 * time.Millisecond
)

var errCapReached = errors.New("relay: data cap reached")

// proxy is the DIRECT-mode proxy role of one hub session: a TCP listener for
// the hub's proxy connections and the UDP flows of B4/B5 datagrams. Every
// connection goes out over cellular and counts against the data cap.
type proxy struct {
	l       *link
	ln      net.Listener
	ctx     context.Context
	cancel  context.CancelFunc
	token   atomic.Pointer[[wire.ProxyTokenLen]byte]
	tcpIdle time.Duration
	udpIdle time.Duration
	wg      sync.WaitGroup
	tcp     atomic.Int32 // spliced TCP flows

	mu     sync.Mutex
	closed bool
	conns  map[net.Conn]struct{}
	udp    map[uint32]*udpFlow
}

// setProxy applies a mode message: start the proxy role, change its token or
// stop it. Called from the control loop only.
func (l *link) setProxy(ctx context.Context, m *control.Mode) {
	if !m.Proxy {
		if p := l.proxy.Swap(nil); p != nil {
			p.close()
			l.r.cfg.Log.Info("proxy role off", "mode", m.Mode)
		}
		return
	}
	tok, err := decodeToken(m.ProxyToken)
	if err != nil {
		l.r.cfg.Log.Warn("proxy role", "err", err)
		return
	}
	if p := l.proxy.Load(); p != nil {
		p.token.Store(&tok)
		return
	}
	p, err := l.newProxy(ctx, tok)
	if err != nil {
		l.r.cfg.Log.Warn("proxy role", "err", err)
		return
	}
	l.proxy.Store(p)
	select {
	case <-l.done: // the link closed meanwhile: nobody else would close p
		if l.proxy.CompareAndSwap(p, nil) {
			p.close()
		}
		return
	default:
	}
	l.r.cfg.Log.Info("proxy role on", "mode", m.Mode, "listen", p.ln.Addr())
}

func decodeToken(s string) ([wire.ProxyTokenLen]byte, error) {
	var tok [wire.ProxyTokenLen]byte
	b, err := base64.StdEncoding.DecodeString(s)
	if err != nil || len(b) != wire.ProxyTokenLen {
		return tok, fmt.Errorf("proxy token: %d bytes, %v", len(b), err)
	}
	copy(tok[:], b)
	return tok, nil
}

func (l *link) newProxy(ctx context.Context, tok [wire.ProxyTokenLen]byte) (*proxy, error) {
	port := portOr(l.r.cfg.ProxyPort, control.ProxyPort)
	lc := net.ListenConfig{Control: bindFunc(l.r.cfg.Binder, NetWiFi, false)}
	ln, err := lc.Listen(ctx, "tcp4", listenAddr(l.r.cfg.ListenIP, port))
	if err != nil {
		return nil, fmt.Errorf("proxy listen: %w", err)
	}
	p := &proxy{l: l, ln: ln, tcpIdle: ProxyTCPIdle, udpIdle: ProxyUDPIdle,
		conns: make(map[net.Conn]struct{}), udp: make(map[uint32]*udpFlow)}
	if l.r.cfg.proxyIdle > 0 {
		p.tcpIdle, p.udpIdle = l.r.cfg.proxyIdle, l.r.cfg.proxyIdle
	}
	p.ctx, p.cancel = context.WithCancel(ctx)
	p.token.Store(&tok)
	p.wg.Add(2)
	go p.acceptLoop()
	go p.sweepLoop()
	return p, nil
}

func portOr(p, def int) int {
	switch p {
	case 0:
		return def
	case -1:
		return 0
	}
	return p
}

// close stops the listener and ends every flow.
func (p *proxy) close() {
	p.mu.Lock()
	if p.closed {
		p.mu.Unlock()
		return
	}
	p.closed = true
	p.cancel()
	_ = p.ln.Close()
	for c := range p.conns {
		_ = c.Close()
	}
	for _, f := range p.udp {
		_ = f.conn.Close()
	}
	p.mu.Unlock()
	p.wg.Wait()
}

// port returns the listening port.
func (p *proxy) port() int { return p.ln.Addr().(*net.TCPAddr).Port }

// flows returns the number of spliced TCP flows and open UDP flows.
func (p *proxy) flows() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return int(p.tcp.Load()) + len(p.udp)
}

// track registers a connection to close on stop; false when stopped.
func (p *proxy) track(c net.Conn) bool {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.closed {
		return false
	}
	p.conns[c] = struct{}{}
	return true
}

func (p *proxy) untrack(c net.Conn) {
	_ = c.Close()
	p.mu.Lock()
	delete(p.conns, c)
	p.mu.Unlock()
}

func (p *proxy) acceptLoop() {
	defer p.wg.Done()
	for {
		c, err := p.ln.Accept()
		if err != nil {
			p.mu.Lock()
			closed := p.closed
			p.mu.Unlock()
			if closed || errors.Is(err, net.ErrClosed) {
				return
			}
			time.Sleep(readErrPause) // transient (out of descriptors, aborted): keep the proxy
			continue
		}
		ta, _ := c.RemoteAddr().(*net.TCPAddr)
		if ta == nil || ta.AddrPort().Addr().Unmap() != p.l.hub.Addr() {
			p.l.r.dropped.Add(1)
			_ = c.Close()
			continue
		}
		p.mu.Lock()
		ok := !p.closed
		if ok {
			p.conns[c] = struct{}{}
			p.wg.Add(1)
		}
		p.mu.Unlock()
		if !ok {
			_ = c.Close()
			return
		}
		go func() {
			defer p.wg.Done()
			defer p.untrack(c)
			p.serveTCP(c)
		}()
	}
}

// serveTCP reads the request, dials the destination over cellular, replies
// and splices the two streams.
func (p *proxy) serveTCP(c net.Conn) {
	_ = c.SetDeadline(time.Now().Add(ProxyDialTimeout))
	buf := make([]byte, wire.ProxyRequestLen)
	if _, err := io.ReadFull(c, buf); err != nil {
		return
	}
	req, err := wire.ParseProxyRequest(buf)
	code := p.check(req, err)
	var out net.Conn
	if code == wire.ProxyOK {
		out, code = p.dial(req.Dst)
	}
	_ = c.SetDeadline(time.Now().Add(ProxyDialTimeout)) // the dial may have used up the first one
	if _, err := c.Write([]byte{code}); err != nil || code != wire.ProxyOK {
		if out != nil {
			_ = out.Close()
		}
		if code != wire.ProxyOK {
			p.l.r.cfg.Log.Debug("proxy connection refused", "dst", req.Dst, "code", code)
		}
		return
	}
	_ = c.SetDeadline(time.Time{})
	if !p.track(out) {
		_ = out.Close()
		return
	}
	defer p.untrack(out)
	p.splice(c, out)
}

// check validates a parsed request and returns the reply code.
func (p *proxy) check(req wire.ProxyRequest, err error) byte {
	switch {
	case errors.Is(err, wire.ErrType):
		return wire.ProxyBadAddress
	case err != nil:
		return wire.ProxyFailure
	}
	tok := p.token.Load()
	if subtle.ConstantTimeCompare(req.Token[:], tok[:]) != 1 || !p.allowed(req.Dst) {
		return wire.ProxyNotAllowed
	}
	if _, stop := p.l.r.meter.State(); stop {
		return wire.ProxyCapReached
	}
	return wire.ProxyOK
}

func (p *proxy) allowed(dst netip.AddrPort) bool {
	return allowedDst(dst) || (p.l.r.cfg.allowLoopback && dst.Addr().IsLoopback() && dst.Port() != 0)
}

// allowedDst refuses destinations that must never leave over cellular.
func allowedDst(ap netip.AddrPort) bool {
	a := ap.Addr()
	return a.Is4() && ap.Port() != 0 && !a.IsUnspecified() && !a.IsLoopback() && !a.IsMulticast() &&
		!a.IsLinkLocalUnicast() && a != netip.AddrFrom4([4]byte{255, 255, 255, 255})
}

// dial connects to dst over cellular and returns the reply code.
func (p *proxy) dial(dst netip.AddrPort) (net.Conn, byte) {
	d := net.Dialer{
		Timeout:         ProxyDialTimeout,
		Control:         bindFunc(p.l.r.cfg.Binder, NetCellular, false),
		KeepAliveConfig: net.KeepAliveConfig{Enable: true, Idle: time.Minute, Interval: 15 * time.Second, Count: 4},
	}
	c, err := d.DialContext(p.ctx, "tcp4", dst.String())
	if err == nil {
		return c, wire.ProxyOK
	}
	var ne net.Error
	switch {
	case isRefused(err):
		return nil, wire.ProxyRefused
	case errors.As(err, &ne) && ne.Timeout(), errors.Is(err, syscall.EHOSTUNREACH), errors.Is(err, syscall.ENETUNREACH):
		return nil, wire.ProxyUnreachable
	}
	return nil, wire.ProxyFailure
}

// splice copies both ways until both directions end or one fails.
func (p *proxy) splice(hub, out net.Conn) {
	p.tcp.Add(1)
	defer p.tcp.Add(-1)
	var last atomic.Int64
	last.Store(time.Now().UnixNano())
	errc := make(chan error, 2)
	go func() { errc <- p.pipe(out, hub, &p.l.r.up, &last) }()
	go func() { errc <- p.pipe(hub, out, &p.l.r.down, &last) }()
	for range 2 {
		if err := <-errc; err != nil {
			_ = hub.Close()
			_ = out.Close()
		}
	}
}

// pipe copies src to dst, counting bytes against the cap. EOF half-closes dst.
func (p *proxy) pipe(dst, src net.Conn, ctr *atomic.Uint64, last *atomic.Int64) error {
	buf := make([]byte, proxyBufLen)
	for {
		_ = src.SetReadDeadline(time.Now().Add(p.tcpIdle))
		n, err := src.Read(buf)
		if n > 0 {
			if !p.waitAllow(n) {
				return errCapReached
			}
			if _, werr := dst.Write(buf[:n]); werr != nil {
				return werr
			}
			ctr.Add(uint64(n))
			last.Store(time.Now().UnixNano())
		}
		if err == nil {
			continue
		}
		if errors.Is(err, io.EOF) {
			if cw, ok := dst.(interface{ CloseWrite() error }); ok {
				_ = cw.CloseWrite()
			}
			return nil
		}
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() && time.Since(time.Unix(0, last.Load())) < p.tcpIdle {
			continue // the other direction is active
		}
		return err
	}
}

// waitAllow counts n bytes against the cap, waiting for throttle tokens; it
// returns false when the cap stops the relay or the proxy closes.
func (p *proxy) waitAllow(n int) bool {
	m := p.l.r.meter
	for {
		if m.Allow(n) {
			return true
		}
		if _, stop := m.State(); stop {
			return false
		}
		select {
		case <-p.ctx.Done():
			return false
		case <-time.After(capWait):
		}
	}
}
