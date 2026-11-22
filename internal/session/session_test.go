package session

import (
	"bytes"
	"errors"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/wire"
)

type clock struct{ t time.Time }

func (c *clock) now() time.Time          { return c.t }
func (c *clock) advance(d time.Duration) { c.t = c.t.Add(d) }

// recorder is a Sender that keeps every packet.
type recorder struct {
	mu   sync.Mutex
	pkts [][]byte
	to   []netip.AddrPort
}

func (r *recorder) WriteToUDPAddrPort(b []byte, a netip.AddrPort) (int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pkts = append(r.pkts, append([]byte(nil), b...))
	r.to = append(r.to, a)
	return len(b), nil
}

func (r *recorder) last(t *testing.T) ([]byte, netip.AddrPort) {
	t.Helper()
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.pkts) == 0 {
		t.Fatal("nothing sent")
	}
	return r.pkts[len(r.pkts)-1], r.to[len(r.to)-1]
}

var (
	hubAddr  = netip.MustParseAddrPort("198.51.100.7:40000")
	hubAddr2 = netip.MustParseAddrPort("198.51.100.99:40001")
	srvAddr  = netip.MustParseAddrPort("203.0.113.1:51900")
)

type pair struct {
	clk          *clock
	hubKP, srvKP crypto.Keypair
	hub, srv     *Session
	hubOut       *recorder
	srvOut       *recorder
	dialer       *Dialer
}

func newPair(t *testing.T) *pair {
	t.Helper()
	hubKP, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	srvKP, err := crypto.GenerateKeypair()
	if err != nil {
		t.Fatal(err)
	}
	clk := &clock{t: time.Unix(1_700_000_000, 0)}
	p := &pair{clk: clk, hubKP: hubKP, srvKP: srvKP, hubOut: &recorder{}, srvOut: &recorder{}}
	p.dialer = &Dialer{Local: hubKP, Server: srvKP.Public, Payload: InitPayload{V: 1, SW: "test", HubName: "t1", Paths: []int{0}}}
	p.handshake(t)
	return p
}

// handshake runs one full handshake; the first one creates both sessions.
func (p *pair) handshake(t *testing.T) {
	t.Helper()
	init, err := p.dialer.Begin(0)
	if err != nil {
		t.Fatal(err)
	}
	keys, resp := p.respond(t, init)
	h, msg, err := wire.ParseHeader(resp)
	if err != nil {
		t.Fatal(err)
	}
	rp, hubKeys, err := p.dialer.Finish(h, msg)
	if err != nil {
		t.Fatalf("finish: %v", err)
	}
	if p.hub == nil {
		p.hub = New(rp.SessionID, Options{Now: p.clk.now})
		p.srv = New(rp.SessionID, Options{Now: p.clk.now})
		p.hub.SetPath(0, srvAddr, p.hubOut)
		p.srv.SetPath(0, hubAddr, p.srvOut)
	}
	p.hub.InstallKeys(hubKeys, true)
	p.srv.InstallKeys(keys, false)
}

// respond plays the server side of a handshake for the INIT packet.
func (p *pair) respond(t *testing.T, init []byte) (crypto.Keys, []byte) {
	t.Helper()
	h, msg, err := wire.ParseHeader(init)
	if err != nil || h.Type != wire.TypeHandshakeInit {
		t.Fatalf("init header %+v %v", h, err)
	}
	r, err := crypto.NewResponder(p.srvKP)
	if err != nil {
		t.Fatal(err)
	}
	raw, peer, err := r.ReadInit(msg)
	if err != nil || peer != p.hubKP.Public {
		t.Fatalf("read init: %v", err)
	}
	if ip, err := ParseInit(raw); err != nil || ip.HubName != "t1" {
		t.Fatalf("parse init %+v %v", ip, err)
	}
	resp, keys, err := Respond(r, h.PathID, RespPayload{V: 1, SessionID: 77, TunnelIP: "100.64.0.2", MTU: 1360})
	if err != nil {
		t.Fatal(err)
	}
	return keys, resp
}

// deliver opens pkt on s as if it arrived from addr.
func deliver(s *Session, pkt []byte, from netip.AddrPort, conn Sender) ([]wire.Frame, error) {
	h, _, err := wire.ParseHeader(pkt)
	if err != nil {
		return nil, err
	}
	return s.Open(h, pkt, from, conn)
}

