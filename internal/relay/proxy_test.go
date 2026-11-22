package relay

import (
	"bytes"
	"encoding/base64"
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/wire"
)

// proxyEnv is a relay connected to a fake hub with the proxy role on.
type proxyEnv struct {
	h     *fakeHub
	r     *Relay
	ctl   *control.Conn
	data  netip.AddrPort // relay's Wi-Fi data socket
	port  int            // proxy listener
	token [wire.ProxyTokenLen]byte
}

func newProxyEnv(t *testing.T, cfg Config) *proxyEnv {
	t.Helper()
	h := newFakeHub(t)
	cfg.Hub, cfg.ProxyPort, cfg.allowLoopback, cfg.ListenIP = h.payload(), -1, true, loopback
	r, _ := start(t, cfg)
	c := h.accept()
	recvType(t, c, control.TypeHello)
	if err := c.Send(&control.Welcome{RelayID: "r1", PathID: 1, DataPort: h.data.LocalAddr().(*net.UDPAddr).Port}); err != nil {
		t.Fatal(err)
	}
	_, data := readUDP(t, h.data) // keepalive
	e := &proxyEnv{h: h, r: r, ctl: c, data: data}
	for i := range e.token {
		e.token[i] = byte(i + 1)
	}
	e.mode(t, true)
	return e
}

// mode sends a mode message and waits until the proxy role follows it.
func (e *proxyEnv) mode(t *testing.T, on bool) {
	t.Helper()
	m := &control.Mode{Mode: control.ModeDirect, Proxy: on}
	if on {
		m.ProxyToken = base64.StdEncoding.EncodeToString(e.token[:])
	} else {
		m.Mode = control.ModeBonded
	}
	if err := e.ctl.Send(m); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for (e.r.Stats().ProxyPort != 0) != on {
		if time.Now().After(deadline) {
			t.Fatalf("proxy role on=%v not applied", on)
		}
		time.Sleep(5 * time.Millisecond)
	}
	e.port = e.r.Stats().ProxyPort
}

// open sends a proxy request and returns the connection and the reply code.
func (e *proxyEnv) open(t *testing.T, tok [wire.ProxyTokenLen]byte, dst netip.AddrPort) (net.Conn, byte) {
	t.Helper()
	c, err := net.Dial("tcp4", netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(e.port)).String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })
	req, err := wire.AppendProxyRequest(nil, wire.ProxyRequest{Token: tok, Dst: dst})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Write(req); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	code := make([]byte, 1)
	if _, err := io.ReadFull(c, code); err != nil {
		t.Fatalf("reply: %v", err)
	}
	_ = c.SetReadDeadline(time.Time{})
	return c, code[0]
}

// echoTCP serves one echo per connection; it closes after the peer's EOF.
func echoTCP(t *testing.T) netip.AddrPort {
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
	return ln.Addr().(*net.TCPAddr).AddrPort()
}

func TestProxyTCP(t *testing.T) {
	e := newProxyEnv(t, Config{})
	c, code := e.open(t, e.token, echoTCP(t))
	if code != wire.ProxyOK {
		t.Fatalf("reply %#x", code)
	}
	msg := bytes.Repeat([]byte("hello "), 5000) // several chunks
	if _, err := c.Write(msg); err != nil {
		t.Fatal(err)
	}
	if err := c.(*net.TCPConn).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	got, err := io.ReadAll(c) // EOF only after the half-close went through
	if err != nil || !bytes.Equal(got, msg) {
		t.Fatalf("echo %d bytes, err %v", len(got), err)
	}
	st := e.r.Stats()
	if st.BytesUp < uint64(len(msg)) || st.BytesDown < uint64(len(msg)) || st.PeriodUsed < 2*uint64(len(msg)) {
		t.Fatalf("not counted: %+v", st)
	}
}

func TestProxyTCPRefused(t *testing.T) {
	e := newProxyEnv(t, Config{})
	echo := echoTCP(t)
	ln, _ := net.Listen("tcp4", "127.0.0.1:0")
	closed := ln.Addr().(*net.TCPAddr).AddrPort()
	_ = ln.Close()
	var bad [wire.ProxyTokenLen]byte
	tests := []struct {
		name string
		tok  [wire.ProxyTokenLen]byte
		dst  netip.AddrPort
		want byte
	}{
		{"bad token", bad, echo, wire.ProxyNotAllowed},
		{"multicast", e.token, netip.MustParseAddrPort("224.0.0.1:80"), wire.ProxyNotAllowed},
		{"unspecified", e.token, netip.MustParseAddrPort("0.0.0.0:80"), wire.ProxyNotAllowed},
		{"refused", e.token, closed, wire.ProxyRefused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, code := e.open(t, tt.tok, tt.dst)
			if code != tt.want {
				t.Fatalf("reply %#x, want %#x", code, tt.want)
			}
			_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
			if _, err := c.Read(make([]byte, 1)); err != io.EOF {
				t.Fatalf("connection not closed after refusal: %v", err)
			}
		})
	}
}

func TestProxyTCPBadAddressType(t *testing.T) {
	e := newProxyEnv(t, Config{})
	c, err := net.Dial("tcp4", netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(e.port)).String())
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	req := append(append([]byte(nil), e.token[:]...), 4, 1, 2, 3, 4, 0, 80) // atyp 4, 39 bytes
	if _, err := c.Write(req); err != nil {
		t.Fatal(err)
	}
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	code := make([]byte, 1)
	if _, err := io.ReadFull(c, code); err != nil || code[0] != wire.ProxyBadAddress {
		t.Fatalf("reply %#x, %v", code[0], err)
	}
}

