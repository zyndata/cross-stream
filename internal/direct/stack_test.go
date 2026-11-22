package direct

import (
	"bytes"
	"context"
	"crypto/subtle"
	"errors"
	"io"
	"log/slog"
	"net"
	"net/netip"
	"strings"
	"sync"
	"testing"
	"time"

	"gvisor.dev/gvisor/pkg/buffer"
	"gvisor.dev/gvisor/pkg/tcpip"
	"gvisor.dev/gvisor/pkg/tcpip/adapters/gonet"
	"gvisor.dev/gvisor/pkg/tcpip/header"
	"gvisor.dev/gvisor/pkg/tcpip/link/channel"
	"gvisor.dev/gvisor/pkg/tcpip/network/ipv4"
	"gvisor.dev/gvisor/pkg/tcpip/stack"
	"gvisor.dev/gvisor/pkg/tcpip/transport/tcp"
	"gvisor.dev/gvisor/pkg/tcpip/transport/udp"

	"github.com/zyndata/cross-stream/internal/wire"
)

var testToken = [wire.ProxyTokenLen]byte{1, 2, 3}

// fakeRelay is a relay proxy listener: it checks the request and connects
// every destination to one local echo server.
type fakeRelay struct {
	id   string
	ln   net.Listener
	echo string
	code byte // reply to send; ProxyOK = connect

	mu   sync.Mutex
	dsts []netip.AddrPort
}

func newFakeRelay(t *testing.T, id, echo string) *fakeRelay {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	r := &fakeRelay{id: id, ln: ln, echo: echo}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go r.serve(c)
		}
	}()
	return r
}

func (r *fakeRelay) serve(c net.Conn) {
	defer func() { _ = c.Close() }()
	buf := make([]byte, wire.ProxyRequestLen)
	if _, err := io.ReadFull(c, buf); err != nil {
		return
	}
	req, err := wire.ParseProxyRequest(buf)
	if err != nil || subtle.ConstantTimeCompare(req.Token[:], testToken[:]) != 1 {
		_, _ = c.Write([]byte{wire.ProxyNotAllowed})
		return
	}
	r.mu.Lock()
	r.dsts = append(r.dsts, req.Dst)
	code := r.code
	r.mu.Unlock()
	if code != wire.ProxyOK {
		_, _ = c.Write([]byte{code})
		return
	}
	out, err := net.Dial("tcp4", r.echo)
	if err != nil {
		_, _ = c.Write([]byte{wire.ProxyUnreachable})
		return
	}
	_, _ = c.Write([]byte{wire.ProxyOK})
	splice(c, out)
}

func (r *fakeRelay) relay() Relay {
	return Relay{ID: r.id, Proxy: r.ln.Addr().(*net.TCPAddr).AddrPort()}
}

func (r *fakeRelay) requests() []netip.AddrPort {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]netip.AddrPort(nil), r.dsts...)
}

func echoServer(t *testing.T) string {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() { _, _ = io.Copy(c, c); _ = c.Close() }()
		}
	}()
	return ln.Addr().String()
}

// env is a Stack wired to a client gVisor stack (10.0.0.2) that plays the
// LAN clients behind the TUN.
type env struct {
	s      *Stack
	client *stack.Stack
	mu     sync.Mutex
	relays []Relay
	udpOut chan wire.RelayMsg
}

func newEnv(t *testing.T, relays []Relay, udpIdle time.Duration, opts ...func(*Config)) *env {
	t.Helper()
	e := &env{relays: relays, udpOut: make(chan wire.RelayMsg, 16)}
	cep := channel.New(1024, DefaultMTU, "")
	e.client = stack.New(stack.Options{
		NetworkProtocols:   []stack.NetworkProtocolFactory{ipv4.NewProtocol},
		TransportProtocols: []stack.TransportProtocolFactory{tcp.NewProtocol, udp.NewProtocol},
	})
	if err := e.client.CreateNIC(1, cep); err != nil {
		t.Fatal(err)
	}
	pa := tcpip.ProtocolAddress{Protocol: ipv4.ProtocolNumber, AddressWithPrefix: tcpip.AddrFrom4([4]byte{10, 0, 0, 2}).WithPrefix()}
	if err := e.client.AddProtocolAddress(1, pa, stack.AddressProperties{}); err != nil {
		t.Fatal(err)
	}
	e.client.SetRouteTable([]tcpip.Route{{Destination: header.IPv4EmptySubnet, NIC: 1}})
	cfg := Config{
		Relays: func() []Relay { e.mu.Lock(); defer e.mu.Unlock(); return append([]Relay(nil), e.relays...) },
		Token:  func() [wire.ProxyTokenLen]byte { return testToken },
		SendUDP: func(relayID string, b []byte) error {
			m, err := wire.ParseRelay(b)
			if err != nil {
				return err
			}
			m.Payload = append([]byte(relayID+":"), m.Payload...)
			e.udpOut <- m
			return nil
		},
		Output: func(pkt []byte) {
			pb := stack.NewPacketBuffer(stack.PacketBufferOptions{Payload: buffer.MakeWithData(pkt)})
			cep.InjectInbound(header.IPv4ProtocolNumber, pb)
			pb.DecRef()
		},
		UDPIdle: udpIdle,
		Log:     slog.New(slog.DiscardHandler),
	}
	for _, o := range opts {
		o(&cfg)
	}
	s, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	e.s = s
	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		for {
			pb := cep.ReadContext(ctx)
			if pb == nil {
				return
			}
			v := pb.ToView()
			s.Inject(v.AsSlice())
			v.Release()
			pb.DecRef()
		}
	}()
	t.Cleanup(func() { cancel(); s.Close(); e.client.Close(); cep.Close(); e.client.Wait() })
	return e
}

