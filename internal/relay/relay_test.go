package relay

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"strconv"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/enroll"
	"github.com/zyndata/cross-stream/internal/pairing"
	"github.com/zyndata/cross-stream/internal/transport"
	"github.com/zyndata/cross-stream/internal/wire"
)

// fakeHub is a control listener, a hub data socket and a server socket on loopback.
type fakeHub struct {
	t      *testing.T
	key    crypto.Keypair
	ln     net.Listener
	data   *net.UDPConn
	server *net.UDPConn
	conns  chan *control.Conn
}

func newFakeHub(t *testing.T) *fakeHub {
	t.Helper()
	key, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	data := udp(t)
	server := udp(t)
	h := &fakeHub{t: t, key: key, ln: ln, data: data, server: server, conns: make(chan *control.Conn, 4)}
	t.Cleanup(func() { _ = ln.Close(); _ = data.Close(); _ = server.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			cc, err := control.Server(c, key)
			if err != nil {
				_ = c.Close()
				continue
			}
			h.conns <- cc
		}
	}()
	return h
}

func udp(t *testing.T) *net.UDPConn {
	t.Helper()
	c, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func (h *fakeHub) payload() pairing.Payload {
	tok, _ := enroll.NewToken()
	return pairing.Payload{V: 1, Hub: "hub1", IP: "127.0.0.1", CPort: h.ln.Addr().(*net.TCPAddr).Port,
		DPort: h.data.LocalAddr().(*net.UDPAddr).Port, HPK: crypto.EncodeKey(h.key.Public), Tok: tok.String()}
}

func (h *fakeHub) serverURI() string {
	return "udp://127.0.0.1:" + strconv.Itoa(h.server.LocalAddr().(*net.UDPAddr).Port)
}

func (h *fakeHub) accept() *control.Conn {
	h.t.Helper()
	select {
	case c := <-h.conns:
		h.t.Cleanup(func() { _ = c.Close() })
		return c
	case <-time.After(5 * time.Second):
		h.t.Fatal("relay did not connect")
		return nil
	}
}

// recvType reads control messages until one of type typ arrives.
func recvType(t *testing.T, c *control.Conn, typ string) control.Message {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		m, err := c.Recv()
		if err != nil {
			t.Fatalf("waiting for %s: %v", typ, err)
		}
		if m.Type() == typ {
			return m
		}
	}
}

func readUDP(t *testing.T, c *net.UDPConn) ([]byte, netip.AddrPort) {
	t.Helper()
	buf := make([]byte, 2048)
	_ = c.SetReadDeadline(time.Now().Add(5 * time.Second))
	n, from, err := c.ReadFromUDPAddrPort(buf)
	if err != nil {
		t.Fatal(err)
	}
	return buf[:n], netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
}

func noRead(t *testing.T, c *net.UDPConn) {
	t.Helper()
	_ = c.SetReadDeadline(time.Now().Add(200 * time.Millisecond))
	if n, _, err := c.ReadFromUDPAddrPort(make([]byte, 2048)); err == nil {
		t.Fatalf("unexpected %d-byte datagram", n)
	}
}

type runResult struct{ err error }

func start(t *testing.T, cfg Config) (*Relay, chan runResult) {
	cfg.ListenIP = loopback
	t.Helper()
	if cfg.Key.Public == ([crypto.KeyLen]byte{}) {
		k, err := crypto.GenerateKeypair()
		if err != nil {
			t.Fatal(err)
		}
		cfg.Key = k
	}
	if cfg.Name == "" {
		cfg.Name = "anna"
	}
	cfg.Log = slog.New(slog.DiscardHandler)
	r, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan runResult, 1)
	go func() { done <- runResult{r.Run(ctx)} }()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("Run did not return")
		}
	})
	return r, done
}

func waitErr(t *testing.T, done chan runResult) error {
	t.Helper()
	select {
	case r := <-done:
		done <- r // let the cleanup see it too
		return r.err
	case <-time.After(5 * time.Second):
		t.Fatal("Run did not return")
		return nil
	}
}

func outer(fill byte) []byte {
	return bytes.Repeat([]byte{fill}, wire.HeaderLen+20)
}

