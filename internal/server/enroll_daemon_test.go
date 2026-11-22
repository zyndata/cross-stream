package server_test

import (
	"context"
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/enroll"
	"github.com/zyndata/cross-stream/internal/hub"
	"github.com/zyndata/cross-stream/internal/tun"
)

// TestDaemonEnrollsThroughOwnConnection: a hub daemon with no phone and no
// local link enrolls through the computer's own connection (phase 19).
func TestDaemonEnrollsThroughOwnConnection(t *testing.T) {
	e := startServer(t)
	res, err := e.ctl.Enroll(e.ctx, "solo")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	d, err := hub.NewDaemon(hub.DaemonConfig{Dir: dir, CtlPort: -1, DataPort: -1, Version: "test",
		LanPrefix: netip.MustParsePrefix("127.0.0.1/8"), Log: quiet(),
		NewTUN: func(c tun.Config) (tun.Device, error) {
			dev, _ := tun.Pipe(c.Name, "app", c.MTU)
			return dev, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	defer func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("daemon run: %v", err)
		}
	}()
	en, err := d.Enroll(ctx, res.Code)
	if err != nil {
		t.Fatalf("enroll without phone or local link: %v", err)
	}
	if saved, err := hub.LoadEnrollment(dir); err != nil || saved.Name != "solo" || saved.TunnelIP != en.TunnelIP {
		t.Fatalf("saved enrollment %+v, %v (returned %+v)", saved, err, en)
	}
	if !d.Status().Enrolled {
		t.Fatal("status not enrolled")
	}
}

// TestPendingEnrollmentCompletesThroughRelay: a hub that switched to a new
// server whose answer got lost (server.json pending: the new key and
// endpoints, the code's token, the old tunnel address) finishes the
// enrollment through a relay: the server registers it, server.json gets the
// server's address and loses the token, and the daemon restarts for the new
// TUN address.
func TestPendingEnrollmentCompletesThroughRelay(t *testing.T) {
	e := startServer(t)
	res, err := e.ctl.Enroll(e.ctx, "moved")
	if err != nil {
		t.Fatal(err)
	}
	c, err := enroll.ParseCode(res.Code)
	if err != nil {
		t.Fatal(err)
	}
	const oldIP = "100.64.9.9"
	dir := t.TempDir()
	if err := hub.SaveEnrollment(dir, hub.Enrollment{Name: c.Name, ServerKey: crypto.EncodeKey(c.ServerKey),
		Endpoints: c.Endpoints, TunnelIP: oldIP, MTU: 1360, Token: c.Token.String()}); err != nil {
		t.Fatal(err)
	}
	d, err := hub.NewDaemon(hub.DaemonConfig{Dir: dir, CtlPort: -1, DataPort: -1, Version: "test", Log: quiet(),
		LanPrefix: netip.MustParsePrefix("127.0.0.1/8"),
		NewTUN: func(c tun.Config) (tun.Device, error) {
			dev, _ := tun.Pipe(c.Name, "app", c.MTU)
			return dev, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if st := d.Status(); !st.Enrolled || !st.EnrollPending || st.TunnelIP != "" {
		t.Fatalf("pending status %+v", st)
	}
	ctx, cancel := context.WithCancel(e.ctx)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	code, err := hubPairQR(d)
	if err != nil {
		t.Fatal(err)
	}
	rkey, _ := crypto.GenerateKeypair()
	startRelay(ctx, t, rkey, code, false)

	select {
	case err := <-done:
		if !errors.Is(err, hub.ErrRestart) {
			t.Fatalf("daemon run: %v, want a restart for the new tunnel address", err)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("pending enrollment not confirmed")
	}
	saved, err := hub.LoadEnrollment(dir)
	if err != nil || saved.Token != "" || saved.TunnelIP == oldIP || saved.Name != "moved" {
		t.Fatalf("saved enrollment %+v, %v", saved, err)
	}
	hubs, err := e.ctl.Hubs(e.ctx)
	if err != nil || len(hubs) != 1 || hubs[0].Name != "moved" || hubs[0].TunnelIP != saved.TunnelIP {
		t.Fatalf("server hubs %+v, %v (hub saved %s)", hubs, err, saved.TunnelIP)
	}
}
