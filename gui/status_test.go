package main

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/bond"
	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/hub"
	"github.com/zyndata/cross-stream/internal/hub/uplink"
	"github.com/zyndata/cross-stream/internal/pairing"
)

// TestStatusMatchesHub decodes a real hub.Status into the GUI structs, so a
// renamed JSON field in internal/hub fails here.
func TestStatusMatchesHub(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	src := hub.Status{Hub: "dell", Mode: hub.ModeBonded, ModeSince: now, Clients: 3, Enrolled: true, EnrollPending: true,
		TunnelIP: "10.99.0.2", TunnelUp: true, ServerEP: "1.2.3.4:51820", Bond: &hub.BondStatus{},
		Direct: &hub.DirectStatus{Draining: true, TCP: 4, UDP: 2}, PEP: &hub.PEPStatus{On: true, Available: true, Streams: 6, Flows: 5},
		Warnings: []string{"w"}, OverflowMbps: 12,
		BondMode: "min_latency", Redundancy: "on", Redundant: true, Testing: true,
		Relays: []hub.RelayStatus{{
			Relay: pairing.Relay{ID: "r1", Name: "S23", Carrier: "Orange", Limits: control.Limits{CapMB: 500, BatteryMin: 20},
				Path: pairing.PathSettings{Priority: "backup", RateKbps: 800, CapMB: 300, Up: "off", Down: "backup_only"}},
			Connected: true, Since: now, DirectFlows: 5, LastReason: "leaving: battery", LastSeen: now, Transport: "tcp",
			Stats: &control.Stats{BytesUp: 1, BytesDown: 2, Battery: 77, SignalDBm: -95, NetType: "nr", CellularOK: true, WifiRSSI: -60},
			Bond:  &bond.PathStats{State: "ALIVE", SRTT: time.Second, Jitter: 2, Loss: 0.5, TxRate: 3, RxRate: 4, Capped: true, Used: 9},
		}},
	}
	b, err := json.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	var got hubStatus
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.Hub != "dell" || got.Mode != "BONDED" || !got.ModeSince.Equal(now) || got.Clients != 3 || !got.Enrolled || !got.Pending ||
		!got.TunnelUp || got.ServerEP == "" || got.Bond == nil || got.Direct == nil || !got.Direct.Draining ||
		got.Direct.TCP != 4 || got.OverflowMbps != 12 || len(got.Warnings) != 1 || got.BondMode != "min_latency" ||
		got.Redundancy != "on" || !got.Redundant || !got.Testing {
		t.Fatalf("status %+v", got)
	}
	if got.PEP == nil || *got.PEP != (pepStatus{true, true, 6, 5}) {
		t.Fatalf("pep %+v", got.PEP)
	}
	sb, err := json.Marshal(hub.Settings{PEP: "on", PEPExclude: []string{"22"}})
	if err != nil {
		t.Fatal(err)
	}
	var ps pepSettings
	if err := json.Unmarshal(sb, &ps); err != nil || ps.PEP != "on" || len(ps.Exclude) != 1 {
		t.Fatalf("pep settings %+v %v", ps, err)
	}
	r := got.Relays[0]
	if r.ID != "r1" || r.Name != "S23" || r.Carrier != "Orange" || r.Limits.CapMB != 500 || r.Limits.BatteryMin != 20 ||
		r.Path != (pathSettings{"backup", 800, 300, "off", "backup_only"}) || !r.Connected || r.DirectFlows != 5 ||
		r.LastReason != "leaving: battery" || !r.LastSeen.Equal(now) || !r.Since.Equal(now) || r.Transport != "tcp" {
		t.Fatalf("relay %+v", r)
	}
	if *r.Stats != (relayStats{1, 2, 77, -95, "nr", true, -60}) {
		t.Fatalf("stats %+v", *r.Stats)
	}
	if *r.Bond != (pathStats{"ALIVE", time.Second, 2, 0.5, 3, 4, true, 9}) {
		t.Fatalf("bond %+v", *r.Bond)
	}
}

func TestSpeedResultMatchesHub(t *testing.T) {
	now := time.Now().UTC().Truncate(time.Second)
	src := hub.SpeedResult{At: now, Relay: "S23", Secs: 5, MaxMB: 10, DownMbps: 7.5, UpMbps: 3.25, SentMB: 9.5,
		Paths: []hub.SpeedPath{{Relay: "S23", PathID: 1, Dir: "down", Mbps: 7.5, RTTms: 80, Loss: 0.01}}}
	b, _ := json.Marshal(src)
	var got SpeedResult
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !got.At.Equal(now) || got.Relay != "S23" || got.Secs != 5 || got.MaxMB != 10 || got.DownMbps != 7.5 ||
		got.UpMbps != 3.25 || got.SentMB != 9.5 || len(got.Paths) != 1 ||
		got.Paths[0] != (SpeedPath{Relay: "S23", Dir: "down", Mbps: 7.5, RTTms: 80, Loss: 0.01}) {
		t.Fatalf("speed result %+v", got)
	}
	p, _ := json.Marshal(SpeedTestParams{Relay: "S23", Dir: "both", Secs: 5, MaxMB: 10})
	var hp hub.SpeedTestParams
	if err := json.Unmarshal(p, &hp); err != nil || hp != (hub.SpeedTestParams{Relay: "S23", Dir: "both", Secs: 5, MaxMB: 10}) {
		t.Fatalf("params %+v %v", hp, err)
	}
}

