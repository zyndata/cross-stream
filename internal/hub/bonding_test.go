package hub

import (
	"slices"
	"testing"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/hub/uplink"
	"github.com/zyndata/cross-stream/internal/pairing"
	"github.com/zyndata/cross-stream/internal/wire"
)

func TestPathConfig(t *testing.T) {
	tests := []struct {
		name string
		path pairing.PathSettings
		max  string
		want wire.PathConfig
	}{
		{"defaults", pairing.PathSettings{}, "", wire.PathConfig{PathID: 3}},
		{"owner backup", pairing.PathSettings{Priority: "backup"}, "", wire.PathConfig{PathID: 3, Priority: wire.PriorityBackup}},
		{"relay caps to secondary", pairing.PathSettings{}, control.PrioritySecondary,
			wire.PathConfig{PathID: 3, Priority: wire.PrioritySecondary}},
		{"relay cannot raise", pairing.PathSettings{Priority: "backup"}, control.PrioritySecondary,
			wire.PathConfig{PathID: 3, Priority: wire.PriorityBackup}},
		{"limits and directions", pairing.PathSettings{RateKbps: 2000, CapMB: 50, Up: "off", Down: "backup_only"}, "",
			wire.PathConfig{PathID: 3, RateKbps: 2000, CapMB: 50, DirUp: wire.DirOff, DirDown: wire.DirBackupOnly}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rec := pairing.Relay{PathID: 3, Path: tt.path, Limits: control.Limits{MaxPriority: tt.max}}
			if got := pathConfig(rec); got != tt.want {
				t.Errorf("pathConfig = %+v, want %+v", got, tt.want)
			}
		})
	}
}

func TestHubConfigUsesUploadDirection(t *testing.T) {
	c := hubConfig(wire.PathConfig{Priority: wire.PrioritySecondary, RateKbps: 2000, CapMB: 50, DirUp: wire.DirOff})
	if c.RateBps != 250_000 || c.CapBytes != 50_000_000 || c.Dir != 2 || c.Priority != 2 {
		t.Errorf("hubConfig = %+v", c)
	}
}

func TestSharedIPWarnings(t *testing.T) {
	rs := func(name, obs string) RelayStatus {
		return RelayStatus{Relay: pairing.Relay{Name: name}, Observed: obs}
	}
	tests := []struct {
		name   string
		relays []RelayStatus
		want   []string
	}{
		{"distinct", []RelayStatus{rs("a", "1.1.1.1:5"), rs("b", "2.2.2.2:5")}, nil},
		{"shared", []RelayStatus{rs("a", "1.1.1.1:5"), rs("b", "1.1.1.1:6"), rs("c", "")},
			[]string{"relays a, b share public IP 1.1.1.1 (same uplink, no gain)"}},
		{"server behind a proxy", []RelayStatus{rs("a", "172.22.0.1:5"), rs("b", "172.22.0.1:6")}, nil},
	}
	for _, tt := range tests {
		if got := sharedIPWarnings(tt.relays, nil); !slices.Equal(got, tt.want) {
			t.Errorf("%s: %q, want %q", tt.name, got, tt.want)
		}
	}
	us := []UplinkStatus{{Link: uplink.Link{Name: "USB usb0", Kind: uplink.KindUSB}, Observed: "1.1.1.1:9"}}
	got := sharedIPWarnings([]RelayStatus{rs("a", "1.1.1.1:5")}, us)
	if len(got) != 1 || got[0] != "paths a, USB usb0 share public IP 1.1.1.1 (same uplink, no gain)" || us[0].SameIPAs != "a" {
		t.Errorf("uplink shared: %q %+v", got, us[0])
	}
}

func TestPathParamsApply(t *testing.T) {
	prio, rate := "secondary", uint32(500)
	s := pairing.PathSettings{CapMB: 10, Up: "off"}
	PathParams{Priority: &prio, RateKbps: &rate}.apply(&s)
	want := pairing.PathSettings{Priority: "secondary", RateKbps: 500, CapMB: 10, Up: "off"}
	if s != want {
		t.Errorf("apply = %+v, want %+v", s, want)
	}
}

func TestOrderedEndpoints(t *testing.T) {
	got := orderedEndpoints([]string{"wss://a/xs", "tcp://a:2", "udp://a:1", "::bad", "udp://b:443"})
	want := []string{"udp://a:1", "udp://b:443", "tcp://a:2", "wss://a:443/xs"}
	if !slices.Equal(got, want) {
		t.Errorf("orderedEndpoints = %v, want %v", got, want)
	}
	if firstEndpoint(got) != "udp://a:1" || firstEndpoint(nil) != "" {
		t.Error("firstEndpoint")
	}
	if transportOf("wss://a/xs") != "wss" || transportOf("nope") != "" {
		t.Error("transportOf")
	}
}
