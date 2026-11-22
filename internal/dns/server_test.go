package dns

import (
	"context"
	"encoding/binary"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// buildQuery makes a query for name/type A with an optional EDNS OPT record.
func buildQuery(id uint16, name string, edns uint16) []byte {
	b := make([]byte, headerLen)
	binary.BigEndian.PutUint16(b, id)
	binary.BigEndian.PutUint16(b[2:], 0x0100) // RD
	binary.BigEndian.PutUint16(b[4:], 1)
	b = appendName(b, name)
	b = binary.BigEndian.AppendUint16(b, 1)
	b = binary.BigEndian.AppendUint16(b, 1)
	if edns > 0 {
		binary.BigEndian.PutUint16(b[10:], 1)
		b = append(b, 0)                              // root name
		b = binary.BigEndian.AppendUint16(b, typeOPT) // type
		b = binary.BigEndian.AppendUint16(b, edns)    // class = UDP size
		b = binary.BigEndian.AppendUint32(b, 0)       // ext rcode, version, flags
		b = binary.BigEndian.AppendUint16(b, 0)       // rdlen
	}
	return b
}

func appendName(b []byte, name string) []byte {
	for _, l := range strings.Split(strings.TrimSuffix(name, "."), ".") {
		b = append(b, byte(len(l)))
		b = append(b, l...)
	}
	return append(b, 0)
}

// buildAnswer answers q with n A records (compressed names) of the given TTL,
// padded with extra TXT-like bytes to reach at least size bytes.
func buildAnswer(q []byte, rc int, ttl uint32, n int, size int) []byte {
	qq, err := parseQuery(q)
	if err != nil {
		panic(err)
	}
	b := append([]byte(nil), q[:qq.qEnd]...)
	binary.BigEndian.PutUint16(b[2:], flagQR|0x0100|flagRA|uint16(rc))
	binary.BigEndian.PutUint16(b[6:], uint16(n))
	binary.BigEndian.PutUint16(b[8:], 0)
	binary.BigEndian.PutUint16(b[10:], 0)
	for i := range n {
		b = append(b, 0xC0, headerLen) // pointer to the question name
		b = binary.BigEndian.AppendUint16(b, 1)
		b = binary.BigEndian.AppendUint16(b, 1)
		b = binary.BigEndian.AppendUint32(b, ttl)
		pad := 4
		if i == 0 && size > len(b)+6 {
			pad = size - len(b) - 2
		}
		b = binary.BigEndian.AppendUint16(b, uint16(pad))
		b = append(b, make([]byte, pad)...)
	}
	return b
}

// upstream is a fake resolver on 127.0.0.1 (UDP and TCP on the same port).
type upstream struct {
	addr   netip.AddrPort
	udpN   atomic.Int32
	tcpN   atomic.Int32
	answer func(q []byte, tcp bool) []byte // nil = no answer
}

func newUpstream(t *testing.T, answer func(q []byte, tcp bool) []byte) *upstream {
	t.Helper()
	pc, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:0")))
	if err != nil {
		t.Fatal(err)
	}
	port := pc.LocalAddr().(*net.UDPAddr).Port
	ln, err := net.Listen("tcp", net.JoinHostPort("127.0.0.1", strconv.Itoa(port)))
	if err != nil {
		t.Skip("tcp port taken:", err)
	}
	u := &upstream{addr: netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)), answer: answer}
	t.Cleanup(func() { _ = pc.Close(); _ = ln.Close() })
	go func() {
		buf := make([]byte, 4096)
		for {
			n, from, err := pc.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			u.udpN.Add(1)
			if r := u.answer(buf[:n], false); r != nil {
				_, _ = pc.WriteToUDPAddrPort(r, from)
			}
		}
	}()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				q, err := readTCPMsg(c)
				if err != nil {
					return
				}
				u.tcpN.Add(1)
				if r := u.answer(q, true); r != nil {
					_ = writeTCPMsg(c, r)
				}
			}()
		}
	}()
	return u
}

type clock struct{ t time.Time }

func (c *clock) now() time.Time { return c.t }

