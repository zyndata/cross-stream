package server_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/hub"
	"github.com/zyndata/cross-stream/internal/pairing"
	"github.com/zyndata/cross-stream/internal/relay"
	"github.com/zyndata/cross-stream/internal/tun"
)

// startRelay runs a relay core in-process and returns its Run result channel.
func startRelay(ctx context.Context, t *testing.T, key crypto.Keypair, code string, paired bool) chan error {
	t.Helper()
	p, err := pairing.Decode(code)
	if err != nil {
		t.Fatal(err)
	}
	r, err := relay.New(relay.Config{Key: key, Hub: p, Name: "anna", Carrier: "Play", Paired: paired,
		ListenIP: netip.MustParseAddr("127.0.0.1"), Log: quiet()})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- r.Run(ctx) }()
	return done
}

func waitRun(t *testing.T, done chan error) error {
	t.Helper()
	select {
	case err := <-done:
		return err
	case <-time.After(10 * time.Second):
		t.Fatal("relay Run did not return")
		return nil
	}
}

func eventually(t *testing.T, what string, cond func() bool) {
	t.Helper()
	for i := 0; i < 100; i++ {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

// TestRelayPathEnrollForwardForget: hub daemon enrolls through a relay core,
// carries traffic both ways through it, then forgets it; the forgotten relay
// is rejected on reconnect and may pair again only with a fresh code.
func TestRelayPathEnrollForwardForget(t *testing.T) {
	e := startServer(t)
	res, err := e.ctl.Enroll(e.ctx, "hub1")
	if err != nil {
		t.Fatal(err)
	}
	apps := make(chan tun.Device, 1)
	d, err := hub.NewDaemon(hub.DaemonConfig{Dir: t.TempDir(), CtlPort: -1, DataPort: -1, Version: "test", Log: quiet(),
		LanPrefix: netip.MustParsePrefix("127.0.0.1/8"),
		NewTUN: func(c tun.Config) (tun.Device, error) {
			dev, app := tun.Pipe(c.Name, "app", c.MTU)
			apps <- app
			return dev, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	dDone := make(chan error, 1)
	go func() { dDone <- d.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-dDone; err != nil {
			t.Errorf("daemon run: %v", err)
		}
	}()

	qr := func() string {
		code, err := hubPairQR(d)
		if err != nil {
			t.Fatal(err)
		}
		return code
	}
	firstCode := qr()
	rkey, _ := crypto.GenerateKeypair()
	rctx, rcancel := context.WithCancel(ctx)
	defer rcancel()
	r1 := startRelay(rctx, t, rkey, firstCode, false)
	eventually(t, "relay connected with data address", func() bool {
		st := d.Status()
		return len(st.Relays) == 1 && st.Relays[0].Connected && st.Relays[0].DataAddr != ""
	})
	if qr() == firstCode {
		t.Fatal("pairing code not regenerated after pairing")
	}

	en, err := d.Enroll(ctx, res.Code)
	if err != nil {
		t.Fatalf("enroll via relay: %v", err)
	}
	hubIP := netip.MustParseAddr(en.TunnelIP)
	app := <-apps
	appIn := collect(app)

	var up []byte
	for i := 0; i < 50 && up == nil; i++ {
		_, _ = app.Write(ipv4(hubIP, origin, 1))
		up = readTag(t, e.inet, 1, 100*time.Millisecond)
	}
	if up == nil {
		t.Fatal("uplink through the relay failed")
	}
	if _, err := e.origin.Write(ipv4(origin, hubIP, 2)); err != nil {
		t.Fatal(err)
	}
	if readTag(t, appIn, 2, 2*time.Second) == nil {
		t.Fatal("downlink through the relay failed")
	}
	st := d.Status()
	if !st.Enrolled || !st.TunnelUp || st.Relays[0].Name != "anna" || st.Relays[0].Carrier != "Play" ||
		st.Relays[0].Transport != "udp" {
		t.Fatalf("status %+v", st)
	}

	// Settings reach the server in SESSION_CONFIG.
	on, minLat := "on", "min_latency"
	if _, err := d.SetSettings(hub.SettingsParams{Redundancy: &on, Mode: &minLat}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "server applies SESSION_CONFIG", func() bool {
		ss, err := e.ctl.Sessions(ctx)
		return err == nil && len(ss) == 1 && ss[0].Bond.Mode == "min_latency" && ss[0].Bond.Redundancy == "on"
	})
	if st := d.Status(); st.BondMode != "min_latency" || st.Redundancy != "on" {
		t.Fatalf("hub status mode %q redundancy %q", st.BondMode, st.Redundancy)
	}

	// Speed test through the relay, both directions, small cap.
	sres, err := d.SpeedTest(ctx, hub.SpeedTestParams{Relay: "anna", Secs: 1, MaxMB: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(sres.Paths) != 2 || sres.DownMbps <= 0 || sres.UpMbps <= 0 || sres.SentMB <= 0 {
		t.Fatalf("speed test %+v", sres)
	}
	if list, err := d.SpeedTests(); err != nil || len(list) != 1 {
		t.Fatalf("stored speed tests %v %v", list, err)
	}

	// Forget: the connected relay is told to delete the hub.
	if _, err := d.Forget("anna"); err != nil {
		t.Fatal(err)
	}
	if err := waitRun(t, r1); !errors.Is(err, relay.ErrForgotten) {
		t.Fatalf("relay Run after forget: %v", err)
	}
	// Reconnecting with the old (spent) code is rejected as forgotten.
	err = waitRun(t, startRelay(rctx, t, rkey, firstCode, false))
	var rej *relay.RejectedError
	if !errors.As(err, &rej) || rej.Reason != control.RejectForgotten {
		t.Fatalf("forgotten relay reconnect: %v", err)
	}
	// So is a reconnect that skips the token.
	if err := waitRun(t, startRelay(rctx, t, rkey, firstCode, true)); !errors.As(err, &rej) || rej.Reason != control.RejectForgotten {
		t.Fatalf("forgotten relay reconnect without token: %v", err)
	}
	// A stranger with a spent token gets bad_token.
	other, _ := crypto.GenerateKeypair()
	if err := waitRun(t, startRelay(rctx, t, other, firstCode, false)); !errors.As(err, &rej) || rej.Reason != control.RejectBadToken {
		t.Fatalf("spent token: %v", err)
	}
	// A fresh code pairs the forgotten relay again.
	startRelay(rctx, t, rkey, qr(), false)
	eventually(t, "re-paired relay connected", func() bool {
		st := d.Status()
		return len(st.Relays) == 1 && st.Relays[0].Connected
	})
}

func hubPairQR(d *hub.Daemon) (string, error) {
	return d.PairCode()
}
