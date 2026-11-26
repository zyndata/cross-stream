package dns

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"
)

// Defaults of the hub DNS forwarder.
const (
	DefaultCacheSize = 1024
	DefaultTimeout   = 2 * time.Second
	maxCacheTTL      = time.Hour
	emptyTTL         = 30 * time.Second // NOERROR/NXDOMAIN without records
	maxInflight      = 128
	tcpIdle          = 10 * time.Second
)

// DefaultUpstreams are used when Config.Upstreams is empty.
var DefaultUpstreams = []netip.AddrPort{netip.MustParseAddrPort("1.1.1.1:53"), netip.MustParseAddrPort("9.9.9.9:53")}

// DialFunc opens a connection to an upstream ("udp" or "tcp").
type DialFunc func(ctx context.Context, network, addr string) (net.Conn, error)

// Config configures the forwarder.
type Config struct {
	Listen    []netip.AddrPort // e.g. 10.42.0.1:53 (UDP and TCP)
	Upstreams []netip.AddrPort
	Dial      DialFunc // nil = net.Dialer (tests); the hub binds it to the tunnel
	// Control, if set, runs on every listening socket (the hub marks them).
	Control   func(network, address string, c syscall.RawConn) error
	Timeout   time.Duration
	CacheSize int
	Log       *slog.Logger
	Now       func() time.Time
	// OnAnswer, if set, sees every answer with IPv4 addresses that is sent
	// to a client (also from the cache): the asked name (lower case, no
	// trailing dot), the addresses and their smallest TTL. The hub fills its
	// bypass IP set with it.
	OnAnswer func(name string, ips []netip.Addr, ttl time.Duration)
	// Canary, if set and true, makes the forwarder answer NXDOMAIN itself
	// for the canary names (CanaryNames): Firefox then drops its default
	// DoH and Apple devices turn off Private Relay, so their lookups reach
	// this forwarder. The hub sets it while the bypass list is not empty.
	Canary func() bool
}

// CanaryNames get NXDOMAIN while Config.Canary is true (exact names, any
// query type; subdomains are forwarded as usual).
var CanaryNames = []string{"use-application-dns.net", "mask.icloud.com", "mask-h2.icloud.com"}

// canary reports whether q gets the local NXDOMAIN answer.
func (s *Server) canary(q query) bool {
	if s.cfg.Canary == nil || !q.opcode0 {
		return false
	}
	for _, n := range CanaryNames {
		if q.name == n {
			return s.cfg.Canary()
		}
	}
	return false
}

// Server is a small caching DNS forwarder.
type Server struct {
	cfg  Config
	log  *slog.Logger
	sem  chan struct{}
	mu   sync.Mutex
	c    map[string]*entry
	good int // index of the upstream that answered last
}

type entry struct {
	msg     []byte
	ttlOffs []int
	stored  time.Time
	expires time.Time
}