func newServer(clk *clock, ups ...netip.AddrPort) *Server {
	return New(Config{Upstreams: ups, Timeout: 300 * time.Millisecond, Log: slog.New(slog.DiscardHandler), Now: clk.now})
}

func TestForwardAndCache(t *testing.T) {
	up := newUpstream(t, func(q []byte, _ bool) []byte { return buildAnswer(q, rcodeOK, 300, 2, 0) })
	clk := &clock{t: time.Unix(1_000_000, 0)}
	s := newServer(clk, up.addr)
	ctx := context.Background()
	q := buildQuery(0x1234, "Example.COM", 0)
	r := s.Answer(ctx, q, false)
	if r == nil || binary.BigEndian.Uint16(r) != 0x1234 || rcode(r) != rcodeOK || binary.BigEndian.Uint16(r[6:]) != 2 {
		t.Fatalf("answer %x", r)
	}
	// Same question, other case and ID: from the cache with aged TTLs.
	clk.t = clk.t.Add(100 * time.Second)
	r2 := s.Answer(ctx, buildQuery(0x9999, "example.com", 0), false)
	if up.udpN.Load() != 1 {
		t.Fatalf("upstream asked %d times", up.udpN.Load())
	}
	offs, minTTL, err := answerTTLs(r2)
	if err != nil || len(offs) != 2 || minTTL != 200 || binary.BigEndian.Uint16(r2) != 0x9999 {
		t.Fatalf("cached ttl %d offs %v err %v", minTTL, offs, err)
	}
	// Expired: asked again.
	clk.t = clk.t.Add(201 * time.Second)
	s.Answer(ctx, q, false)
	if up.udpN.Load() != 2 {
		t.Fatalf("expired entry served (%d)", up.udpN.Load())
	}
	// EDNS DO bit is part of the key; a different name is a different key.
	s.Answer(ctx, buildQuery(1, "other.example", 0), false)
	if up.udpN.Load() != 3 || s.CacheLen() != 2 {
		t.Fatalf("asked %d, cache %d", up.udpN.Load(), s.CacheLen())
	}
}

func TestNegativeAndServfailCaching(t *testing.T) {
	var rc atomic.Int32
	up := newUpstream(t, func(q []byte, _ bool) []byte { return buildAnswer(q, int(rc.Load()), 0, 0, 0) })
	clk := &clock{t: time.Unix(1_000_000, 0)}
	s := newServer(clk, up.addr)
	ctx := context.Background()
	rc.Store(rcodeNX)
	if r := s.Answer(ctx, buildQuery(1, "nx.example", 0), false); rcode(r) != rcodeNX {
		t.Fatalf("rcode %d", rcode(r))
	}
	s.Answer(ctx, buildQuery(2, "nx.example", 0), false)
	if up.udpN.Load() != 1 {
		t.Fatal("NXDOMAIN not cached")
	}
	clk.t = clk.t.Add(emptyTTL)
	s.Answer(ctx, buildQuery(3, "nx.example", 0), false)
	if up.udpN.Load() != 2 {
		t.Fatal("NXDOMAIN cached past 30 s")
	}
	rc.Store(5) // REFUSED is passed on, never cached
	s.Answer(ctx, buildQuery(4, "refused.example", 0), false)
	s.Answer(ctx, buildQuery(5, "refused.example", 0), false)
	if up.udpN.Load() != 4 {
		t.Fatal("REFUSED cached")
	}
}

func TestFailoverAndServfail(t *testing.T) {
	dead := newUpstream(t, func([]byte, bool) []byte { return nil })
	up := newUpstream(t, func(q []byte, _ bool) []byte { return buildAnswer(q, rcodeOK, 60, 1, 0) })
	clk := &clock{t: time.Unix(1_000_000, 0)}
	s := newServer(clk, dead.addr, up.addr)
	ctx := context.Background()
	if r := s.Answer(ctx, buildQuery(1, "a.example", 0), false); rcode(r) != rcodeOK {
		t.Fatalf("rcode %d", rcode(r))
	}
	// The good upstream is asked first from now on.
	s.Answer(ctx, buildQuery(2, "b.example", 0), false)
	if dead.udpN.Load() != 1 || up.udpN.Load() != 2 {
		t.Fatalf("dead %d up %d", dead.udpN.Load(), up.udpN.Load())
	}
	s2 := newServer(clk, dead.addr)
	q := buildQuery(0x4242, "c.example", 0)
	r := s2.Answer(ctx, q, false)
	if r == nil || rcode(r) != rcodeFail || binary.BigEndian.Uint16(r) != 0x4242 || binary.BigEndian.Uint16(r[2:])&flagQR == 0 {
		t.Fatalf("servfail %x", r)
	}
	if s2.CacheLen() != 0 {
		t.Fatal("SERVFAIL cached")
	}
}

