package hub

import (
	"context"
	"errors"
	"log/slog"
	"math"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/pairing"
	"github.com/zyndata/cross-stream/internal/relay"
	"github.com/zyndata/cross-stream/internal/tun"
)

func freePort(t *testing.T) int {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	return ln.Addr().(*net.TCPAddr).Port
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for deadline := time.Now().Add(5 * time.Second); !cond(); time.Sleep(10 * time.Millisecond) {
		if time.Now().After(deadline) {
			t.Fatal("timeout: " + what)
		}
	}
}

// TestDirectModeActions: an enrolled hub (no server reachable) with one
// in-process relay; entering DIRECT turns the relay's proxy role on, leaving
// it with no direct flows turns it off.
// startHubWithRelay runs an enrolled daemon (pipe TUN, server unreachable)
// with one relay "anna" that has the proxy role on port.
func startHubWithRelay(t *testing.T) (*Daemon, *relay.Relay, int) {
	t.Helper()
	dir := t.TempDir()
	srv, _ := crypto.GenerateKeypair()
	if err := SaveEnrollment(dir, Enrollment{Name: "hub1", ServerKey: crypto.EncodeKey(srv.Public),
		Endpoints: []string{"udp://127.0.0.1:9"}, TunnelIP: "100.64.0.2", MTU: 1280}); err != nil {
		t.Fatal(err)
	}
	port := freePort(t)
	d, err := NewDaemon(DaemonConfig{Dir: dir, CtlPort: -1, DataPort: -1, ProxyPort: port, Version: "test",
		LanPrefix: netip.MustParsePrefix("127.0.0.1/8"), Log: slog.New(slog.DiscardHandler),
		NewTUN: func(c tun.Config) (tun.Device, error) {
			dev, _ := tun.Pipe(c.Name, "app", c.MTU)
			return dev, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	dDone := make(chan error, 1)
	go func() { dDone <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-dDone; err != nil {
			t.Errorf("daemon run: %v", err)
		}
	})

	code, err := d.PairCode()
	if err != nil {
		t.Fatal(err)
	}
	p, _ := pairing.Decode(code)
	rkey, _ := crypto.GenerateKeypair()
	r, err := relay.New(relay.Config{Key: rkey, Hub: p, Name: "anna", ProxyPort: port, ListenIP: netip.MustParseAddr("127.0.0.1"), Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	rDone := make(chan error, 1)
	go func() { rDone <- r.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-rDone; err != nil && !errors.Is(err, context.Canceled) {
			t.Errorf("relay run: %v", err)
		}
	})
	eventually(t, "relay connected with data address", func() bool {
		st := d.Status()
		return len(st.Relays) == 1 && st.Relays[0].DataAddr != ""
	})
	return d, r, port
}

func TestDirectModeActions(t *testing.T) {
	d, r, port := startHubWithRelay(t)
	if d.directStack() == nil || d.Status().Direct == nil {
		t.Fatal("enrolled hub has no DIRECT netstack")
	}

	d.applyMode(time.Now(), ModeConnecting, ModeDirect)
	eventually(t, "relay proxy role on", func() bool { return r.Stats().ProxyPort == port })
	if st := d.Status(); !st.Direct.Proxy || st.Direct.Draining || d.route.Load() != routeDirect {
		t.Fatalf("direct status %+v route %d", st.Direct, d.route.Load())
	}
	rels := d.directRelays()
	if len(rels) != 1 || rels[0].Proxy != netip.AddrPortFrom(netip.MustParseAddr("127.0.0.1"), uint16(port)) || rels[0].Backup {
		t.Fatalf("direct relays %+v", rels)
	}

	// Owner settings: backup priority → backup tier; both directions off → unused.
	bk, off := "backup", "off"
	if _, err := d.SetPath("anna", PathParams{Priority: &bk}); err != nil {
		t.Fatal(err)
	}
	if rels := d.directRelays(); len(rels) != 1 || !rels[0].Backup {
		t.Fatalf("backup relay %+v", rels)
	}
	if _, err := d.SetPath("anna", PathParams{Up: &off, Down: &off}); err != nil {
		t.Fatal(err)
	}
	if rels := d.directRelays(); len(rels) != 0 {
		t.Fatalf("relay with data off offered: %+v", rels)
	}

	// Server back, no direct flows: no drain, proxy role off at once.
	d.applyMode(time.Now(), ModeDirect, ModeBonded)
	eventually(t, "relay proxy role off", func() bool { return r.Stats().ProxyPort == 0 })
	if st := d.Status(); st.Direct.Proxy || st.Direct.Draining || d.route.Load() != routeTunnel {
		t.Fatalf("after DIRECT → BONDED: %+v route %d", st.Direct, d.route.Load())
	}
	if m := d.modeMsg(); m.Proxy || m.Mode != control.ModeBonded {
		t.Fatalf("mode message %+v", m)
	}
}

func TestUpdatePeak(t *testing.T) {
	t0 := time.Unix(1000, 0)
	rc := &relayPath{}
	peak := func() float64 { return math.Float64frombits(rc.peak.Load()) }
	steps := []struct {
		at       time.Duration
		up, down uint64
		want     float64
	}{
		{0, 0, 0, 0},                         // first sample: baseline only
		{time.Second, 500_000, 500_000, 1e6}, // 1 MB/s
		{2 * time.Second, 600_000, 600_000, 1e6 * math.Exp2(-1/PeakHalfLife.Seconds())},
		{2*time.Second + PeakHalfLife, 600_000, 600_000, 1e6 * math.Exp2(-1/PeakHalfLife.Seconds()) / 2},
		{3*time.Second + PeakHalfLife, 0, 0, 1e6 * math.Exp2(-1/PeakHalfLife.Seconds()) / 2}, // counters restarted
	}
	for i, s := range steps {
		rc.updatePeak(t0.Add(s.at), &control.Stats{BytesUp: s.up, BytesDown: s.down})
		if math.Abs(peak()-s.want) > 1 {
			t.Fatalf("step %d: peak %.0f, want %.0f", i, peak(), s.want)
		}
	}
}
