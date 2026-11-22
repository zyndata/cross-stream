package server_test

import (
	"context"
	"encoding/binary"
	"net/netip"
	"syscall"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/hub"
	"github.com/zyndata/cross-stream/internal/hub/uplink"
	"github.com/zyndata/cross-stream/internal/tun"
)

// TestLocalUplinkPath: a hub with no relay enrolls and carries traffic over
// a local uplink (the test "interface" is loopback: binding is a no-op); a
// daily cap stops the link and the status says why.
func TestLocalUplinkPath(t *testing.T) {
	e := startServer(t)
	res, err := e.ctl.Enroll(e.ctx, "hub-local")
	if err != nil {
		t.Fatal(err)
	}
	apps := make(chan tun.Device, 1)
	iface := uplink.Iface{Name: "usb9", MAC: "02:00:00:00:00:09", Kind: uplink.KindUSB,
		Addr: netip.MustParsePrefix("127.0.0.1/8")}
	d, err := hub.NewDaemon(hub.DaemonConfig{Dir: t.TempDir(), CtlPort: -1, DataPort: -1, Version: "test", Log: quiet(),
		LanPrefix: netip.MustParsePrefix("127.0.0.1/8"),
		NewTUN: func(c tun.Config) (tun.Device, error) {
			dev, app := tun.Pipe(c.Name, "app", c.MTU)
			apps <- app
			return dev, nil
		},
		ScanUplinks: func() ([]uplink.Iface, error) { return []uplink.Iface{iface}, nil },
		UplinkControl: func(string) func(string, string, syscall.RawConn) error {
			return func(string, string, syscall.RawConn) error { return nil }
		},
	})
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

	// Found, but off until the owner enables it.
	eventually(t, "uplink found", func() bool {
		l := d.UplinksList()
		return len(l) == 1 && l[0].State == hub.UplinkOff && l[0].Present && l[0].TTL
	})
	on := true
	if _, err := d.UplinksUpdate(uplink.Params{Uplink: "u1", Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	en, err := d.Enroll(ctx, res.Code)
	if err != nil {
		t.Fatalf("enroll via local uplink: %v", err)
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
		t.Fatal("uplink through the local path failed")
	}
	if _, err := e.origin.Write(ipv4(origin, hubIP, 2)); err != nil {
		t.Fatal(err)
	}
	if readTag(t, appIn, 2, 2*time.Second) == nil {
		t.Fatal("downlink through the local path failed")
	}
	st := d.Status()
	eventually(t, "mode BONDED", func() bool { return d.Status().Mode == hub.ModeBonded })
	if !st.TunnelUp || len(st.Uplinks) != 1 || st.Uplinks[0].State != hub.UplinkUp ||
		st.Uplinks[0].Transport != "udp" || st.Uplinks[0].Bond == nil || st.Uplinks[0].UsedNow == 0 {
		t.Fatalf("status %+v", st)
	}

	// A 1 MB daily cap, already used: the link stops and says why.
	if _, err := d.UplinksUpdate(uplink.Params{Uplink: "u1",
		Limits: &control.Limits{CapMB: 1, CapPeriod: control.PeriodDay}}); err != nil {
		t.Fatal(err)
	}
	big := append(ipv4(hubIP, origin, 3), make([]byte, 1000)...)
	binary.BigEndian.PutUint16(big[2:], uint16(len(big)))
	eventually(t, "cap stops the link", func() bool {
		for range 50 {
			_, _ = app.Write(big)
		}
		l := d.UplinksList()
		return len(l) == 1 && l[0].State == hub.UplinkLimit
	})
	// Counter reset: running again.
	if _, err := d.UplinksUpdate(uplink.Params{Uplink: "u1", ResetUsage: true}); err != nil {
		t.Fatal(err)
	}
	eventually(t, "link back after counter reset", func() bool {
		l := d.UplinksList()
		return len(l) == 1 && l[0].State == hub.UplinkUp
	})
}