func TestTruncatedOverTCP(t *testing.T) {
	up := newUpstream(t, func(q []byte, tcp bool) []byte {
		if tcp {
			return buildAnswer(q, rcodeOK, 60, 1, 1500)
		}
		r := buildAnswer(q, rcodeOK, 60, 0, 0)
		binary.BigEndian.PutUint16(r[2:], binary.BigEndian.Uint16(r[2:])|flagTC)
		return r
	})
	clk := &clock{t: time.Unix(1_000_000, 0)}
	s := newServer(clk, up.addr)
	ctx := context.Background()
	// A UDP client gets the truncated answer (it retries over TCP itself); not cached.
	if r := s.Answer(ctx, buildQuery(1, "big.example", 0), false); !truncated(r) || s.CacheLen() != 0 {
		t.Fatalf("udp: tc %v cache %d", truncated(r), s.CacheLen())
	}
	r := s.Answer(ctx, buildQuery(2, "big.example", 0), true)
	if truncated(r) || len(r) < 1500 || up.tcpN.Load() != 1 {
		t.Fatalf("tcp: len %d tc %v tcp asks %d", len(r), truncated(r), up.tcpN.Load())
	}
	// Cached big answer is not sent to a 512-byte UDP client.
	if r := s.Answer(ctx, buildQuery(3, "big.example", 0), false); !truncated(r) {
		t.Fatal("oversized cached answer sent over UDP")
	}
	if r := s.Answer(ctx, buildQuery(4, "big.example", 4096), false); r == nil || truncated(r) {
		t.Fatal("EDNS client did not get the cached answer")
	}
}

func TestServeUDPAndTCP(t *testing.T) {
	up := newUpstream(t, func(q []byte, _ bool) []byte { return buildAnswer(q, rcodeOK, 60, 1, 0) })
	probe, err := net.ListenUDP("udp", net.UDPAddrFromAddrPort(netip.MustParseAddrPort("127.0.0.1:0")))
	if err != nil {
		t.Fatal(err)
	}
	listen := netip.MustParseAddrPort(probe.LocalAddr().String())
	_ = probe.Close()
	s := New(Config{Listen: []netip.AddrPort{listen}, Upstreams: []netip.AddrPort{up.addr}, Log: slog.New(slog.DiscardHandler)})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { s.Run(ctx); close(done) }()
	defer func() { cancel(); <-done }()

	var c net.Conn
	for range 50 {
		if c, err = net.Dial("tcp", listen.String()); err == nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if err != nil {
		t.Fatal(err)
	}
	_ = c.SetDeadline(time.Now().Add(3 * time.Second))
	if err := writeTCPMsg(c, buildQuery(7, "tcp.example", 0)); err != nil {
		t.Fatal(err)
	}
	r, err := readTCPMsg(c)
	_ = c.Close()
	if err != nil || binary.BigEndian.Uint16(r) != 7 || rcode(r) != rcodeOK {
		t.Fatalf("tcp answer %x %v", r, err)
	}
	u, err := net.Dial("udp", listen.String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = u.Close() }()
	_ = u.SetDeadline(time.Now().Add(3 * time.Second))
	if _, err := u.Write(buildQuery(8, "udp.example", 0)); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 512)
	n, err := u.Read(buf)
	if err != nil || binary.BigEndian.Uint16(buf) != 8 || rcode(buf[:n]) != rcodeOK {
		t.Fatalf("udp answer %x %v", buf[:n], err)
	}
	// Garbage is dropped, the server keeps going.
	_, _ = u.Write([]byte{1, 2, 3})
	if _, err := u.Write(buildQuery(9, "udp.example", 0)); err != nil {
		t.Fatal(err)
	}
	if n, err = u.Read(buf); err != nil || binary.BigEndian.Uint16(buf) != 9 {
		t.Fatalf("after garbage %x %v", buf[:n], err)
	}
}

