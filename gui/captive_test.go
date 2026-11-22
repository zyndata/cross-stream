package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/hub"
	"github.com/zyndata/cross-stream/internal/hub/uplink"
	"github.com/zyndata/cross-stream/internal/ipc"
)

// TestCaptiveMatchesHub decodes a real hub uplink with a probe result.
func TestCaptiveMatchesHub(t *testing.T) {
	src := hub.UplinkStatus{Link: uplink.Link{ID: "u1"}, State: hub.UplinkCaptive,
		Captive: &hub.CaptiveStatus{Probe: uplink.Probe{State: uplink.NetCaptive, URL: "http://p/login"},
			Checked: time.Now(), Rechecking: true}}
	b, err := json.Marshal(src)
	if err != nil {
		t.Fatal(err)
	}
	var got uplinkStatus
	if err := json.Unmarshal(b, &got); err != nil {
		t.Fatal(err)
	}
	if got.State != "captive" || got.Captive == nil || *got.Captive != (captiveProbe{"captive", "http://p/login", true}) {
		t.Fatalf("uplink %+v captive %+v", got, got.Captive)
	}
	got.Captive.State = "no_internet"
	if uv := newTracker().uplink(got); !uv.NoInternet || uv.State != "captive" {
		t.Fatalf("view %+v", uv)
	}
}

// TestCaptiveLogin runs the login button against a fake xs-hubd.
func TestCaptiveLogin(t *testing.T) {
	dir, err := os.MkdirTemp("", "xsgui")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	sock := filepath.Join(dir, "s")
	ln, err := ipc.Listen(sock, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	results := map[string]hub.CaptiveResult{
		"open":   {Probe: uplink.Probe{State: uplink.NetOpen}},
		"none":   {Probe: uplink.Probe{State: uplink.NetNoAccess, Err: "timeout"}},
		"portal": {Probe: uplink.Probe{State: uplink.NetCaptive, URL: "http://192.0.2.80/login"}, Route: []string{"192.0.2.80"}},
		"noroute": {Probe: uplink.Probe{State: uplink.NetCaptive, URL: "https://p/"}, RouteErr: "x",
			Hint: hub.CaptiveHint},
	}
	go ipc.Serve(ctx, ln, func(_ context.Context, method string, params json.RawMessage) (any, error) {
		var p hub.CaptiveParams
		if method != hub.MethodUplinksCaptive || json.Unmarshal(params, &p) != nil || !p.Open {
			return nil, ipc.ErrMethodNotFound
		}
		return results[p.Uplink], nil
	}, slog.New(slog.DiscardHandler))

	var opened []string
	a := &App{sock: sock, log: slog.New(slog.DiscardHandler), openURL: func(u string) { opened = append(opened, u) }}
	tests := []struct {
		id, msg string
		opened  int
	}{
		{"open", "Internet działa — logowanie nie jest potrzebne", 0},
		{"none", "Brak internetu na tym łączu", 0},
		{"portal", "", 1},
		{"noroute", hub.CaptiveHint, 1},
	}
	for _, tc := range tests {
		msg, err := a.CaptiveLogin(tc.id)
		if err != nil || msg != tc.msg || len(opened) != tc.opened {
			t.Fatalf("%s: %q %v opened %q", tc.id, msg, err, opened)
		}
	}
	if opened[0] != "http://192.0.2.80/login" || methodUplinksCaptive != hub.MethodUplinksCaptive {
		t.Fatalf("opened %q", opened)
	}
}