func TestRelayForwarding(t *testing.T) {
	h := newFakeHub(t)
	paired := make(chan control.Welcome, 1)
	r, _ := start(t, Config{Hub: h.payload(), Carrier: "Play", OnPaired: func(w control.Welcome) { paired <- w }})
	c := h.accept()
	hello := recvType(t, c, control.TypeHello).(*control.Hello)
	if hello.RelayName != "anna" || hello.Carrier != "Play" || hello.PairToken == "" {
		t.Fatalf("hello %+v", hello)
	}
	if err := c.Send(&control.Welcome{RelayID: "r1", PathID: 3, ServerEP: h.serverURI(), DataPort: h.data.LocalAddr().(*net.UDPAddr).Port}); err != nil {
		t.Fatal(err)
	}
	if w := <-paired; w.PathID != 3 {
		t.Fatalf("OnPaired %+v", w)
	}

	// First keepalive tells the hub the relay's data address.
	ka, relayData := readUDP(t, h.data)
	if !bytes.Equal(ka, []byte{byte(wire.RelayKeepalive), 3}) {
		t.Fatalf("keepalive % x", ka)
	}
	waitServerEP(t, r, h.serverURI())

	// hub → relay → server
	pkt := outer(0xAA)
	b1, _ := wire.AppendRelay(nil, wire.RelayMsg{Kind: wire.RelayToServer, PathID: 3, Payload: pkt})
	if _, err := h.data.WriteToUDPAddrPort(b1, relayData); err != nil {
		t.Fatal(err)
	}
	got, relayCell := readUDP(t, h.server)
	if !bytes.Equal(got, pkt) {
		t.Fatal("server got a different packet")
	}

	// server → relay → hub
	back := outer(0xBB)
	if _, err := h.server.WriteToUDPAddrPort(back, relayCell); err != nil {
		t.Fatal(err)
	}
	got, _ = readUDP(t, h.data)
	if want := append([]byte{byte(wire.RelayFromSrv), 3}, back...); !bytes.Equal(got, want) {
		t.Fatalf("hub got % x", got[:4])
	}

	// Filters: wrong path id, foreign sender on either side (the connected
	// cellular socket drops a foreign server in the kernel), short packet.
	dropped := r.Stats().Dropped
	wrongPath, _ := wire.AppendRelay(nil, wire.RelayMsg{Kind: wire.RelayToServer, PathID: 4, Payload: pkt})
	_, _ = h.data.WriteToUDPAddrPort(wrongPath, relayData)
	stranger := udp(t)
	defer func() { _ = stranger.Close() }()
	_, _ = stranger.WriteToUDPAddrPort(b1, relayData)
	noRead(t, h.server)
	_, _ = stranger.WriteToUDPAddrPort(back, relayCell)
	_, _ = h.server.WriteToUDPAddrPort([]byte{1, 2, 3}, relayCell)
	noRead(t, h.data)

	st := recvType(t, c, control.TypeStats).(*control.Stats)
	if st.BytesUp != uint64(len(pkt)) || st.BytesDown != uint64(len(back)) || st.Battery != 100 || !st.CellularOK {
		t.Fatalf("stats %+v", st)
	}
	s := r.Stats()
	if !s.Connected || s.RelayID != "r1" || s.PathID != 3 || s.ServerEP != h.serverURI() || s.Dropped-dropped != 3 ||
		s.PeriodUsed != uint64(len(pkt)+len(back)) {
		t.Fatalf("Stats %+v", s)
	}

	// A proxy request is answered with "not supported" (logged) and changes nothing.
	tok := make([]byte, control.ProxyTokenLen)
	if err := c.Send(&control.Mode{Mode: control.ModeDirect, Proxy: true, ProxyToken: b64(tok)}); err != nil {
		t.Fatal(err)
	}
	if err := r.SetLimits(control.Limits{CapMB: 10}); err != nil {
		t.Fatal(err)
	}
	if lu := recvType(t, c, control.TypeLimits).(*control.LimitsUpdate); lu.Limits.CapMB != 10 {
		t.Fatalf("limits %+v", lu)
	}

	// Stop tells the hub why.
	r.Stop(control.LeavingUser)
	if lv := recvType(t, c, control.TypeLeaving).(*control.Leaving); lv.Reason != control.LeavingUser {
		t.Fatalf("leaving %+v", lv)
	}
}

