package main

import (
	"strings"
	"testing"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/hub"
	"github.com/zyndata/cross-stream/internal/hub/uplink"
)

func TestParseUplinkSet(t *testing.T) {
	lim := &control.Limits{CapMB: 10, CapPeriod: control.PeriodMonth, ResetDay: 5}
	p, err := parseUplinkSet("u1", []string{"enabled=true", "ttl=false", "priority=backup", "rate_kbps=800",
		"cap_mb=50", "period=day", "over_cap=throttle", "throttle_kbps=256", "reset=1", "name=Modem Dell"}, lim)
	if err != nil {
		t.Fatal(err)
	}
	if !*p.Enabled || *p.TTL || *p.Priority != "backup" || *p.RateKbps != 800 || !p.ResetUsage || *p.Name != "Modem Dell" ||
		p.Limits == nil || p.Limits.CapMB != 50 || p.Limits.CapPeriod != "day" || p.Limits.ResetDay != 5 ||
		p.Limits.OverCap != "throttle" || p.Limits.ThrottleKbps != 256 {
		t.Fatalf("params %+v limits %+v", p, p.Limits)
	}
	if p, _ := parseUplinkSet("u1", []string{"enabled=0"}, &control.Limits{}); p.Limits != nil {
		t.Fatal("limits sent without a limit key")
	}
	for _, bad := range []string{"cap_mb=-1", "enabled=maybe", "nope=1", "noequals"} {
		if _, err := parseUplinkSet("u1", []string{bad}, &control.Limits{}); err == nil {
			t.Errorf("%q accepted", bad)
		}
	}
}

func TestParseCaptive(t *testing.T) {
	tests := []struct {
		args    []string
		want    hub.CaptiveParams
		wantErr bool
	}{
		{[]string{"u1"}, hub.CaptiveParams{Uplink: "u1"}, false},
		{[]string{"u1", "-open"}, hub.CaptiveParams{Uplink: "u1", Open: true}, false},
		{[]string{"-open", "Modem"}, hub.CaptiveParams{Uplink: "Modem", Open: true}, false},
		{[]string{"-open"}, hub.CaptiveParams{}, true},
		{[]string{"u1", "u2"}, hub.CaptiveParams{}, true},
		{[]string{"u1", "-x"}, hub.CaptiveParams{}, true},
	}
	for _, tc := range tests {
		got, err := parseCaptive(tc.args)
		if (err != nil) != tc.wantErr || (!tc.wantErr && got != tc.want) {
			t.Fatalf("%q: %+v %v", tc.args, got, err)
		}
	}
	txt := captiveText(hub.CaptiveResult{Uplink: "u1", Name: "Hotel", Probe: uplink.Probe{State: uplink.NetCaptive, URL: "http://p/login"},
		Route: []string{"192.0.2.80"}})
	if !strings.Contains(txt, "login page http://p/login") || !strings.Contains(txt, "192.0.2.80 routed") {
		t.Fatalf("text %q", txt)
	}
	if err := openURL("file:///etc/passwd"); err == nil {
		t.Fatal("file URL opened")
	}
}