func full(ap string) tcpip.FullAddress {
	a := netip.MustParseAddrPort(ap)
	return tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4(a.Addr().As4()), Port: a.Port()}
}

func (e *env) dial(t *testing.T, dst string) (*gonet.TCPConn, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	c, err := gonet.DialContextTCP(ctx, e.client, full(dst), ipv4.ProtocolNumber)
	if err == nil {
		t.Cleanup(func() { _ = c.Close() })
	}
	return c, err
}

func echoOnce(t *testing.T, c net.Conn, msg []byte) {
	t.Helper()
	if _, err := c.Write(msg); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(msg))
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := io.ReadFull(c, got); err != nil || !bytes.Equal(got, msg) {
		t.Fatalf("echo: %v", err)
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("timeout: " + what)
		}
	}
}

func TestStackTCPSpreadAndSticky(t *testing.T) {
	echo := echoServer(t)
	ra, rb, rc := newFakeRelay(t, "a", echo), newFakeRelay(t, "b", echo), newFakeRelay(t, "c", echo)
	e := newEnv(t, []Relay{ra.relay(), rb.relay(), rc.relay()}, 0)
	var conns []*gonet.TCPConn
	for _, dst := range []string{"93.184.216.34:80", "93.184.216.35:80", "93.184.216.36:443"} {
		c, err := e.dial(t, dst)
		if err != nil {
			t.Fatalf("dial %s: %v", dst, err)
		}
		echoOnce(t, c, bytes.Repeat([]byte(dst), 3000))
		conns = append(conns, c)
	}
	for _, r := range []*fakeRelay{ra, rb, rc} {
		if n := len(r.requests()); n != 1 {
			t.Fatalf("relay %s got %d flows, want 1 each", r.id, n)
		}
	}
	st := e.s.Stats()
	if st.TCP != 3 || st.Relays["a"] != 1 || st.Relays["b"] != 1 || st.Relays["c"] != 1 {
		t.Fatalf("stats %+v", st)
	}
	// Same destination IP, other port: sticks to relay a.
	c, err := e.dial(t, "93.184.216.34:8080")
	if err != nil {
		t.Fatal(err)
	}
	echoOnce(t, c, []byte("sticky"))
	if got := ra.requests(); len(got) != 2 || got[1] != netip.MustParseAddrPort("93.184.216.34:8080") {
		t.Fatalf("relay a requests %v", got)
	}
	// Client close ends the flow.
	_ = conns[1].Close()
	waitFor(t, "flow removed after close", func() bool { return e.s.Stats().Relays["b"] == 0 })
}

func TestStackTCPHalfClose(t *testing.T) {
	r := newFakeRelay(t, "a", echoServer(t))
	e := newEnv(t, []Relay{r.relay()}, 0)
	c, err := e.dial(t, "1.2.3.4:80")
	if err != nil {
		t.Fatal(err)
	}
	msg := bytes.Repeat([]byte("x"), 100_000)
	if _, err := c.Write(msg); err != nil {
		t.Fatal(err)
	}
	if err := c.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(c)
	if err != nil || len(got) != len(msg) {
		t.Fatalf("read %d bytes after half-close, err %v", len(got), err)
	}
}

func TestStackTCPRefused(t *testing.T) {
	r := newFakeRelay(t, "a", echoServer(t))
	r.code = wire.ProxyRefused
	e := newEnv(t, []Relay{r.relay()}, 0)
	if _, err := e.dial(t, "1.2.3.4:81"); err == nil {
		t.Fatal("dial succeeded although the relay refused")
	}
	e.mu.Lock()
	e.relays = nil
	e.mu.Unlock()
	if _, err := e.dial(t, "1.2.3.4:82"); err == nil {
		t.Fatal("dial succeeded with no relay")
	}
	if st := e.s.Stats(); st.TCP != 0 {
		t.Fatalf("stats %+v", st)
	}
}