func TestRelayEndpointLaterAndReconnect(t *testing.T) {
	h := newFakeHub(t)
	start(t, Config{Hub: h.payload()})
	c := h.accept()
	recvType(t, c, control.TypeHello)
	dport := h.data.LocalAddr().(*net.UDPAddr).Port
	_ = c.Send(&control.Welcome{RelayID: "r1", PathID: 1, DataPort: dport})
	_, relayData := readUDP(t, h.data)

	// No server endpoint yet: hub packets are dropped.
	b1, _ := wire.AppendRelay(nil, wire.RelayMsg{Kind: wire.RelayToServer, PathID: 1, Payload: outer(1)})
	_, _ = h.data.WriteToUDPAddrPort(b1, relayData)
	noRead(t, h.server)

	_ = c.Send(&control.Endpoint{ServerEP: h.serverURI()})
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, _ = h.data.WriteToUDPAddrPort(b1, relayData)
		_ = h.server.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if _, _, err := h.server.ReadFromUDPAddrPort(make([]byte, 2048)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("endpoint message did not take effect")
		}
	}

	// Hub drops the connection: the relay reconnects, without a pair token now.
	_ = c.Close()
	c2 := h.accept()
	if hello := recvType(t, c2, control.TypeHello).(*control.Hello); hello.PairToken != "" {
		t.Fatal("pair token sent again after pairing")
	}
}

func TestRelayRejectAndForget(t *testing.T) {
	tests := []struct {
		name  string
		reply func(c *control.Conn, dport int)
		check func(err error) bool
	}{
		{"reject", func(c *control.Conn, _ int) { _ = c.Send(&control.Reject{Reason: control.RejectForgotten}) },
			func(err error) bool { // after the second reject: see the retry below
				var rej *RejectedError
				return errors.As(err, &rej) && rej.Reason == control.RejectForgotten
			}},
		{"forget after welcome", func(c *control.Conn, dport int) {
			_ = c.Send(&control.Welcome{RelayID: "r1", PathID: 1, DataPort: dport})
			_ = c.Send(&control.Forget{})
		}, func(err error) bool { return errors.Is(err, ErrForgotten) }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newFakeHub(t)
			_, done := start(t, Config{Hub: h.payload(), Paired: true})
			c := h.accept()
			if hello := recvType(t, c, control.TypeHello).(*control.Hello); hello.PairToken != "" {
				t.Fatal("paired relay sent a pair token")
			}
			tt.reply(c, h.data.LocalAddr().(*net.UDPAddr).Port)
			if tt.name == "reject" { // a forgotten relay tries once more, with the code's token
				c2 := h.accept()
				if hello := recvType(t, c2, control.TypeHello).(*control.Hello); hello.PairToken == "" {
					t.Fatal("no pair token on the retry after a reject")
				}
				tt.reply(c2, 0)
			}
			if err := waitErr(t, done); !tt.check(err) {
				t.Fatalf("Run returned %v", err)
			}
		})
	}
}

func TestRelayCapStop(t *testing.T) {
	h := newFakeHub(t)
	_, done := start(t, Config{Hub: h.payload(), Limits: control.Limits{CapMB: 1, UsedMB: 1}})
	c := h.accept()
	recvType(t, c, control.TypeHello)
	_ = c.Send(&control.Welcome{RelayID: "r1", PathID: 1, ServerEP: h.serverURI(), DataPort: h.data.LocalAddr().(*net.UDPAddr).Port})
	_, relayData := readUDP(t, h.data)
	b1, _ := wire.AppendRelay(nil, wire.RelayMsg{Kind: wire.RelayToServer, PathID: 1, Payload: outer(1)})
	_, _ = h.data.WriteToUDPAddrPort(b1, relayData)
	noRead(t, h.server)
	if lv := recvType(t, c, control.TypeLeaving).(*control.Leaving); lv.Reason != control.LeavingLimit {
		t.Fatalf("leaving %+v", lv)
	}
	var st *StoppedError
	if err := waitErr(t, done); !errors.As(err, &st) || st.Reason != control.LeavingLimit {
		t.Fatalf("Run returned %v", err)
	}
}

func TestRelayBatteryStop(t *testing.T) {
	h := newFakeHub(t)
	_, done := start(t, Config{Hub: h.payload(), Limits: control.Limits{BatteryMin: 30},
		Device: func() DeviceInfo { return DeviceInfo{Battery: 20} }})
	c := h.accept()
	recvType(t, c, control.TypeHello)
	_ = c.Send(&control.Welcome{RelayID: "r1", PathID: 1, DataPort: h.data.LocalAddr().(*net.UDPAddr).Port})
	if lv := recvType(t, c, control.TypeLeaving).(*control.Leaving); lv.Reason != control.LeavingBattery {
		t.Fatalf("leaving %+v", lv)
	}
	var st *StoppedError
	if err := waitErr(t, done); !errors.As(err, &st) {
		t.Fatalf("Run returned %v", err)
	}
}

