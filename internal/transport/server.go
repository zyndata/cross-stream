package transport

import (
	"context"
	"crypto/tls"
	"errors"
	"log/slog"
	"net"
	"net/http"
	"net/netip"
	"sync"
	"time"

	"github.com/coder/websocket"

	"github.com/zyndata/cross-stream/internal/wire"
)

// Handler receives one outer packet from a stream connection. from is the
// connection's remote address (its path address on the server); replies go
// through s. pkt is only valid during the call.
type Handler func(pkt []byte, from netip.AddrPort, s *ServerConn)

// ServerConn is the server side of one TCP or WSS connection. It implements
// session.Sender: replies always go to this connection, whatever addr says.
type ServerConn struct {
	c      Conn
	remote netip.AddrPort
}

// WriteToUDPAddrPort queues b on the connection (addr is ignored).
func (s *ServerConn) WriteToUDPAddrPort(b []byte, _ netip.AddrPort) (int, error) {
	if err := s.c.Send(b); err != nil {
		return 0, err
	}
	return len(b), nil
}

// Backlog implements Backlogger (0 when the connection does not report it).
func (s *ServerConn) Backlog() int {
	if b, ok := s.c.(Backlogger); ok {
		return b.Backlog()
	}
	return 0
}

// Stream reports true: the bond engine treats this path as reliable.
func (s *ServerConn) Stream() bool { return true }

// Transport returns tcp or wss.
func (s *ServerConn) Transport() wire.Transport { return s.c.Transport() }

// RemoteAddr is the peer address.
func (s *ServerConn) RemoteAddr() netip.AddrPort { return s.remote }

// Close closes the connection.
func (s *ServerConn) Close() error { return s.c.Close() }

// IdleTimeout closes a stream connection that received nothing this long
// (hubs ping every path several times a second). FirstPacketTimeout bounds
// the wait for the first packet (the handshake) of a new connection.
const (
	IdleTimeout        = 2 * time.Minute
	FirstPacketTimeout = 10 * time.Second
)

// Server serves stream transports. Close stops the listeners and every
// connection.
type Server struct {
	Handler Handler
	Log     *slog.Logger
	// MaxPerIP caps open connections per remote IP (0 = no cap); OnRefuse
	// is called for each connection closed because of it.
	MaxPerIP int
	OnRefuse func(netip.Addr)
	// FirstPacket overrides FirstPacketTimeout (tests).
	FirstPacket time.Duration

	mu     sync.Mutex
	conns  map[*ServerConn]struct{}
	perIP  map[netip.Addr]int
	lns    []net.Listener
	https  []*http.Server
	closed bool
	wg     sync.WaitGroup
}

func (sv *Server) log() *slog.Logger {
	if sv.Log != nil {
		return sv.Log
	}
	return slog.Default()
}

// track registers c; false when the server is closing or c's IP already
// has MaxPerIP connections.
func (sv *Server) track(c *ServerConn) bool {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	if sv.closed {
		return false
	}
	ip := c.remote.Addr()
	if sv.MaxPerIP > 0 && sv.perIP[ip] >= sv.MaxPerIP {
		if sv.OnRefuse != nil {
			sv.OnRefuse(ip)
		}
		sv.log().Debug("stream connection refused (per-IP cap)", "from", c.remote)
		return false
	}
	if sv.conns == nil {
		sv.conns = make(map[*ServerConn]struct{})
		sv.perIP = make(map[netip.Addr]int)
	}
	sv.conns[c] = struct{}{}
	sv.perIP[ip]++
	sv.wg.Add(1)
	return true
}

func (sv *Server) untrack(c *ServerConn) {
	sv.mu.Lock()
	delete(sv.conns, c)
	ip := c.remote.Addr()
	if sv.perIP[ip]--; sv.perIP[ip] <= 0 {
		delete(sv.perIP, ip)
	}
	sv.mu.Unlock()
	sv.wg.Done()
}

