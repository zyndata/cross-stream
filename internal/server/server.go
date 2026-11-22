package server

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/server/admin"
	"github.com/zyndata/cross-stream/internal/transport"
	"github.com/zyndata/cross-stream/internal/tun"
)

// Options are the dependencies of a Server. cmd/xs-server wires the real
// ones; tests use an in-memory TUN and NoFirewall.
type Options struct {
	Config   Config
	Key      crypto.Keypair // server static key
	State    *State
	TUN      tun.Device // owned by the server from Listen on (closed by Run)
	Firewall Firewall
	Log      *slog.Logger
	Now      func() time.Time
	// Detect finds the public address when Config.PublicEndpoints is empty.
	Detect func() (netip.Addr, error)
	// DialOrigin connects PEP streams (nil = a BBR TCP socket after the
	// destination check).
	DialOrigin func(ctx context.Context, dst netip.AddrPort) (net.Conn, error)
	// Version is shown in the admin server info.
	Version string
	// Logs returns recent log lines, redacted (web panel; nil = none).
	Logs func() []string
	// Ports is where the tunnel port is configured (nil = defaults only, read-only).
	Ports PortSource
	// Restart restarts the server process (nil = not possible from the panel).
	Restart func(context.Context) error
}

// Server is xs-server: UDP listeners, session table, TUN forwarding, NAT and
// the control socket.
type Server struct {
	o      Options
	log    *slog.Logger
	egress Egress
	hsRate *rateLimiter // handshakes per source IP
	conns  []*net.UDPConn
	ctl    net.Listener
	adm    *admin.Service
	// started is when New ran (server info).
	started time.Time

	streams *transport.Server // TCP and WSS connections
	tcpLns  []net.Listener
	wssLns  []net.Listener
	cert    tls.Certificate

	closing  atomic.Bool // set before sockets and TUN are closed
	counters counters
	metrics  net.Listener // nil when metrics_listen is empty

	mu     sync.Mutex
	fwdMu  sync.Mutex // serializes port forward rule updates
	byID   map[uint32]*hubSession
	byName map[string]*hubSession
	byIP   map[netip.Addr]*hubSession
	initTS map[string]int64 // newest accepted INIT timestamp per hub name
}

// New checks opts and returns a server that is not listening yet.
func New(o Options) (*Server, error) {
	if err := o.Config.Validate(); err != nil {
		return nil, err
	}
	if o.State == nil || o.TUN == nil {
		return nil, errors.New("server: State and TUN are required")
	}
	if o.Firewall == nil {
		o.Firewall = NoFirewall{}
	}
	if o.Log == nil {
		o.Log = slog.Default()
	}
	if o.Now == nil {
		o.Now = time.Now
	}
	s := &Server{
		o: o, log: o.Log, egress: o.Config.Egress(), hsRate: newRateLimiter(HandshakeRate, HandshakeBurst, MaxRateSources),
		byID: map[uint32]*hubSession{}, byName: map[string]*hubSession{}, byIP: map[netip.Addr]*hubSession{},
		initTS: map[string]int64{}, started: o.Now(),
	}
	s.adm = s.newAdmin()
	s.streams = &transport.Server{Log: o.Log, MaxPerIP: MaxStreamConnsPerIP,
		OnRefuse: func(netip.Addr) { s.counters.streamDenied.Add(1) },
		Handler: func(pkt []byte, from netip.AddrPort, c *transport.ServerConn) {
			s.handlePacket(pkt, from, c)
		}}
	return s, nil
}

// Listen sets up NAT, opens the UDP ports and the control socket. On error
// everything opened so far is closed again.
func (s *Server) Listen(ctx context.Context) (err error) {
	defer func() {
		if err != nil {
			s.closeAll(context.WithoutCancel(ctx))
		}
	}()
	if err := s.o.Firewall.Setup(ctx); err != nil {
		return err
	}
	for _, p := range s.o.Config.ListenPorts {
		addr := net.JoinHostPort(s.o.Config.ListenAddress, strconv.Itoa(p))
		lc := net.ListenConfig{Control: dontFragment}
		pc, err := lc.ListenPacket(ctx, "udp", addr)
		if err != nil {
			return fmt.Errorf("listen %s: %w", addr, err)
		}
		s.conns = append(s.conns, pc.(*net.UDPConn))
	}
	if err := s.listenStreams(ctx); err != nil {
		return err
	}
	sock := s.o.Config.ControlSocket
	if err := os.Remove(sock); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("remove stale control socket: %w", err)
	}
	if s.ctl, err = net.Listen("unix", sock); err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	if err := os.Chmod(sock, 0o600); err != nil {
		return fmt.Errorf("control socket: %w", err)
	}
	if m := s.o.Config.MetricsListen; m != "" {
		if s.metrics, err = (&net.ListenConfig{}).Listen(ctx, "tcp", m); err != nil {
			return fmt.Errorf("metrics listen: %w", err)
		}
	}
	_, tunnel := s.codePorts()
	stale, err := s.o.State.SetTunnelPort(tunnel)
	if err != nil {
		return err
	}
	if len(stale) > 0 {
		s.log.Warn("tunnel port changed: these hubs were enrolled with another port and must enroll again (revoke + new code)",
			"tunnel_port", tunnel, "hubs", stale)
	}
	s.log.Info("xs-server listening", "ports", s.o.Config.ListenPorts, "tcp", s.o.Config.TCPPorts,
		"wss", s.o.Config.WSSPorts, "tun", s.o.TUN.Name(),
		"control", sock, "key", s.o.Key)
	return nil
}