func TestStackResetRelayAndOwns(t *testing.T) {
	echo := echoServer(t)
	ra, rb := newFakeRelay(t, "a", echo), newFakeRelay(t, "b", echo)
	e := newEnv(t, []Relay{ra.relay(), rb.relay()}, 0)
	ca, err := e.dial(t, "1.1.1.1:80")
	if err != nil {
		t.Fatal(err)
	}
	cb, err := e.dial(t, "2.2.2.2:80")
	if err != nil {
		t.Fatal(err)
	}
	echoOnce(t, ca, []byte("a"))
	echoOnce(t, cb, []byte("b"))
	src := netip.MustParseAddrPort(ca.LocalAddr().String())
	own := ipv4Packet(protoTCP, src.Addr().String(), "1.1.1.1", src.Port(), 80, 0)
	if !e.s.Owns(own) {
		t.Fatal("Owns = false for an open flow")
	}
	if e.s.Owns(ipv4Packet(protoTCP, src.Addr().String(), "1.1.1.1", src.Port(), 81, 0)) {
		t.Fatal("Owns = true for an unknown flow")
	}
	if n := e.s.ResetRelay("a"); n != 1 {
		t.Fatalf("ResetRelay = %d", n)
	}
	_ = ca.SetReadDeadline(time.Now().Add(5 * time.Second))
	if _, err := ca.Read(make([]byte, 1)); err == nil || errors.Is(err, io.EOF) {
		t.Fatalf("flow of the dead relay not reset: %v", err)
	}
	if e.s.Owns(own) {
		t.Fatal("Owns = true after reset")
	}
	echoOnce(t, cb, []byte("still up"))
	if n := e.s.ResetExcept(func(dst netip.Addr) bool { return dst == netip.MustParseAddr("2.2.2.2") }); n != 0 {
		t.Fatalf("ResetExcept ended a kept flow (%d)", n)
	}
	if n := e.s.ResetAll(); n != 1 {
		t.Fatalf("ResetAll = %d", n)
	}
}

func TestStackUDP(t *testing.T) {
	e := newEnv(t, []Relay{{ID: "a"}, {ID: "b"}}, 300*time.Millisecond)
	c, err := gonet.DialUDP(e.client, nil, &tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4([4]byte{9, 9, 9, 9}), Port: 53}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if _, err := c.Write([]byte("query")); err != nil {
		t.Fatal(err)
	}
	var m wire.RelayMsg
	select {
	case m = <-e.udpOut:
	case <-time.After(5 * time.Second):
		t.Fatal("no B4 datagram")
	}
	if m.Kind != wire.RelayUDPOut || m.Dst != netip.MustParseAddrPort("9.9.9.9:53") || string(m.Payload) != "a:query" {
		t.Fatalf("B4 %+v %q", m, m.Payload)
	}
	e.s.HandleUDP("b", m.FlowID, []byte("wrong relay")) // dropped
	e.s.HandleUDP("a", m.FlowID, []byte("answer"))
	buf := make([]byte, 100)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := c.Read(buf)
	if err != nil || string(buf[:n]) != "answer" {
		t.Fatalf("reply %q, %v", buf[:n], err)
	}
	if st := e.s.Stats(); st.UDP != 1 || st.Relays["a"] != 1 {
		t.Fatalf("stats %+v", st)
	}
	waitFor(t, "idle UDP flow closed", func() bool { return e.s.Stats().UDP == 0 })
}

func TestNewInvalid(t *testing.T) {
	if _, err := New(Config{}); err == nil {
		t.Fatal("New without callbacks succeeded")
	}
}

func TestDialProxyErrors(t *testing.T) {
	r := newFakeRelay(t, "a", echoServer(t))
	d := &net.Dialer{}
	ctx := context.Background()
	if _, err := DialProxy(ctx, d.DialContext, r.relay().Proxy, [wire.ProxyTokenLen]byte{9}, netip.MustParseAddrPort("1.2.3.4:80")); err == nil {
		t.Fatal("bad token accepted")
	} else if pe := (*ProxyError)(nil); !errors.As(err, &pe) || pe.Code != wire.ProxyNotAllowed {
		t.Fatalf("err %v", err)
	}
	if _, err := DialProxy(ctx, d.DialContext, r.relay().Proxy, testToken, netip.MustParseAddrPort("[::1]:80")); err == nil {
		t.Fatal("IPv6 destination accepted")
	}
	ln, _ := net.Listen("tcp4", "127.0.0.1:0")
	addr := ln.Addr().(*net.TCPAddr).AddrPort()
	_ = ln.Close()
	if _, err := DialProxy(ctx, d.DialContext, addr, testToken, netip.MustParseAddrPort("1.2.3.4:80")); err == nil {
		t.Fatal("dial to a closed relay port succeeded")
	}
}