func TestRelayStopWhileDisconnected(t *testing.T) {
	h := newFakeHub(t)
	p := h.payload()
	_ = h.ln.Close() // nothing listens: Run keeps retrying
	r, done := start(t, Config{Hub: p})
	time.Sleep(100 * time.Millisecond)
	r.Stop(control.LeavingUser)
	var st *StoppedError
	if err := waitErr(t, done); !errors.As(err, &st) || st.Reason != control.LeavingUser {
		t.Fatalf("Run returned %v", err)
	}
	if r.Stats().Connected {
		t.Fatal("stats say connected")
	}
}

func TestNewInvalid(t *testing.T) {
	h := newFakeHub(t)
	k, _ := crypto.GenerateKeypair()
	tests := []struct {
		name string
		cfg  Config
	}{
		{"no name", Config{Key: k, Hub: h.payload()}},
		{"bad payload", Config{Key: k, Name: "a", Hub: pairing.Payload{V: 1}}},
		{"bad limits", Config{Key: k, Name: "a", Hub: h.payload(), Limits: control.Limits{CapMB: -1}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.cfg); err == nil {
				t.Fatal("New accepted an invalid config")
			}
		})
	}
}

// A server name that does not resolve at welcome (DNS down after a hub
// outage) is retried until it does; the data path then works without a
// reconnect (phase 07b field bug).
func TestRelayEndpointResolveRetry(t *testing.T) {
	h := newFakeHub(t)
	var calls atomic.Int32
	lookup := func(context.Context, string, string) ([]netip.Addr, error) {
		if calls.Add(1) <= 2 {
			return nil, errors.New("no such host")
		}
		return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
	}
	start(t, Config{Hub: h.payload(), lookup: lookup, endpointRetry: 50 * time.Millisecond})
	c := h.accept()
	recvType(t, c, control.TypeHello)
	port := h.server.LocalAddr().(*net.UDPAddr).Port
	_ = c.Send(&control.Welcome{RelayID: "r1", PathID: 1, DataPort: h.data.LocalAddr().(*net.UDPAddr).Port,
		ServerEP: "udp://server.example.test:" + strconv.Itoa(port)})
	_, relayData := readUDP(t, h.data)

	b1, _ := wire.AppendRelay(nil, wire.RelayMsg{Kind: wire.RelayToServer, PathID: 1, Payload: outer(1)})
	deadline := time.Now().Add(3 * time.Second)
	for {
		_, _ = h.data.WriteToUDPAddrPort(b1, relayData)
		_ = h.server.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if _, _, err := h.server.ReadFromUDPAddrPort(make([]byte, 2048)); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("endpoint never resolved (%d lookups)", calls.Load())
		}
	}
	if calls.Load() < 3 {
		t.Fatalf("lookups %d", calls.Load())
	}
}