func TestDataBothWaysAfterConfirmation(t *testing.T) {
	p := newPair(t)
	if err := p.srv.Send(&wire.Ping{TS: 1}); !errors.Is(err, ErrNoKeys) {
		t.Fatalf("server send before confirmation: %v", err)
	}
	if err := p.hub.Send(&wire.PathGone{PathID: 7}); err != nil {
		t.Fatal(err)
	}
	pkt, to := p.hubOut.last(t)
	if to != srvAddr {
		t.Fatalf("hub sent to %v", to)
	}
	frames, err := deliver(p.srv, pkt, hubAddr, p.srvOut)
	if err != nil {
		t.Fatal(err)
	}
	if g, ok := frames[0].(*wire.PathGone); !ok || g.PathID != 7 {
		t.Fatalf("frames %#v", frames)
	}
	if err := p.srv.Send(&wire.Close{Reason: 2}); err != nil {
		t.Fatalf("server send after confirmation: %v", err)
	}
	pkt, _ = p.srvOut.last(t)
	frames, err = deliver(p.hub, pkt, srvAddr, p.hubOut)
	if err != nil || frames[0].(*wire.Close).Reason != 2 {
		t.Fatalf("hub open: %#v %v", frames, err)
	}
	st := p.srv.Stats()
	if st.RxBytes == 0 || st.TxBytes == 0 || !st.LastRecv.Equal(p.clk.t) || len(st.Paths) != 1 {
		t.Fatalf("stats %+v", st)
	}
}

// TestIPThroughBondEngine checks that IP packets are numbered by the engine,
// consumed by Open and delivered in order, and that PINGs are answered.
func TestIPThroughBondEngine(t *testing.T) {
	var got [][]byte
	p := newPair(t)
	p.srv = New(p.srv.ID(), Options{Now: p.clk.now, Deliver: func(b []byte) { got = append(got, append([]byte(nil), b...)) }})
	keysFromPair(t, p)
	p.hub.AddPath(0, srvAddr, p.hubOut, true)
	p.hub.Bond().SetAggregation(false) // small packets would wait AggregateWait
	p.srv.Bond().SetAggregation(false)
	p.hub.SendIP([]byte{0x45, 1, 2})
	pkt, _ := p.hubOut.last(t)
	frames, err := deliver(p.srv, pkt, hubAddr, p.srvOut)
	if err != nil || len(frames) != 0 {
		t.Fatalf("open IP: %#v %v", frames, err)
	}
	if len(got) != 1 || !bytes.Equal(got[0], []byte{0x45, 1, 2}) {
		t.Fatalf("delivered %v", got)
	}
	// The server engine sends an ACK at once (first packet) on the path.
	ack, _ := p.srvOut.last(t)
	if _, err := deliver(p.hub, ack, srvAddr, p.hubOut); err != nil {
		t.Fatalf("hub open ack: %v", err)
	}
	if st := p.hub.Stats().Bond.Paths[0]; st.Inflight != 0 || st.TxData != 3 {
		t.Fatalf("hub path after ACK: inflight %d tx data %d", st.Inflight, st.TxData)
	}
	n := len(p.srvOut.pkts)
	if err := p.hub.Send(&wire.Ping{TS: 9}); err != nil {
		t.Fatal(err)
	}
	ping, _ := p.hubOut.last(t)
	if _, err := deliver(p.srv, ping, hubAddr, p.srvOut); err != nil {
		t.Fatal(err)
	}
	if len(p.srvOut.pkts) != n+1 {
		t.Fatal("PING not answered")
	}
}

// keysFromPair re-runs the handshake so a replaced server session gets keys
// and is confirmed by one hub packet.
func keysFromPair(t *testing.T, p *pair) {
	t.Helper()
	p.srv.SetPath(0, hubAddr, p.srvOut)
	p.handshake(t)
	if err := p.hub.Send(&wire.PathGone{PathID: 1}); err != nil {
		t.Fatal(err)
	}
	pkt, _ := p.hubOut.last(t)
	if _, err := deliver(p.srv, pkt, hubAddr, p.srvOut); err != nil {
		t.Fatalf("confirm keys: %v", err)
	}
}