func TestHotspotMatchesHub(t *testing.T) {
	src := hub.HotspotInfo{Enabled: true, On: true, Clients: 2, Error: "e",
		Settings: hub.HotspotSettings{SSID: "s", PSK: "p", Band: "a", Channel: 36, Country: "PL", ClientWifi: "share"}}
	src.State.Band, src.State.Channel, src.State.Iface = "a", 149, "wlan0"
	b, _ := json.Marshal(src)
	var got HotspotInfo
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if !got.Enabled || !got.On || got.Clients != 2 || got.Error != "e" || got.State.Channel != 149 ||
		got.Settings != (HotspotSettings{"s", "p", "a", 36, "PL", "share"}) {
		t.Fatalf("hotspot %+v", got)
	}
	// Saving the form sends client_wifi back as hotspot.set params.
	b, _ = json.Marshal(got.Settings)
	var p hub.HotspotParams
	if err := json.Unmarshal(b, &p); err != nil || p.ClientWifi == nil || *p.ClientWifi != "share" {
		t.Fatalf("params %s %v", b, err)
	}
}

// TestClientWifiMatchesHub decodes hub.ClientWifiQuestion into the GUI struct.
func TestClientWifiMatchesHub(t *testing.T) {
	src := hub.ClientWifiQuestion{Iface: "wlo1", Conn: "Office", CanShare: true, Tried: "share", Error: "no IR"}
	b, _ := json.Marshal(src)
	var got clientWifiQ
	if err := json.Unmarshal(b, &got); err != nil || got != (clientWifiQ{"wlo1", "Office", true, "share", "no IR"}) {
		t.Fatalf("%+v %v", got, err)
	}
	if eventClientWifi != hub.EventClientWifi {
		t.Fatal("event name differs from xs-hubd")
	}
}

// TestUplinkMatchesHub decodes a real hub.UplinkStatus into the GUI struct
// and builds its row.
func TestUplinkMatchesHub(t *testing.T) {
	src := hub.Status{Uplinks: []hub.UplinkStatus{{
		Link: uplink.Link{ID: "u1", Name: "Modem", Kind: uplink.KindModem, Iface: "wwan0", Enabled: true, TTL: true,
			Path:   pairing.PathSettings{Priority: "secondary", RateKbps: 900, Up: "off"},
			Limits: control.Limits{CapMB: 50, CapPeriod: "day", ResetDay: 3, OverCap: "throttle", ThrottleKbps: 256}},
		State: hub.UplinkUp, Present: true, Addr: "10.64.0.2/30", Transport: "udp", UsedNow: 2_500_000,
		SameIPAs: "S23", DirectFlows: 2,
		Bond:  &bond.PathStats{State: "ALIVE", SRTT: 40 * time.Millisecond, TxRate: bytesPerMbit, RxRate: 2 * bytesPerMbit},
		Modem: &uplink.Modem{Tech: "lte", Operator: "Orange", Quality: 40, RSRP: -115},
	}}}
	b, _ := json.Marshal(src)
	var got hubStatus
	if err := json.Unmarshal(b, &got); err != nil || len(got.Uplinks) != 1 {
		t.Fatalf("%v %+v", err, got)
	}
	tr := newTracker()
	var uv UplinkView
	for range hintOnAfter {
		uv = tr.uplink(got.Uplinks[0])
	}
	want := UplinkLimits{CapMB: 50, CapPeriod: "day", ResetDay: 3, OverCap: "throttle", ThrottleKbps: 256}
	if uv.ID != "u1" || uv.Kind != "modem" || uv.Iface != "wwan0" || !uv.Enabled || !uv.TTL || uv.State != "up" ||
		uv.Priority != "secondary" || uv.RateKbps != 900 || uv.Up != "off" || uv.Down != "on" || uv.Limits != want ||
		uv.UsedMB != 2.5 || uv.UpMbps != 1 || uv.DownMbps != 2 || uv.RTTms != 40 || uv.Tech != "LTE" ||
		uv.Operator != "Orange" || uv.SignalDBm != -115 || uv.Quality != 40 || !uv.WeakCell || uv.SameIPAs != "S23" ||
		uv.DirectFlows != 2 || uv.Transport != "udp" || uv.PathState != "ALIVE" {
		t.Fatalf("row %+v", uv)
	}
	p, _ := json.Marshal(UplinkParams{Uplink: "u1", Limits: &want, ResetUsage: true})
	var hp uplink.Params
	if err := json.Unmarshal(p, &hp); err != nil || hp.Uplink != "u1" || !hp.ResetUsage || hp.Limits.ThrottleKbps != 256 ||
		hp.Limits.CapPeriod != "day" {
		t.Fatalf("params %+v %v", hp, err)
	}
}

// TestEventMatchesHub decodes a hub speed test event into the GUI struct.
func TestEventMatchesHub(t *testing.T) {
	b, _ := json.Marshal(hub.Event{Type: hub.EvSpeedTestEnd, Name: "S23", Value: 80, Up: 43})
	var e hubEvent
	if err := json.Unmarshal(b, &e); err != nil || e.Type != "speedtest_done" || e.Value != 80 || e.Up != 43 || e.Name != "S23" {
		t.Fatalf("%+v %v", e, err)
	}
}