// New returns a forwarder; Run serves it.
func New(cfg Config) *Server {
	if len(cfg.Upstreams) == 0 {
		cfg.Upstreams = DefaultUpstreams
	}
	if cfg.Dial == nil {
		var d net.Dialer
		cfg.Dial = d.DialContext
	}
	if cfg.Timeout <= 0 {
		cfg.Timeout = DefaultTimeout
	}
	if cfg.CacheSize <= 0 {
		cfg.CacheSize = DefaultCacheSize
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	return &Server{cfg: cfg, log: cfg.Log, sem: make(chan struct{}, maxInflight), c: make(map[string]*entry)}
}

// Run serves every listen address until ctx ends. An address that cannot be
// bound yet (the port has no address yet) is retried every 2 s.
func (s *Server) Run(ctx context.Context) {
	var wg sync.WaitGroup
	for _, a := range s.cfg.Listen {
		wg.Add(1)
		go func() { defer wg.Done(); s.serveAddr(ctx, a) }()
	}
	wg.Wait()
}

// Serve serves one more listen address until ctx ends (the hub's tunnel
// address, known only once enrolled); like Run, it retries every 2 s while
// the address cannot be bound.
func (s *Server) Serve(ctx context.Context, a netip.AddrPort) { s.serveAddr(ctx, a) }

func (s *Server) serveAddr(ctx context.Context, a netip.AddrPort) {
	logged := false
	lc := net.ListenConfig{Control: s.cfg.Control}
	for ctx.Err() == nil {
		p, err := lc.ListenPacket(ctx, "udp", a.String())
		if err == nil {
			pc := p.(*net.UDPConn)
			var ln net.Listener
			if ln, err = lc.Listen(ctx, "tcp", a.String()); err == nil {
				s.log.Info("dns listening", "addr", a)
				go func() { <-ctx.Done(); _ = pc.Close(); _ = ln.Close() }()
				go s.serveTCP(ctx, ln)
				s.serveUDP(ctx, pc)
				return
			}
			_ = pc.Close()
		}
		if !logged {
			s.log.Warn("dns listen, retrying every 2 s", "addr", a, "err", err)
			logged = true
		}
		select {
		case <-ctx.Done():
		case <-time.After(2 * time.Second):
		}
	}
}

func (s *Server) serveUDP(ctx context.Context, pc *net.UDPConn) {
	buf := make([]byte, 4096)
	for {
		n, from, err := pc.ReadFromUDPAddrPort(buf)
		if err != nil {
			if ctx.Err() != nil || errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		q := append([]byte(nil), buf[:n]...)
		select {
		case s.sem <- struct{}{}:
		default:
			continue // overloaded: drop, the client retries
		}
		go func() {
			defer func() { <-s.sem }()
			if resp := s.Answer(ctx, q, false); resp != nil {
				_, _ = pc.WriteToUDPAddrPort(resp, from)
			}
		}()
	}
}

func (s *Server) serveTCP(ctx context.Context, ln net.Listener) {
	for {
		c, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) || ctx.Err() != nil {
				return
			}
			time.Sleep(50 * time.Millisecond) // transient: keep serving DNS over TCP
			continue
		}
		go func() {
			defer func() { _ = c.Close() }()
			for {
				_ = c.SetDeadline(time.Now().Add(tcpIdle))
				q, err := readTCPMsg(c)
				if err != nil {
					return
				}
				resp := s.Answer(ctx, q, true)
				if resp == nil || writeTCPMsg(c, resp) != nil {
					return
				}
			}
		}()
	}
}

func readTCPMsg(r io.Reader) ([]byte, error) {
	var l [2]byte
	if _, err := io.ReadFull(r, l[:]); err != nil {
		return nil, err
	}
	b := make([]byte, binary.BigEndian.Uint16(l[:]))
	if _, err := io.ReadFull(r, b); err != nil {
		return nil, err
	}
	return b, nil
}

func writeTCPMsg(w io.Writer, m []byte) error {
	b := make([]byte, 2+len(m))
	binary.BigEndian.PutUint16(b, uint16(len(m)))
	copy(b[2:], m)
	_, err := w.Write(b)
	return err
}

// Answer returns the answer to one query (nil = drop it): from the cache or
// the upstreams; SERVFAIL when no upstream answers. overTCP lifts the UDP
// size limit (truncated upstream answers are then fetched over TCP).
func (s *Server) Answer(ctx context.Context, msg []byte, overTCP bool) []byte {
	q, err := parseQuery(msg)
	if err != nil {
		return nil
	}
	if s.canary(q) {
		s.log.Debug("dns canary answered", "name", q.name)
		return emptyAnswer(msg, q.qEnd, rcodeNX)
	}
	maxSize := q.maxUDP
	if overTCP {
		maxSize = 65535
	}
	if q.opcode0 {
		if r := s.cached(q.key, msg[:2], maxSize); r != nil {
			s.notify(r)
			return r
		}
	}
	resp, err := s.forward(ctx, msg, q, overTCP)
	if err != nil {
		s.log.Debug("dns upstream", "err", err)
		return servfail(msg, q.qEnd)
	}
	if q.opcode0 && !truncated(resp) {
		s.store(q.key, resp)
	}
	s.notify(resp)
	return resp
}

// notify passes the A records of an answer to OnAnswer.
func (s *Server) notify(resp []byte) {
	if s.cfg.OnAnswer == nil || rcode(resp) != rcodeOK {
		return
	}
	name, ips, ttl, err := answerA(resp)
	if err != nil || len(ips) == 0 {
		return
	}
	s.cfg.OnAnswer(name, ips, time.Duration(ttl)*time.Second)
}