// waitServerEP waits until the relay's server connection to uri is up.
func waitServerEP(t *testing.T, r *Relay, uri string) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for r.Stats().ServerEP != uri {
		if time.Now().After(deadline) {
			t.Fatalf("server endpoint %q never up (now %q)", uri, r.Stats().ServerEP)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// Stream transports: the relay dials tcp:// and wss:// endpoints, forwards
// both ways, and switches when the hub sends another endpoint.
func TestRelayStreamEndpoints(t *testing.T) {
	h := newFakeHub(t)
	got := make(chan []byte, 16)
	sv := &transport.Server{Handler: func(pkt []byte, _ netip.AddrPort, c *transport.ServerConn) {
		got <- append([]byte(nil), pkt...)
		back := append([]byte(nil), pkt...)
		back[0] ^= 0xFF
		_, _ = c.WriteToUDPAddrPort(back, netip.AddrPort{})
	}}
	t.Cleanup(sv.Close)
	tcpLn, _ := net.Listen("tcp4", "127.0.0.1:0")
	wssLn, _ := net.Listen("tcp4", "127.0.0.1:0")
	cert, err := transport.LoadOrCreateCert(t.TempDir(), "")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = sv.ServeTCP(tcpLn) }()
	go func() { _ = sv.ServeWSS(wssLn, cert, "/xs") }()
	tcpURI := "tcp://127.0.0.1:" + strconv.Itoa(tcpLn.Addr().(*net.TCPAddr).Port)
	wssURI := "wss://127.0.0.1:" + strconv.Itoa(wssLn.Addr().(*net.TCPAddr).Port) + "/xs"

	r, _ := start(t, Config{Hub: h.payload()})
	c := h.accept()
	recvType(t, c, control.TypeHello)
	_ = c.Send(&control.Welcome{RelayID: "r1", PathID: 2, ServerEP: tcpURI, DataPort: h.data.LocalAddr().(*net.UDPAddr).Port})
	_, relayData := readUDP(t, h.data)
	for i, uri := range []string{tcpURI, wssURI} {
		if i > 0 {
			_ = c.Send(&control.Endpoint{ServerEP: uri})
		}
		waitServerEP(t, r, uri)
		pkt := outer(byte(0x10 + i))
		b1, _ := wire.AppendRelay(nil, wire.RelayMsg{Kind: wire.RelayToServer, PathID: 2, Payload: pkt})
		_, _ = h.data.WriteToUDPAddrPort(b1, relayData)
		select {
		case p := <-got:
			if !bytes.Equal(p, pkt) {
				t.Fatalf("%s: server got a different packet", uri)
			}
		case <-time.After(3 * time.Second):
			t.Fatalf("%s: nothing reached the server", uri)
		}
		for {
			back, _ := readUDP(t, h.data)
			if back[0] == byte(wire.RelayKeepalive) {
				continue
			}
			if back[0] != byte(wire.RelayFromSrv) || back[1] != 2 || back[2] != pkt[0]^0xFF {
				t.Fatalf("%s: hub got % x", uri, back[:4])
			}
			break
		}
	}
}

// loopback is where test relays bind their sockets (no firewall prompts).
var loopback = netip.MustParseAddr("127.0.0.1")

func TestListenAddr(t *testing.T) {
	tests := []struct {
		ip   netip.Addr
		port int
		want string
	}{
		{netip.Addr{}, 0, ":0"},
		{netip.Addr{}, 51903, ":51903"},
		{loopback, 0, "127.0.0.1:0"},
	}
	for _, tt := range tests {
		if got := listenAddr(tt.ip, tt.port); got != tt.want {
			t.Fatalf("listenAddr(%v, %d) = %q, want %q", tt.ip, tt.port, got, tt.want)
		}
	}
}

// countingListener counts accepted connections.
type countingListener struct {
	net.Listener
	n atomic.Int32
}

func (l *countingListener) Accept() (net.Conn, error) {
	c, err := l.Listener.Accept()
	if err == nil {
		l.n.Add(1)
	}
	return c, err
}

// TestRelayTCPConns (phase 20): the relay opens the number of TCP
// connections the hub asks for, reports them in stats, and a new number for
// the same endpoint resizes the set without redialing it; the local
// override wins over the hub.
func TestRelayTCPConns(t *testing.T) {
	tests := []struct {
		name               string
		override           int
		welcome, resize    int
		wantFirst, wantEnd int
		wantAccepted       int32
	}{
		{name: "hub decides", welcome: 2, resize: 3, wantFirst: 2, wantEnd: 3, wantAccepted: 3},
		{name: "shrink", welcome: 4, resize: 2, wantFirst: 4, wantEnd: 2, wantAccepted: 4},
		{name: "old hub, same endpoint again redials", welcome: 0, resize: 0, wantFirst: 1, wantEnd: 1, wantAccepted: 2},
		{name: "override", override: 1, welcome: 4, resize: 2, wantFirst: 1, wantEnd: 1, wantAccepted: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			h := newFakeHub(t)
			sv := &transport.Server{Handler: func([]byte, netip.AddrPort, *transport.ServerConn) {}}
			t.Cleanup(sv.Close)
			ln, _ := net.Listen("tcp4", "127.0.0.1:0")
			cl := &countingListener{Listener: ln}
			go func() { _ = sv.ServeTCP(cl) }()
			uri := "tcp://127.0.0.1:" + strconv.Itoa(ln.Addr().(*net.TCPAddr).Port)
			r, _ := start(t, Config{Hub: h.payload(), TCPConns: tt.override})
			c := h.accept()
			recvType(t, c, control.TypeHello)
			_ = c.Send(&control.Welcome{RelayID: "r1", PathID: 2, ServerEP: uri,
				DataPort: h.data.LocalAddr().(*net.UDPAddr).Port, TCPConns: tt.welcome})
			waitServerEP(t, r, uri)
			waitStatsConns := func(want int) {
				t.Helper()
				deadline := time.Now().Add(5 * time.Second)
				for {
					m := recvType(t, c, control.TypeStats).(*control.Stats)
					if m.TCPConns == want {
						return
					}
					if time.Now().After(deadline) {
						t.Fatalf("stats report %d connections, want %d", m.TCPConns, want)
					}
				}
			}
			waitStatsConns(tt.wantFirst)
			_ = c.Send(&control.Endpoint{ServerEP: uri, TCPConns: tt.resize})
			waitStatsConns(tt.wantEnd)
			if got := cl.n.Load(); got != tt.wantAccepted {
				t.Fatalf("server accepted %d connections, want %d (no redial)", got, tt.wantAccepted)
			}
		})
	}
}
