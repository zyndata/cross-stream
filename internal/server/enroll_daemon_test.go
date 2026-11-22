package server_test

import (
	"context"
	"net/netip"
	"testing"

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