// forward asks the upstreams in turn, starting with the last good one.
func (s *Server) forward(ctx context.Context, msg []byte, q query, overTCP bool) ([]byte, error) {
	s.mu.Lock()
	first := s.good
	s.mu.Unlock()
	ups := s.cfg.Upstreams
	var errs []error
	for i := range ups {
		idx := (first + i) % len(ups)
		resp, err := s.exchange(ctx, ups[idx].String(), msg, q, "udp")
		if err == nil && overTCP && truncated(resp) {
			resp, err = s.exchange(ctx, ups[idx].String(), msg, q, "tcp")
		}
		if err == nil {
			s.mu.Lock()
			s.good = idx
			s.mu.Unlock()
			return resp, nil
		}
		errs = append(errs, err)
	}
	return nil, errors.Join(errs...)
}

// exchange sends msg with a random ID and returns the answer with the
// client's ID put back.
func (s *Server) exchange(ctx context.Context, addr string, msg []byte, q query, network string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, s.cfg.Timeout)
	defer cancel()
	c, err := s.cfg.Dial(ctx, network, addr)
	if err != nil {
		return nil, fmt.Errorf("dns %s %s: %w", network, addr, err)
	}
	defer func() { _ = c.Close() }()
	if dl, ok := ctx.Deadline(); ok {
		_ = c.SetDeadline(dl)
	}
	out := append([]byte(nil), msg...)
	var id [2]byte
	_, _ = rand.Read(id[:])
	copy(out, id[:])
	var resp []byte
	if network == "tcp" {
		if err := writeTCPMsg(c, out); err != nil {
			return nil, fmt.Errorf("dns tcp %s: %w", addr, err)
		}
		if resp, err = readTCPMsg(c); err != nil {
			return nil, fmt.Errorf("dns tcp %s: %w", addr, err)
		}
	} else {
		if _, err := c.Write(out); err != nil {
			return nil, fmt.Errorf("dns udp %s: %w", addr, err)
		}
		buf := make([]byte, 65535)
		for {
			n, err := c.Read(buf)
			if err != nil {
				return nil, fmt.Errorf("dns udp %s: %w", addr, err)
			}
			if n >= headerLen && buf[0] == id[0] && buf[1] == id[1] {
				resp = append([]byte(nil), buf[:n]...)
				break
			}
		}
	}
	if len(resp) < headerLen || resp[0] != id[0] || resp[1] != id[1] ||
		binary.BigEndian.Uint16(resp[2:])&flagQR == 0 || !sameQuestion(msg, q.qEnd, resp) {
		return nil, fmt.Errorf("dns %s %s: answer does not match the query", network, addr)
	}
	copy(resp, msg[:2])
	return resp, nil
}

// cached returns a copy of a fresh cached answer with id and aged TTLs, or
// nil when there is none or it is larger than maxSize.
func (s *Server) cached(key string, id []byte, maxSize int) []byte {
	now := s.cfg.Now()
	s.mu.Lock()
	e := s.c[key]
	if e != nil && !now.Before(e.expires) {
		delete(s.c, key)
		e = nil
	}
	s.mu.Unlock()
	if e == nil || len(e.msg) > maxSize {
		return nil
	}
	r := append([]byte(nil), e.msg...)
	copy(r, id)
	age := uint32(now.Sub(e.stored) / time.Second)
	for _, off := range e.ttlOffs {
		ttl := binary.BigEndian.Uint32(r[off:])
		binary.BigEndian.PutUint32(r[off:], ttl-min(ttl, age))
	}
	return r
}

// store caches NOERROR and NXDOMAIN answers for their smallest TTL.
func (s *Server) store(key string, resp []byte) {
	if rc := rcode(resp); rc != rcodeOK && rc != rcodeNX {
		return
	}
	offs, minTTL, err := answerTTLs(resp)
	if err != nil {
		return
	}
	ttl := emptyTTL
	if len(offs) > 0 {
		ttl = min(time.Duration(minTTL)*time.Second, maxCacheTTL)
	}
	if ttl <= 0 {
		return
	}
	now := s.cfg.Now()
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.c) >= s.cfg.CacheSize {
		for k, e := range s.c {
			if !now.Before(e.expires) {
				delete(s.c, k)
			}
		}
		for k := range s.c { // still full: drop an arbitrary entry
			if len(s.c) < s.cfg.CacheSize {
				break
			}
			delete(s.c, k)
		}
	}
	s.c[key] = &entry{msg: append([]byte(nil), resp...), ttlOffs: offs, stored: now, expires: now.Add(ttl)}
}

// CacheLen returns the number of cached answers.
func (s *Server) CacheLen() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.c)
}