func TestReplayAndTamperRejected(t *testing.T) {
	p := newPair(t)
	if err := p.hub.Send(&wire.PathGone{PathID: 1}); err != nil {
		t.Fatal(err)
	}
	pkt, _ := p.hubOut.last(t)
	if _, err := deliver(p.srv, pkt, hubAddr, p.srvOut); err != nil {
		t.Fatal(err)
	}
	if _, err := deliver(p.srv, pkt, hubAddr, p.srvOut); !errors.Is(err, ErrAuth) {
		t.Fatalf("replay: %v", err)
	}
	if err := p.hub.Send(&wire.PathGone{PathID: 1}); err != nil {
		t.Fatal(err)
	}
	pkt, _ = p.hubOut.last(t)
	pkt[len(pkt)-1] ^= 1
	if _, err := deliver(p.srv, pkt, hubAddr2, p.srvOut); !errors.Is(err, ErrAuth) {
		t.Fatalf("tampered: %v", err)
	}
	if got := p.srv.Stats().Paths[0].Addr; got != hubAddr {
		t.Fatalf("invalid packet moved the path to %v", got)
	}
	if _, err := p.srv.Open(wire.Header{}, []byte{1}, hubAddr, p.srvOut); !errors.Is(err, ErrAuth) {
		t.Fatalf("short packet: %v", err)
	}
}

func TestRoamingFollowsValidSource(t *testing.T) {
	p := newPair(t)
	if err := p.hub.Send(&wire.PathGone{PathID: 1}); err != nil {
		t.Fatal(err)
	}
	pkt, _ := p.hubOut.last(t)
	other := &recorder{}
	if _, err := deliver(p.srv, pkt, hubAddr2, other); err != nil {
		t.Fatal(err)
	}
	if err := p.srv.SendOn(0, &wire.PathGone{PathID: 1}); err != nil {
		t.Fatal(err)
	}
	if _, to := other.last(t); to != hubAddr2 {
		t.Fatalf("server answered to %v, want roamed %v", to, hubAddr2)
	}
}

func TestRekeyKeepsOldKeysForLatePackets(t *testing.T) {
	p := newPair(t)
	if err := p.hub.Send(&wire.PathGone{PathID: 1}); err != nil { // confirm first keys
		t.Fatal(err)
	}
	pkt, _ := p.hubOut.last(t)
	if _, err := deliver(p.srv, pkt, hubAddr, p.srvOut); err != nil {
		t.Fatal(err)
	}
	if err := p.hub.Send(&wire.PathGone{PathID: 0xAA}); err != nil { // late packet, old keys
		t.Fatal(err)
	}
	late, _ := p.hubOut.last(t)

	p.clk.advance(RekeyAfterTime)
	if !p.hub.NeedsRekey() {
		t.Fatal("NeedsRekey false after RekeyAfterTime")
	}
	p.handshake(t)
	if p.hub.NeedsRekey() {
		t.Fatal("NeedsRekey true right after rekey")
	}
	// Server still sends with the old (confirmed) keys; hub opens with prev.
	if err := p.srv.Send(&wire.PathGone{PathID: 1}); err != nil {
		t.Fatal(err)
	}
	pkt, _ = p.srvOut.last(t)
	if _, err := deliver(p.hub, pkt, srvAddr, p.hubOut); err != nil {
		t.Fatalf("hub open with prev keys: %v", err)
	}
	// Hub's first packet with new keys confirms them on the server.
	if err := p.hub.Send(&wire.PathGone{PathID: 1}); err != nil {
		t.Fatal(err)
	}
	pkt, _ = p.hubOut.last(t)
	if _, err := deliver(p.srv, pkt, hubAddr, p.srvOut); err != nil {
		t.Fatalf("server open with new keys: %v", err)
	}
	// The late old-key packet is still accepted within OldKeyLifetime...
	if _, err := deliver(p.srv, late, hubAddr, p.srvOut); err != nil {
		t.Fatalf("late packet with prev keys: %v", err)
	}
	// ...and server now sends with the new keys.
	if err := p.srv.Send(&wire.PathGone{PathID: 1}); err != nil {
		t.Fatal(err)
	}
	pkt, _ = p.srvOut.last(t)
	p.clk.advance(OldKeyLifetime)
	if _, err := deliver(p.hub, pkt, srvAddr, p.hubOut); err != nil {
		t.Fatalf("hub open with new keys: %v", err)
	}
}