// listenStreams opens the TCP and WSS listeners (WSS: self-signed
// certificate in the data directory, created on first use).
func (s *Server) listenStreams(ctx context.Context) error {
	lc := net.ListenConfig{}
	for _, p := range s.o.Config.TCPPorts {
		addr := net.JoinHostPort(s.o.Config.ListenAddress, strconv.Itoa(p))
		ln, err := lc.Listen(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("listen tcp %s: %w", addr, err)
		}
		s.tcpLns = append(s.tcpLns, ln)
	}
	if len(s.o.Config.WSSPorts) == 0 {
		return nil
	}
	cert, err := transport.LoadOrCreateCert(s.o.Config.DataDir, "")
	if err != nil {
		return err
	}
	s.cert = cert
	for _, p := range s.o.Config.WSSPorts {
		addr := net.JoinHostPort(s.o.Config.ListenAddress, strconv.Itoa(p))
		ln, err := lc.Listen(ctx, "tcp", addr)
		if err != nil {
			return fmt.Errorf("listen wss %s: %w", addr, err)
		}
		s.wssLns = append(s.wssLns, ln)
	}
	return nil
}

// StreamPorts returns the local ports of the TCP and WSS listeners.
func (s *Server) StreamPorts() (tcp, wss []int) {
	for _, ln := range s.tcpLns {
		tcp = append(tcp, ln.Addr().(*net.TCPAddr).Port)
	}
	for _, ln := range s.wssLns {
		wss = append(wss, ln.Addr().(*net.TCPAddr).Port)
	}
	return tcp, wss
}

// Addrs returns the local addresses of the UDP listeners.
func (s *Server) Addrs() []netip.AddrPort {
	out := make([]netip.AddrPort, 0, len(s.conns))
	for _, c := range s.conns {
		out = append(out, c.LocalAddr().(*net.UDPAddr).AddrPort())
	}
	return out
}

// Run forwards traffic until ctx is done, then tells hubs the server stops,
// closes sockets and TUN and removes the NAT rules.
func (s *Server) Run(ctx context.Context) error {
	if s.ctl == nil {
		return errors.New("server: Run before Listen")
	}
	var wg sync.WaitGroup
	for _, c := range s.conns {
		wg.Add(1)
		go func() { defer wg.Done(); s.readLoop(c) }()
	}
	for _, ln := range s.tcpLns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.streams.ServeTCP(ln); err != nil {
				s.log.Warn("tcp listener", "err", err)
			}
		}()
	}
	for _, ln := range s.wssLns {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.streams.ServeWSS(ln, s.cert, s.o.Config.WSSPath); err != nil {
				s.log.Warn("wss listener", "err", err)
			}
		}()
	}
	if s.metrics != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if err := s.serveMetrics(ctx, s.metrics); err != nil {
				s.log.Warn("metrics", "err", err)
			}
		}()
	}
	wg.Add(3)
	go func() { defer wg.Done(); s.tunLoop() }()
	go func() { defer wg.Done(); s.tickLoop(ctx) }()
	ctlErr := make(chan error, 1)
	go func() { defer wg.Done(); ctlErr <- admin.Serve(ctx, s.ctl, s.adm, s.log) }()

	var err error
	select {
	case <-ctx.Done():
	case err = <-ctlErr:
	}
	s.closeSessions()
	s.closeAll(context.WithoutCancel(ctx))
	wg.Wait()
	s.log.Info("xs-server stopped")
	return err
}

// closeAll releases sockets, TUN, control socket and NAT rules.
func (s *Server) closeAll(ctx context.Context) {
	s.closing.Store(true)
	for _, c := range s.conns {
		_ = c.Close()
	}
	for _, ln := range append(append([]net.Listener{}, s.tcpLns...), s.wssLns...) {
		_ = ln.Close()
	}
	s.streams.Close()
	if s.metrics != nil {
		_ = s.metrics.Close()
	}
	if s.ctl != nil {
		_ = s.ctl.Close()
		_ = os.Remove(s.o.Config.ControlSocket)
	}
	_ = s.o.TUN.Close()
	cctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if err := s.o.Firewall.Cleanup(cctx); err != nil {
		s.log.Warn("firewall cleanup", "err", err)
	}
}
