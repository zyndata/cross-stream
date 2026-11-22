package hub

import (
	"context"
	"net/netip"
	"syscall"
	"testing"

	"github.com/zyndata/cross-stream/internal/hub/uplink"
)

func TestUplinkManager(t *testing.T) {
	present := []uplink.Iface{
		{Name: "usb0", MAC: "02:00:00:00:00:01", Kind: uplink.KindUSB, Addr: netip.MustParsePrefix("192.168.42.7/24")},
		{Name: "wwan0", Kind: uplink.KindModem, Addr: netip.MustParsePrefix("10.64.0.2/30")},
	}
	mm := 0
	d := newTestDaemon(t, shortTemp(t), DaemonConfig{
		ScanUplinks: func() ([]uplink.Iface, error) { return present, nil },
		UplinkControl: func(string) func(string, string, syscall.RawConn) error {
			return func(string, string, syscall.RawConn) error { return nil }
		},
		ModemInfo: func(context.Context) map[string]uplink.Modem {
			mm++
			return map[string]uplink.Modem{"wwan0": {Tech: "lte", RSRP: -98}}
		},
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	d.ctx = ctx
	d.uplinkScan(ctx)
	l := d.UplinksList()
	if len(l) != 2 || l[0].State != UplinkOff || l[1].State != UplinkOff || l[1].Modem == nil || l[1].Modem.RSRP != -98 ||
		l[1].Key != "if:wwan0" || mm != 1 {
		t.Fatalf("found %+v (modem queries %d)", l, mm)
	}
	on := true
	if _, err := d.UplinksUpdate(uplink.Params{Uplink: "u1", Enabled: &on}); err != nil {
		t.Fatal(err)
	}
	d.mu.Lock()
	running := d.locals["u1"] != nil && d.locals["u2"] == nil
	d.mu.Unlock()
	if !running || len(d.up.ttl) != 1 || d.up.ttl[0] != "usb0" {
		t.Fatalf("enabled usb0: running %v ttl %v", running, d.up.ttl)
	}
	if st := d.UplinksList()[0].State; st != UplinkConnecting { // not enrolled: no endpoint yet
		t.Fatalf("state %s", st)
	}
	// Unplugged: stopped, absent.
	present = present[1:]
	d.uplinkScan(ctx)
	d.mu.Lock()
	n := len(d.locals)
	d.mu.Unlock()
	if l := d.UplinksList(); n != 0 || l[0].State != UplinkAbsent || len(d.up.ttl) != 0 {
		t.Fatalf("after unplug: %d running, %+v", n, l[0])
	}
	if err := d.UplinksForget("u1"); err != nil || len(d.UplinksList()) != 1 {
		t.Fatalf("forget: %v", err)
	}
	if _, err := d.handleRPC(ctx, MethodUplinksUpdate, []byte(`{"uplink":"nope"}`)); err == nil {
		t.Fatal("unknown link accepted")
	}
}