func TestOldKeysExpire(t *testing.T) {
	p := newPair(t)
	if err := p.hub.Send(&wire.PathGone{PathID: 1}); err != nil {
		t.Fatal(err)
	}
	old, _ := p.hubOut.last(t)
	p.handshake(t)
	if err := p.hub.Send(&wire.PathGone{PathID: 1}); err != nil {
		t.Fatal(err)
	}
	pkt, _ := p.hubOut.last(t)
	if _, err := deliver(p.srv, pkt, hubAddr, p.srvOut); err != nil { // server: next → cur
		t.Fatal(err)
	}
	p.clk.advance(OldKeyLifetime)
	if _, err := deliver(p.srv, old, hubAddr, p.srvOut); !errors.Is(err, ErrAuth) {
		t.Fatalf("expired keys accepted: %v", err)
	}
}

func TestReplayedHandshakeDoesNotBreakSession(t *testing.T) {
	p := newPair(t)
	if err := p.hub.Send(&wire.PathGone{PathID: 1}); err != nil {
		t.Fatal(err)
	}
	pkt, _ := p.hubOut.last(t)
	if _, err := deliver(p.srv, pkt, hubAddr, p.srvOut); err != nil {
		t.Fatal(err)
	}
	// An attacker replays an INIT: the server installs unconfirmed keys only.
	init, err := (&Dialer{Local: p.hubKP, Server: p.srvKP.Public, Payload: p.dialer.Payload}).Begin(0)
	if err != nil {
		t.Fatal(err)
	}
	keys, _ := p.respond(t, init)
	p.srv.InstallKeys(keys, false)
	if err := p.srv.Send(&wire.PathGone{PathID: 1}); err != nil {
		t.Fatal(err)
	}
	pkt, _ = p.srvOut.last(t)
	if _, err := deliver(p.hub, pkt, srvAddr, p.hubOut); err != nil {
		t.Fatalf("hub lost the session after a replayed INIT: %v", err)
	}
}

