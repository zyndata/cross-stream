package hub

import (
	"context"
	"errors"
	"net/netip"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/hub/netcfg"
	"github.com/zyndata/cross-stream/internal/hub/uplink"
)

// fakeProbe answers captive probes from a settable result and counts them.
type fakeProbe struct {
	mu    sync.Mutex
	next  uplink.Probe
	calls int
}

func (f *fakeProbe) set(p uplink.Probe) { f.mu.Lock(); f.next = p; f.mu.Unlock() }
func (f *fakeProbe) n() int             { f.mu.Lock(); defer f.mu.Unlock(); return f.calls }
func (f *fakeProbe) probe(context.Context, string) uplink.Probe {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls++
	return f.next
}

var portal = uplink.Probe{State: uplink.NetCaptive, URL: "http://192.0.2.80/login?mac=02:00:00:00:00:01"}

func newCaptiveDaemon(t *testing.T, present *[]uplink.Iface, fp *fakeProbe, routes *[]string) *Daemon {
	t.Helper()
	d := newTestDaemon(t, shortTemp(t), DaemonConfig{
		ScanUplinks: func() ([]uplink.Iface, error) { return *present, nil },
		UplinkControl: func(string) func(string, string, syscall.RawConn) error {
			return func(string, string, syscall.RawConn) error { return nil }
		},
		ProbeCaptive: fp.probe,
		PortalRoute: func(_ context.Context, r netcfg.HostRoute, add bool) error {
			if r.Dst == netip.MustParseAddr("192.0.2.99") {
				return errors.New("route refused")
			}
			op := "del "
			if add {
				op = "add "
			}
			*routes = append(*routes, op+r.Dst.String()+" dev "+r.Iface+" via "+r.GW.String())
			return nil
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	d.ctx = ctx
	return d
}

// settle waits until no probe of link id runs.
func settle(t *testing.T, d *Daemon, id string) {
	t.Helper()
	for range 200 {
		d.upMu.Lock()
		ci := d.up.captive[id]
		busy := ci != nil && ci.probing
		d.upMu.Unlock()
		if !busy {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatal("probe did not finish")
}

func running(d *Daemon, id string) bool {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.locals[id] != nil
}

func TestCaptiveUplink(t *testing.T) {
	present := []uplink.Iface{{Name: "usb0", MAC: "02:00:00:00:00:01", Kind: uplink.KindUSB,
		Addr: netip.MustParsePrefix("192.168.42.7/24"), Gateway: netip.MustParseAddr("192.168.42.129")}}
	fp := &fakeProbe{next: portal}
	var routes []string
	d := newCaptiveDaemon(t, &present, fp, &routes)
	d.uplinkScan(d.ctx)
	on := true
	if _, err := d.UplinksUpdate(uplink.Params{Uplink: "u1", Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	settle(t, d, "u1")
	// Probed when it came up: captive, path held down, portal URL shown.
	u := d.UplinksList()[0]
	if fp.n() != 1 || u.State != UplinkCaptive || running(d, "u1") || u.Captive == nil ||
		u.Captive.URL != portal.URL || !u.Captive.Rechecking {
		t.Fatalf("after start: probes %d, %+v captive %+v", fp.n(), u.State, u.Captive)
	}
	// Rechecked every 30 s, not sooner.
	now := time.Now()
	d.uplinkTick(now.Add(10 * time.Second))
	settle(t, d, "u1")
	if fp.n() != 1 {
		t.Fatalf("rechecked after 10 s (%d probes)", fp.n())
	}
	d.uplinkTick(now.Add(31 * time.Second))
	settle(t, d, "u1")
	if fp.n() != 2 || running(d, "u1") {
		t.Fatalf("recheck at 31 s: %d probes, running %v", fp.n(), running(d, "u1"))
	}
	// Logged in: the next recheck finds the internet, the path starts again.
	fp.set(uplink.Probe{State: uplink.NetOpen})
	d.uplinkTick(now.Add(62 * time.Second))
	settle(t, d, "u1")
	if u := d.UplinksList()[0]; !running(d, "u1") || u.State == UplinkCaptive || u.Captive.State != uplink.NetOpen {
		t.Fatalf("after login: running %v %+v", running(d, "u1"), u)
	}
	n := fp.n() // the restart does not probe again right away (fresh open result)
	if n != 3 {
		t.Fatalf("probes after login %d", n)
	}

	// A path that does not come alive is probed once after 15 s, then not
	// again until it was alive.
	t0 := time.Now()
	d.uplinkTick(t0)
	d.uplinkTick(t0.Add(10 * time.Second))
	settle(t, d, "u1")
	if fp.n() != n {
		t.Fatalf("probed a path before 15 s (%d)", fp.n())
	}
	d.uplinkTick(t0.Add(16 * time.Second))
	settle(t, d, "u1")
	d.uplinkTick(t0.Add(50 * time.Second))
	settle(t, d, "u1")
	if fp.n() != n+1 {
		t.Fatalf("dead path probes %d, want %d", fp.n(), n+1)
	}
}

func TestCaptiveGiveUpAndChange(t *testing.T) {
	present := []uplink.Iface{{Name: "usb0", MAC: "02:00:00:00:00:01", Kind: uplink.KindUSB,
		Addr: netip.MustParsePrefix("192.168.42.7/24"), Gateway: netip.MustParseAddr("192.168.42.129")}}
	fp := &fakeProbe{next: portal}
	var routes []string
	d := newCaptiveDaemon(t, &present, fp, &routes)
	d.uplinkScan(d.ctx)
	on := true
	if _, err := d.UplinksUpdate(uplink.Params{Uplink: "u1", Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	settle(t, d, "u1")
	now := time.Now()
	d.uplinkTick(now.Add(CaptiveGiveUp + time.Second)) // gives up
	settle(t, d, "u1")
	d.uplinkTick(now.Add(CaptiveGiveUp + time.Minute))
	settle(t, d, "u1")
	if u := d.UplinksList()[0]; fp.n() != 1 || u.Captive.Rechecking || u.State != UplinkCaptive {
		t.Fatalf("after 10 min: %d probes, %+v", fp.n(), u.Captive)
	}
	// Manual check with -open: probed now, portal host routed via the link.
	res, err := d.UplinksCaptive(d.ctx, CaptiveParams{Uplink: "u1", Open: true})
	if err != nil || res.State != uplink.NetCaptive || len(res.Route) != 1 || res.Route[0] != "192.0.2.80" ||
		res.Hint != "" || fp.n() != 2 {
		t.Fatalf("manual check %+v %v (probes %d)", res, err, fp.n())
	}
	if len(routes) != 1 || routes[0] != "add 192.0.2.80 dev usb0 via 192.168.42.129" {
		t.Fatalf("routes %q", routes)
	}
	if u := d.UplinksList()[0]; !u.Captive.Rechecking {
		t.Fatal("manual check did not restart the rechecks")
	}
	// A route that cannot be added leaves the hint.
	fp.set(uplink.Probe{State: uplink.NetCaptive, URL: "http://192.0.2.99/"})
	res, _ = d.UplinksCaptive(d.ctx, CaptiveParams{Uplink: "u1", Open: true})
	if res.RouteErr == "" || res.Hint != CaptiveHint {
		t.Fatalf("refused route: %+v", res)
	}
	// Stop removes the routes.
	d.upMu.Lock()
	d.dropPortalRoutesLocked()
	d.upMu.Unlock()
	if len(routes) != 2 || routes[1] != "del 192.0.2.80 dev usb0 via 192.168.42.129" {
		t.Fatalf("routes after stop %q", routes)
	}
	// Another address (re-plugged, new DHCP lease): tried again.
	fp.set(uplink.Probe{State: uplink.NetOpen})
	present[0].Addr = netip.MustParsePrefix("192.168.42.8/24")
	d.uplinkScan(d.ctx)
	settle(t, d, "u1")
	if u := d.UplinksList()[0]; !running(d, "u1") || u.State == UplinkCaptive || fp.n() != 4 {
		t.Fatalf("after change: running %v %+v probes %d", running(d, "u1"), u.State, fp.n())
	}
	if _, err := d.UplinksCaptive(d.ctx, CaptiveParams{Uplink: "nope"}); err == nil {
		t.Fatal("unknown link accepted")
	}
	present = nil
	d.uplinkScan(d.ctx)
	if _, err := d.UplinksCaptive(d.ctx, CaptiveParams{Uplink: "u1"}); err == nil {
		t.Fatal("absent link probed")
	}
}
