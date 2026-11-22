package server_test

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/server"
	"github.com/zyndata/cross-stream/internal/wire"
)

// fakeFW records the forward rules.
type fakeFW struct {
	mu    sync.Mutex
	rules []server.ForwardRule
	n     int
}

func (f *fakeFW) Setup(context.Context) error   { return nil }
func (f *fakeFW) Cleanup(context.Context) error { return nil }
func (f *fakeFW) Forward(_ context.Context, r []server.ForwardRule) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.rules, f.n = slices.Clone(r), f.n+1
	return nil
}

func (f *fakeFW) get() []server.ForwardRule {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.rules)
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for range 100 {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timeout: %s", what)
}

func TestPortForwards(t *testing.T) {
	fw := &fakeFW{}
	e := startServerFW(t, fw)
	enrollAs := func(name string) string {
		res, err := e.ctl.Enroll(e.ctx, name)
		if err != nil {
			t.Fatal(err)
		}
		dir, err := e.enrollHub(t, res.Code)
		if err != nil {
			t.Fatal(err)
		}
		return dir
	}
	d1, d2 := enrollAs("h1"), enrollAs("h2")
	if err := e.ctl.ForwardAllow(e.ctx, "h1", 2); err != nil {
		t.Fatal(err)
	}
	if err := e.ctl.ForwardAllow(e.ctx, "h2", 21); err == nil {
		t.Fatal("allowance over 20 accepted")
	}
	if err := e.ctl.ForwardAllow(e.ctx, "nope", 1); err == nil {
		t.Fatal("allowance for an unknown hub accepted")
	}
	_ = e.ctl.ForwardAllow(e.ctx, "h2", 5)
	_, _, _, cancel1, c1 := connectClient(t, e, d1)
	defer cancel1()
	tcp := func(p uint16) wire.Forward { return wire.Forward{Proto: wire.ProtoTCP, Port: p} }
	udp := func(p uint16) wire.Forward { return wire.Forward{Proto: wire.ProtoUDP, Port: p} }
	c1.SetForwards([]wire.Forward{tcp(8080), udp(41820), tcp(22), tcp(80), tcp(8081)})
	waitFor(t, "h1 FWD_ACK", func() bool { return c1.ForwardAck() != nil })
	a := c1.ForwardAck()
	reasons := map[uint16]uint8{}
	for _, r := range a.Refused {
		reasons[r.Port] = r.Reason
	}
	if !slices.Equal(a.Forwards, []wire.Forward{tcp(8080), udp(41820)}) || reasons[22] != wire.FwdOutOfRange ||
		reasons[80] != wire.FwdOutOfRange || reasons[8081] != wire.FwdNotAllowed {
		t.Fatalf("h1 ack %+v", a)
	}
	waitFor(t, "rules for h1", func() bool { return len(fw.get()) == 2 })
	if r := fw.get(); r[0].Port != 8080 || r[0].To.String() != "100.64.0.2" || r[1].Proto != wire.ProtoUDP {
		t.Fatalf("rules %+v", r)
	}
	hubs, _ := e.ctl.Hubs(e.ctx)
	if hubs[0].Name != "h1" || hubs[0].FwdAllow != 2 || len(hubs[0].Forwards) != 2 || hubs[0].Forwards[1].Proto != "udp" {
		t.Fatalf("hubs %+v", hubs)
	}

	// h2: a port h1 holds is refused as in use; the server's own UDP port is restricted.
	_, _, _, cancel2, c2 := connectClient(t, e, d2)
	defer cancel2()
	srvUDP := e.srv.Addrs()[0].Port()
	c2.SetForwards([]wire.Forward{tcp(8080), tcp(9000), udp(srvUDP)})
	waitFor(t, "h2 FWD_ACK", func() bool { return c2.ForwardAck() != nil })
	a = c2.ForwardAck()
	if !slices.Equal(a.Forwards, []wire.Forward{tcp(9000)}) || len(a.Refused) != 2 || a.Refused[0].Reason != wire.FwdInUse {
		t.Fatalf("h2 ack %+v", a)
	}
	if srvUDP >= server.FwdPortMin && srvUDP <= server.FwdPortMax && a.Refused[1].Reason != wire.FwdRestricted {
		t.Fatalf("server port: %+v", a.Refused[1])
	}
	waitFor(t, "rules for both hubs", func() bool { return len(fw.get()) == 3 })

	// Lowering the allowance re-evaluates the live session.
	if err := e.ctl.ForwardAllow(e.ctx, "h1", 1); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "h1 down to 1 port", func() bool { return len(fw.get()) == 2 })

	// Session end removes the hub's rules.
	if err := e.ctl.Revoke(e.ctx, "h2"); err != nil {
		t.Fatal(err)
	}
	waitFor(t, "h2 rules removed", func() bool { return len(fw.get()) == 1 })
	sess, _ := e.ctl.Sessions(e.ctx)
	if len(sess) != 1 || len(sess[0].Forwards) != 1 {
		t.Fatalf("sessions %+v", sess)
	}
}

func TestForwardScript(t *testing.T) {
	n := server.NFT{TunName: "xss0", Prefix: server.TunnelPrefix}
	if !strings.Contains(n.Ruleset(), "type nat hook prerouting priority dstnat") {
		t.Fatal("ruleset has no prerouting chain")
	}
	s := n.ForwardScript([]server.ForwardRule{{Proto: wire.ProtoUDP, Port: 51820, To: netip.MustParseAddr("100.64.0.3")}})
	want := "flush chain ip crossstream prerouting\nadd rule ip crossstream prerouting fib daddr type local udp dport 51820 dnat to 100.64.0.3:51820\n"
	if s != want {
		t.Fatalf("script:\n%s", s)
	}
}