// serveConn reads packets from c until it fails or goes idle; the first
// packet must arrive within FirstPacketTimeout.
func (sv *Server) serveConn(c *ServerConn, deadline func(time.Time) error) {
	if !sv.track(c) {
		_ = c.Close()
		return
	}
	defer func() {
		_ = c.Close()
		sv.untrack(c)
	}()
	buf := make([]byte, MaxPacket)
	wait := FirstPacketTimeout
	if sv.FirstPacket > 0 {
		wait = sv.FirstPacket
	}
	for {
		_ = deadline(time.Now().Add(wait))
		wait = IdleTimeout
		n, err := c.c.Recv(buf)
		if err != nil {
			sv.log().Debug("stream connection closed", "from", c.remote, "transport", c.Transport(), "err", err)
			return
		}
		sv.Handler(buf[:n], c.remote, c)
	}
}

func remoteAddrPort(s string) netip.AddrPort {
	ap, _ := netip.ParseAddrPort(s)
	return netip.AddrPortFrom(ap.Addr().Unmap(), ap.Port())
}

// ServeTCP accepts framed TCP connections on ln until Close.
func (sv *Server) ServeTCP(ln net.Listener) error {
	if !sv.addListener(ln) {
		return net.ErrClosed
	}
	var delay time.Duration
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil
			}
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			// Out of file descriptors, an aborted connection: back off and
			// keep the listener (like net/http), a hub may have no other way in.
			delay = min(max(2*delay, 5*time.Millisecond), time.Second)
			sv.log().Warn("tcp accept", "err", err, "retry_in", delay)
			time.Sleep(delay)
			continue
		}
		delay = 0
		tuneTCP(c)
		sc := &ServerConn{c: newTCPConn(c), remote: remoteAddrPort(c.RemoteAddr().String())}
		go sv.serveConn(sc, c.SetReadDeadline)
	}
}

// ServeWSS serves WebSocket connections on path over TLS on ln until Close.
// Any other request gets 404, like a plain HTTPS server.
func (sv *Server) ServeWSS(ln net.Listener, cert tls.Certificate, path string) error {
	mux := http.NewServeMux()
	mux.HandleFunc(path, func(w http.ResponseWriter, r *http.Request) {
		ws, err := websocket.Accept(w, r, nil)
		if err != nil {
			return
		}
		wc := newWSConn(ws)
		sv.serveConn(&ServerConn{c: wc, remote: remoteAddrPort(r.RemoteAddr)}, wc.setReadDeadline)
	})
	hs := &http.Server{
		Handler:           mux,
		TLSConfig:         &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS12},
		ReadHeaderTimeout: 10 * time.Second,
		ErrorLog:          slog.NewLogLogger(sv.log().Handler(), slog.LevelDebug),
		BaseContext:       func(net.Listener) context.Context { return context.Background() },
	}
	sv.mu.Lock()
	if sv.closed {
		sv.mu.Unlock()
		_ = ln.Close()
		return net.ErrClosed
	}
	sv.https = append(sv.https, hs)
	sv.mu.Unlock()
	err := hs.ServeTLS(ln, "", "")
	if errors.Is(err, http.ErrServerClosed) || errors.Is(err, net.ErrClosed) {
		return nil
	}
	return err
}

func (sv *Server) addListener(ln net.Listener) bool {
	sv.mu.Lock()
	defer sv.mu.Unlock()
	if sv.closed {
		_ = ln.Close()
		return false
	}
	sv.lns = append(sv.lns, ln)
	return true
}

// Close stops all listeners and connections and waits for their goroutines.
func (sv *Server) Close() {
	sv.mu.Lock()
	sv.closed = true
	lns, hss := sv.lns, sv.https
	conns := make([]*ServerConn, 0, len(sv.conns))
	for c := range sv.conns {
		conns = append(conns, c)
	}
	sv.mu.Unlock()
	for _, ln := range lns {
		_ = ln.Close()
	}
	for _, hs := range hss {
		_ = hs.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
	sv.wg.Wait()
}