func TestDialerAcceptsOnlyLatestAttempt(t *testing.T) {
	p := newPair(t)
	first, err := p.dialer.Begin(0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.dialer.Begin(0); err != nil {
		t.Fatal(err)
	}
	_, resp := p.respond(t, first)
	h, msg, _ := wire.ParseHeader(resp)
	if _, _, err := p.dialer.Finish(h, msg); err == nil {
		t.Fatal("response to a superseded attempt accepted")
	}
	// The stale response must not break the latest attempt.
	latest, err := p.dialer.Begin(0)
	if err != nil {
		t.Fatal(err)
	}
	_, stale := p.respond(t, first)
	sh, smsg, _ := wire.ParseHeader(stale)
	if _, _, err := p.dialer.Finish(sh, smsg); err == nil {
		t.Fatal("stale response accepted")
	}
	_, good := p.respond(t, latest)
	gh, gmsg, _ := wire.ParseHeader(good)
	if _, _, err := p.dialer.Finish(gh, gmsg); err != nil {
		t.Fatalf("latest response after a stale one: %v", err)
	}
	if _, _, err := (&Dialer{}).Finish(h, msg); err == nil {
		t.Fatal("Finish without Begin accepted")
	}
}

func TestSendWithoutPath(t *testing.T) {
	p := newPair(t)
	s := New(1, Options{})
	s.cur = p.hub.cur
	if err := s.Send(&wire.Ping{}); !errors.Is(err, ErrNoPath) {
		t.Fatalf("send without path: %v", err)
	}
	if err := s.SendOn(3, &wire.Ping{}); !errors.Is(err, ErrNoPath) {
		t.Fatalf("send on unknown path: %v", err)
	}
}

// streamRecorder is a recorder over a reliable stream transport.
type streamRecorder struct{ recorder }

func (*streamRecorder) Stream() bool { return true }

// TestStreamPathFlag: a path heard over a stream sender (TCP/WSS connection)
// is marked as a stream path in the bond engine, and back when it roams to UDP.
func TestStreamPathFlag(t *testing.T) {
	p := newPair(t)
	keysFromPair(t, p)
	stream := &streamRecorder{}
	tcpAddr := netip.MustParseAddrPort("192.0.2.9:40000")
	for _, tt := range []struct {
		from netip.AddrPort
		conn Sender
		want bool
	}{{tcpAddr, stream, true}, {hubAddr, p.srvOut, false}} {
		if err := p.hub.Send(&wire.PathGone{PathID: 9}); err != nil {
			t.Fatal(err)
		}
		pkt, _ := p.hubOut.last(t)
		if _, err := deliver(p.srv, pkt, tt.from, tt.conn); err != nil {
			t.Fatal(err)
		}
		if got := p.srv.Stats().Bond.Paths[0].Stream; got != tt.want {
			t.Fatalf("from %v: stream %v, want %v", tt.from, got, tt.want)
		}
	}
}

func TestPathCap(t *testing.T) {
	p := newPair(t)
	send := func(id uint8) error {
		p.hub.SetPath(id, srvAddr, p.hubOut)
		if err := p.hub.SendOn(id, &wire.PathGone{PathID: 250}); err != nil {
			t.Fatal(err)
		}
		pkt, _ := p.hubOut.last(t)
		_, err := deliver(p.srv, pkt, hubAddr, p.srvOut)
		return err
	}
	for id := 1; id < MaxPaths; id++ { // path 0 exists already
		if err := send(uint8(id)); err != nil {
			t.Fatalf("path %d: %v", id, err)
		}
	}
	if err := send(MaxPaths); !errors.Is(err, ErrPaths) {
		t.Fatalf("path %d over the cap: err %v, want ErrPaths", MaxPaths, err)
	}
	if err := send(3); err != nil {
		t.Fatalf("known path refused at the cap: %v", err)
	}
	p.srv.RemovePath(3)
	if err := send(MaxPaths); err != nil {
		t.Fatalf("new path after a removal: %v", err)
	}
}

func TestRoamingIgnoresOlderPacket(t *testing.T) {
	p := newPair(t)
	if err := p.hub.Send(&wire.PathGone{PathID: 1}); err != nil {
		t.Fatal(err)
	}
	old, _ := p.hubOut.last(t)
	old = append([]byte(nil), old...)
	if err := p.hub.Send(&wire.PathGone{PathID: 1}); err != nil {
		t.Fatal(err)
	}
	newer, _ := p.hubOut.last(t)
	if _, err := deliver(p.srv, newer, hubAddr2, p.srvOut); err != nil {
		t.Fatal(err)
	}
	if _, err := deliver(p.srv, old, hubAddr, p.srvOut); err != nil {
		t.Fatalf("late packet inside the replay window: %v", err)
	}
	if got := p.srv.Stats().Paths[0].Addr; got != hubAddr2 {
		t.Fatalf("a late packet moved the path back to %v", got)
	}
}

// TestSetPathStreamPorts: on a stream path, a new source port from the same
// IP (several TCP connections per path, phase 20) follows the connection
// but is not an address change (no PATH_INFO per packet); a new IP, a UDP
// port change and a stream/UDP switch are.
func TestSetPathStreamPorts(t *testing.T) {
	udp, stream := &recorder{}, &streamRecorder{}
	a1 := netip.MustParseAddrPort("192.0.2.9:40000")
	a2 := netip.MustParseAddrPort("192.0.2.9:40001")
	b := netip.MustParseAddrPort("192.0.2.10:40001")
	tests := []struct {
		name       string
		first      Sender
		next       netip.AddrPort
		conn       Sender
		wantChange bool
	}{
		{"stream port", stream, a2, &streamRecorder{}, false},
		{"stream same", stream, a1, stream, false},
		{"stream new ip", stream, b, stream, true},
		{"udp port", udp, a2, udp, true},
		{"stream to udp", stream, a2, udp, true},
		{"udp to stream", udp, a2, stream, true},
	}
	for _, tt := range tests {
		s := New(1, Options{})
		s.mu.Lock()
		if !s.setPathLocked(1, a1, tt.first) {
			t.Fatalf("%s: new path not reported", tt.name)
		}
		got := s.setPathLocked(1, tt.next, tt.conn)
		p := s.paths[1]
		s.mu.Unlock()
		if got != tt.wantChange {
			t.Errorf("%s: changed %v, want %v", tt.name, got, tt.wantChange)
		}
		if p.Conn != tt.conn || p.Addr != tt.next {
			t.Errorf("%s: path not moved to the newest connection", tt.name)
		}
	}
}
