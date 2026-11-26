package hub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/netip"
	"testing"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/hub/netcfg"
)

func newTestDaemon(t *testing.T, dir string, cfg DaemonConfig) *Daemon {
	t.Helper()
	cfg.Dir, cfg.CtlPort, cfg.DataPort = dir, -1, -1
	cfg.LanPrefix = netip.MustParsePrefix("127.0.0.1/8")
	cfg.Log = slog.New(slog.DiscardHandler)
	if cfg.CaptiveURL == "" && cfg.ProbeCaptive == nil {
		cfg.CaptiveURL = "off" // no real HTTP probes from unit tests
	}
	d, err := NewDaemon(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = d.ctlLn.Close(); _ = d.data.Close() })
	return d
}

func TestHubSettings(t *testing.T) {
	dir := shortTemp(t)
	d := newTestDaemon(t, dir, DaemonConfig{OverflowMbps: 7})
	if got := d.Status().OverflowMbps; got != 7 {
		t.Fatalf("flag overflow %d", got)
	}
	res, err := d.handleRPC(context.Background(), MethodSettingsSet, json.RawMessage(`{"overflow_mbps":20}`))
	if err != nil || res.(Settings).OverflowMbps != 20 {
		t.Fatalf("set %v %v", res, err)
	}
	if _, err := d.handleRPC(context.Background(), MethodSettingsSet, json.RawMessage(`{"overflow_mbps":-1}`)); err == nil {
		t.Fatal("negative overflow accepted")
	}
	res, err = d.handleRPC(context.Background(), MethodSettingsGet, nil)
	if err != nil || res.(Settings).OverflowMbps != 20 {
		t.Fatalf("get %v %v", res, err)
	}
	// Saved settings win over the flag after a restart.
	_ = d.ctlLn.Close()
	_ = d.data.Close()
	d2 := newTestDaemon(t, dir, DaemonConfig{OverflowMbps: 7})
	if got := d2.Settings().OverflowMbps; got != 20 {
		t.Fatalf("after restart %d", got)
	}
}

func TestClientSetOverflow(t *testing.T) {
	c := &Client{OverflowMbps: 5}
	if c.overflowMbps() != 5 {
		t.Fatal("initial")
	}
	c.SetOverflow(0) // no session: stored only
	if c.overflowMbps() != 0 {
		t.Fatalf("after set %d", c.overflowMbps())
	}
}

func TestHotspotPower(t *testing.T) {
	ctx := context.Background()
	hs := &fakeHotspot{}
	d := newTestDaemon(t, shortTemp(t), DaemonConfig{LanIface: "wlan9", Hotspot: hs})
	tests := []struct {
		name      string
		on        bool
		ups, down int
	}{
		{"off", false, 1, 1},
		{"off again is a no-op", false, 1, 1},
		{"on", true, 2, 1},
		{"on again is a no-op", true, 2, 1},
	}
	for _, tt := range tests {
		res, err := d.handleRPC(ctx, MethodHotspotPower, json.RawMessage(`{"on":`+map[bool]string{true: "true", false: "false"}[tt.on]+`}`))
		if err != nil {
			t.Fatalf("%s: %v", tt.name, err)
		}
		info := res.(HotspotInfo)
		_, ups, downs := hs.last()
		if info.On != tt.on || ups != tt.ups || downs != tt.down {
			t.Fatalf("%s: on=%v ups=%d downs=%d", tt.name, info.On, ups, downs)
		}
	}
	// Settings changed while off apply on the next start, not at once.
	if _, err := d.HotspotPower(ctx, false); err != nil {
		t.Fatal(err)
	}
	ssid := "Later"
	info, err := d.HotspotSet(ctx, HotspotParams{SSID: &ssid})
	if _, ups, _ := hs.last(); err != nil || ups != 2 || info.On || info.Settings.SSID != "Later" {
		t.Fatalf("set while off: ups=%d %+v %v", ups, info, err)
	}
	if p, _, _ := d.issuer.Current(); p.SSID != "Later" {
		t.Fatalf("pairing code SSID %q", p.SSID)
	}
	if _, err := d.HotspotPower(ctx, true); err != nil {
		t.Fatal(err)
	}
	if last, _, _ := hs.last(); last.SSID != "Later" {
		t.Fatalf("started with %q", last.SSID)
	}
	headless := newTestDaemon(t, shortTemp(t), DaemonConfig{})
	if _, err := headless.HotspotPower(ctx, false); err == nil {
		t.Fatal("headless hotspot.power")
	}
}

func TestRelayGoneInStatus(t *testing.T) {
	d := newTestDaemon(t, shortTemp(t), DaemonConfig{})
	rec, err := d.reg.Add([crypto.KeyLen]byte{1}, control.Hello{RelayName: "Pixel", Carrier: "Orange"})
	if err != nil {
		t.Fatal(err)
	}
	if rs := d.Status().Relays[0]; rs.LastReason != "" || !rs.LastSeen.IsZero() {
		t.Fatalf("never connected: %+v", rs)
	}
	d.relayGone(rec.ID, "leaving: limit")
	if rs := d.Status().Relays[0]; rs.LastReason != "leaving: limit" || rs.LastSeen.IsZero() {
		t.Fatalf("after leave: %+v", rs)
	}
}

// TestOwnTrafficSetting: the switch round-trips through settings.set/get,
// the saved file and status; Windows has no "off" (phase 21 W1).
func TestOwnTrafficSetting(t *testing.T) {
	dir := shortTemp(t)
	d := newTestDaemon(t, dir, DaemonConfig{})
	ctx := context.Background()
	if got := d.Status().OwnTraffic; got != netcfg.OwnTrafficAlways {
		t.Fatalf("default own_traffic %v", got)
	}
	tests := []struct {
		params string
		want   bool
		err    bool
	}{
		{`{"own_traffic":true}`, true, false},
		{`{"own_traffic":false}`, false, netcfg.OwnTrafficAlways},
		{`{"mode":"min_latency"}`, netcfg.OwnTrafficAlways, false}, // other settings keep it
	}
	for _, tc := range tests {
		res, err := d.handleRPC(ctx, MethodSettingsSet, json.RawMessage(tc.params))
		if tc.err {
			if !errors.Is(err, ErrOwnTrafficFixed) {
				t.Fatalf("%s: err %v, want ErrOwnTrafficFixed", tc.params, err)
			}
			continue
		}
		if err != nil || res.(Settings).OwnTraffic != tc.want {
			t.Fatalf("%s: %v %v", tc.params, res, err)
		}
		if got := d.Status().OwnTraffic; got != tc.want {
			t.Fatalf("%s: status own_traffic %v", tc.params, got)
		}
	}
	_, _ = d.handleRPC(ctx, MethodSettingsSet, json.RawMessage(`{"own_traffic":true}`))
	_ = d.ctlLn.Close()
	_ = d.data.Close()
	if got := newTestDaemon(t, dir, DaemonConfig{}).Settings().OwnTraffic; !got {
		t.Fatal("own_traffic not saved")
	}
}