func TestParseQuery(t *testing.T) {
	tests := []struct {
		name string
		msg  []byte
		ok   bool
		max  int
	}{
		{"plain", buildQuery(1, "a.b", 0), true, 512},
		{"edns", buildQuery(1, "a.b", 1232), true, 1232},
		{"small edns", buildQuery(1, "a.b", 100), true, 512},
		{"short", []byte{1, 2, 3}, false, 0},
		{"response", buildAnswer(buildQuery(1, "a.b", 0), 0, 1, 1, 0), false, 0},
		{"cut name", buildQuery(1, "abc.def", 0)[:15], false, 0},
	}
	for _, tc := range tests {
		q, err := parseQuery(tc.msg)
		if (err == nil) != tc.ok || (tc.ok && q.maxUDP != tc.max) {
			t.Errorf("%s: %+v %v", tc.name, q, err)
		}
	}
	// A pointer loop is rejected, not followed forever.
	loop := buildQuery(1, "a", 0)[:headerLen]
	loop = append(loop, 0xC0, headerLen, 0, 1, 0, 1)
	if _, err := parseQuery(loop); err == nil {
		t.Fatal("pointer loop accepted")
	}
}

func TestOnAnswer(t *testing.T) {
	up := newUpstream(t, func(q []byte, _ bool) []byte { return buildAnswer(q, rcodeOK, 60, 2, 0) })
	clk := &clock{t: time.Unix(1_000_000, 0)}
	type seen struct {
		name string
		ips  []netip.Addr
		ttl  time.Duration
	}
	var got []seen
	s := New(Config{Upstreams: []netip.AddrPort{up.addr}, Timeout: 300 * time.Millisecond,
		Log: slog.New(slog.DiscardHandler), Now: clk.now,
		OnAnswer: func(name string, ips []netip.Addr, ttl time.Duration) { got = append(got, seen{name, ips, ttl}) }})
	ctx := context.Background()
	s.Answer(ctx, buildQuery(1, "Bank.Example", 0), false)
	clk.t = clk.t.Add(10 * time.Second)
	s.Answer(ctx, buildQuery(2, "bank.example", 0), false) // cached: reported too, aged TTL
	if len(got) != 2 {
		t.Fatalf("OnAnswer calls %d", len(got))
	}
	zero := netip.AddrFrom4([4]byte{})
	for i, want := range []time.Duration{60 * time.Second, 50 * time.Second} {
		if got[i].name != "bank.example" || len(got[i].ips) != 2 || got[i].ips[0] != zero || got[i].ttl != want {
			t.Errorf("call %d: %+v", i, got[i])
		}
	}
	// NXDOMAIN or answers without A records are not reported.
	upNX := newUpstream(t, func(q []byte, _ bool) []byte { return buildAnswer(q, rcodeNX, 0, 0, 0) })
	s.cfg.Upstreams = []netip.AddrPort{upNX.addr}
	s.Answer(ctx, buildQuery(3, "nx.example", 0), false)
	if len(got) != 2 {
		t.Fatalf("NXDOMAIN reported")
	}
}

func TestAnswerA(t *testing.T) {
	q := buildQuery(1, "a.example", 0)
	if _, _, _, err := answerA(q[:5]); err == nil {
		t.Fatal("short message accepted")
	}
	name, ips, ttl, err := answerA(buildAnswer(q, rcodeOK, 30, 0, 0))
	if err != nil || name != "a.example" || len(ips) != 0 || ttl != 0 {
		t.Fatalf("empty answer: %q %v %d %v", name, ips, ttl, err)
	}
	// The first record is padded (not 4 bytes of RDATA): skipped.
	_, ips, ttl, err = answerA(buildAnswer(q, rcodeOK, 30, 3, 200))
	if err != nil || len(ips) != 2 || ttl != 30 {
		t.Fatalf("padded answer: %v %d %v", ips, ttl, err)
	}
}