// A local uplink (Relay.Dial) reaches the destination itself: the dial goes
// to the flow's real destination, rewritten here to local test servers.
func TestStackLocalUplink(t *testing.T) {
	tcpEcho := echoServer(t)
	pc, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = pc.Close() }()
	go func() {
		buf := make([]byte, 100)
		for {
			n, from, err := pc.ReadFrom(buf)
			if err != nil {
				return
			}
			_, _ = pc.WriteTo(append([]byte("re:"), buf[:n]...), from)
		}
	}()
	var mu sync.Mutex
	var dialed []string
	dial := func(ctx context.Context, network, addr string) (net.Conn, error) {
		mu.Lock()
		dialed = append(dialed, network+" "+addr)
		mu.Unlock()
		var d net.Dialer
		if network == "udp4" {
			return d.DialContext(ctx, network, pc.LocalAddr().String())
		}
		return d.DialContext(ctx, network, tcpEcho)
	}
	e := newEnv(t, []Relay{{ID: "u1", Dial: dial}}, 300*time.Millisecond)
	c, err := e.dial(t, "203.0.113.5:80")
	if err != nil {
		t.Fatal(err)
	}
	echoOnce(t, c, []byte("hello local"))
	u, err := gonet.DialUDP(e.client, nil, &tcpip.FullAddress{NIC: 1, Addr: tcpip.AddrFrom4([4]byte{9, 9, 9, 9}), Port: 53}, ipv4.ProtocolNumber)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = u.Close() }()
	if _, err := u.Write([]byte("q")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 100)
	_ = u.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, err := u.Read(buf)
	if err != nil || string(buf[:n]) != "re:q" {
		t.Fatalf("udp reply %q, %v", buf[:n], err)
	}
	select {
	case m := <-e.udpOut:
		t.Fatalf("local flow sent a relay datagram %+v", m)
	default:
	}
	mu.Lock()
	got := strings.Join(dialed, ",")
	mu.Unlock()
	if got != "tcp4 203.0.113.5:80,udp4 9.9.9.9:53" {
		t.Fatalf("dialed %s", got)
	}
	waitFor(t, "idle local UDP flow closed", func() bool { return e.s.Stats().UDP == 0 })
}

func TestStackIntercept(t *testing.T) {
	tcpEcho := echoServer(t)
	pepDial := func(ctx context.Context, _, _ string) (net.Conn, error) {
		var d net.Dialer
		return d.DialContext(ctx, "tcp4", tcpEcho)
	}
	claim := func(k FlowKey) (Relay, bool) {
		return Relay{ID: "pep", Dial: pepDial}, k.Dst.Port() == 443
	}
	e := newEnv(t, nil, time.Minute, func(c *Config) { c.Intercept = claim })
	if _, err := e.dial(t, "203.0.113.5:80"); err == nil {
		t.Fatal("unclaimed flow connected without a relay")
	}
	c, err := e.dial(t, "203.0.113.5:443")
	if err != nil {
		t.Fatal(err)
	}
	echoOnce(t, c, []byte("hello pep"))
	src := netip.MustParseAddrPort(c.LocalAddr().String())
	pkt := make([]byte, 24)
	pkt[0], pkt[9] = 0x45, protoTCP
	copy(pkt[12:16], src.Addr().AsSlice())
	copy(pkt[16:20], []byte{203, 0, 113, 5})
	pkt[20], pkt[21], pkt[22], pkt[23] = byte(src.Port()>>8), byte(src.Port()), 443>>8, 443&0xff
	if !e.s.OwnsIntercepted(pkt) {
		t.Error("OwnsIntercepted false for the intercepted flow's packet")
	}
	pkt[23]++ // another port: not a flow
	if e.s.OwnsIntercepted(pkt) {
		t.Error("OwnsIntercepted true for an unknown flow")
	}
	if st := e.s.Stats(); st.Intercepted != 1 || st.TCP != 0 || len(st.Relays) != 0 {
		t.Fatalf("stats %+v, want 1 intercepted, no DIRECT flows", st)
	}
	if n := e.s.ResetAll() + e.s.ResetExcept(func(netip.Addr) bool { return false }); n != 0 {
		t.Fatalf("DIRECT resets ended %d intercepted flows", n)
	}
	if n := e.s.ResetRelay("pep"); n != 1 {
		t.Fatalf("ResetRelay(pep) ended %d flows, want 1", n)
	}
	if e.s.icp.Load() != 0 {
		t.Errorf("intercepted counter %d after reset", e.s.icp.Load())
	}
}