func TestProxyOnlyFromHub(t *testing.T) {
	e := newProxyEnv(t, Config{})
	d := net.Dialer{LocalAddr: &net.TCPAddr{IP: net.IPv4(127, 0, 0, 2)}}
	c, err := d.Dial("tcp4", netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(e.port)).String())
	if err != nil {
		t.Skipf("127.0.0.2 not usable here: %v", err)
	}
	defer func() { _ = c.Close() }()
	_ = c.SetReadDeadline(time.Now().Add(2 * time.Second))
	if n, err := c.Read(make([]byte, 1)); n != 0 || err == nil {
		t.Fatalf("connection from another host was served: %d, %v", n, err)
	}
}

func TestProxyTCPIdle(t *testing.T) {
	e := newProxyEnv(t, Config{proxyIdle: 300 * time.Millisecond})
	c, code := e.open(t, e.token, echoTCP(t))
	if code != wire.ProxyOK {
		t.Fatalf("reply %#x", code)
	}
	_ = c.SetReadDeadline(time.Now().Add(3 * time.Second))
	start := time.Now()
	if _, err := c.Read(make([]byte, 1)); err == nil {
		t.Fatal("idle connection got data")
	} else if ne, ok := err.(net.Error); ok && ne.Timeout() {
		t.Fatal("idle connection was not closed")
	}
	if d := time.Since(start); d < 250*time.Millisecond {
		t.Fatalf("closed after %v, before the idle timeout", d)
	}
}

// echoUDP echoes datagrams.
func echoUDP(t *testing.T) netip.AddrPort {
	t.Helper()
	c := udp(t)
	t.Cleanup(func() { _ = c.Close() })
	go func() {
		buf := make([]byte, 2048)
		for {
			n, from, err := c.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			_, _ = c.WriteToUDPAddrPort(buf[:n], from)
		}
	}()
	return c.LocalAddr().(*net.UDPAddr).AddrPort()
}

func TestProxyUDP(t *testing.T) {
	e := newProxyEnv(t, Config{proxyIdle: 400 * time.Millisecond})
	dst := echoUDP(t)
	b4, _ := wire.AppendRelay(nil, wire.RelayMsg{Kind: wire.RelayUDPOut, FlowID: 77, Dst: dst, Payload: []byte("query")})
	if _, err := e.h.data.WriteToUDPAddrPort(b4, e.data); err != nil {
		t.Fatal(err)
	}
	got, _ := readUDPKind(t, e.h.data, wire.RelayUDPIn)
	m, err := wire.ParseRelay(got)
	if err != nil || m.FlowID != 77 || string(m.Payload) != "query" {
		t.Fatalf("B5 %+v, %v", m, err)
	}
	if n := e.r.Stats().ProxyFlows; n != 1 {
		t.Fatalf("flows = %d, want 1", n)
	}
	// B4 from another socket than the hub's data socket is dropped.
	other := udp(t)
	defer func() { _ = other.Close() }()
	before := e.r.Stats().Dropped
	if _, err := other.WriteToUDPAddrPort(b4, e.data); err != nil {
		t.Fatal(err)
	}
	for deadline := time.Now().Add(2 * time.Second); e.r.Stats().Dropped == before; time.Sleep(5 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("B4 from another socket was not dropped")
		}
	}
	// Idle flows close.
	deadline := time.Now().Add(5 * time.Second)
	for e.r.Stats().ProxyFlows != 0 {
		if time.Now().After(deadline) {
			t.Fatal("idle UDP flow not closed")
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func TestProxyOff(t *testing.T) {
	e := newProxyEnv(t, Config{})
	port := e.port
	e.mode(t, false)
	if c, err := net.DialTimeout("tcp4", netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)).String(), time.Second); err == nil {
		_ = c.Close()
		t.Fatal("proxy listener still open after proxy=false")
	}
	b4, _ := wire.AppendRelay(nil, wire.RelayMsg{Kind: wire.RelayUDPOut, FlowID: 1, Dst: echoUDP(t), Payload: []byte("x")})
	before := e.r.Stats().Dropped
	if _, err := e.h.data.WriteToUDPAddrPort(b4, e.data); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for e.r.Stats().Dropped == before {
		if time.Now().After(deadline) {
			t.Fatal("B4 with the proxy role off was not dropped")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// readUDPKind reads datagrams until one of the given relay kind arrives
// (keepalives are skipped).
func readUDPKind(t *testing.T, c *net.UDPConn, k wire.RelayKind) ([]byte, netip.AddrPort) {
	t.Helper()
	for {
		b, from := readUDP(t, c)
		if len(b) > 0 && wire.RelayKind(b[0]) == k {
			return b, from
		}
	}
}

func TestAllowedDst(t *testing.T) {
	tests := []struct {
		dst  string
		want bool
	}{
		{"1.1.1.1:53", true},
		{"10.1.2.3:443", true},
		{"1.1.1.1:0", false},
		{"127.0.0.1:80", false},
		{"0.0.0.0:80", false},
		{"224.0.0.251:5353", false},
		{"169.254.1.1:80", false},
		{"255.255.255.255:67", false},
		{"[2001:db8::1]:80", false},
	}
	for _, tt := range tests {
		if got := allowedDst(netip.MustParseAddrPort(tt.dst)); got != tt.want {
			t.Errorf("allowedDst(%s) = %v, want %v", tt.dst, got, tt.want)
		}
	}
}
