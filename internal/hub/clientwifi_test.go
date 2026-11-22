package hub

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"net/netip"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/hub/netcfg"
	"github.com/zyndata/cross-stream/internal/ipc"
	"github.com/zyndata/cross-stream/internal/tun"
)

func TestAskClientWifi(t *testing.T) {
	dir := shortTemp(t)
	sock := filepath.Join(dir, "s.sock")
	if _, err := LoadHotspotSettings(dir, "test"); err != nil {
		t.Fatal(err)
	}
	cw := &netcfg.ClientWifiError{Iface: "wlan9", Conn: "Office", CanShare: false}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() {
		done <- AskClientWifi(ctx, DaemonConfig{Dir: dir, Socket: sock, Log: slog.New(slog.DiscardHandler)}, cw)
	}()
	var q ClientWifiQuestion
	for {
		err := ipc.Subscribe(ctx, sock, MethodEventsSubscribe, nil, func(method string, params json.RawMessage) error {
			if method != EventClientWifi {
				t.Errorf("event %s", method)
			}
			if err := json.Unmarshal(params, &q); err != nil {
				return err
			}
			return errors.New("got it")
		})
		if err != nil && err.Error() == "got it" {
			break
		}
		if ctx.Err() != nil {
			t.Fatal(err)
		}
		time.Sleep(20 * time.Millisecond)
	}
	if q != (ClientWifiQuestion{Iface: "wlan9", Conn: "Office"}) {
		t.Fatalf("question %+v", q)
	}
	if err := ipc.Call(ctx, sock, MethodStatus, nil, nil); err == nil || !strings.Contains(err.Error(), "waits for the Wi-Fi choice") {
		t.Fatalf("status: %v", err)
	}
	s := func(v string) *string { return &v }
	for _, p := range []HotspotParams{{}, {ClientWifi: s("")}, {ClientWifi: s("maybe")}, {ClientWifi: s("share")},
		{ClientWifi: s("disconnect"), PSK: s("short")}} {
		if err := ipc.Call(ctx, sock, MethodHotspotSet, p, nil); err == nil {
			t.Fatalf("%+v accepted", p)
		}
	}
	select {
	case err := <-done:
		t.Fatalf("returned before a choice: %v", err)
	default:
	}
	var info HotspotInfo
	if err := ipc.Call(ctx, sock, MethodHotspotSet, HotspotParams{ClientWifi: s("disconnect")}, &info); err != nil {
		t.Fatal(err)
	}
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	got, err := LoadHotspotSettings(dir, "test")
	if err != nil || got.ClientWifi != netcfg.ClientWifiDisconnect || info.Settings.ClientWifi != got.ClientWifi {
		t.Fatalf("saved %+v %v", got, err)
	}
}

// movingHotspot puts the AP on xsap0 while the choice is share.
type movingHotspot struct{ fakeHotspot }

func (m *movingHotspot) Up(ctx context.Context, c netcfg.HotspotConfig) (netcfg.HotspotState, error) {
	st, err := m.fakeHotspot.Up(ctx, c)
	if c.ClientWifi == netcfg.ClientWifiShare {
		st.Iface = netcfg.APVirtualIface
	}
	return st, err
}

func TestDaemonClientWifiShare(t *testing.T) {
	dir := shortTemp(t)
	share := netcfg.ClientWifiShare
	s, err := LoadHotspotSettings(dir, "test")
	if err != nil {
		t.Fatal(err)
	}
	s.ClientWifi = share
	if err := SaveHotspotSettings(dir, s); err != nil {
		t.Fatal(err)
	}
	hs := &movingHotspot{}
	d, err := NewDaemon(DaemonConfig{Dir: dir, LanIface: "wlan9", Hotspot: hs, CtlPort: -1, DataPort: -1,
		LanPrefix: netip.MustParsePrefix("127.0.0.1/8"), Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.ctlLn.Close(); _ = d.data.Close() }()
	if d.cfg.LanIface != netcfg.APVirtualIface || d.apIface() != "wlan9" {
		t.Fatalf("lan %s, ap card %s", d.cfg.LanIface, d.apIface())
	}
	ctx := context.Background()
	ssid := "Other"
	if _, err := d.HotspotSet(ctx, HotspotParams{SSID: &ssid}); err != nil {
		t.Fatal(err)
	}
	if last, _, _ := hs.last(); last.Iface != "wlan9" || d.restartReq.Load() {
		t.Fatalf("Up on %s, restart %v", last.Iface, d.restartReq.Load())
	}
	bad := "maybe"
	if _, err := d.HotspotSet(ctx, HotspotParams{ClientWifi: &bad}); err == nil {
		t.Fatal("bad client_wifi accepted")
	}
	disc := netcfg.ClientWifiDisconnect
	if _, err := d.HotspotSet(ctx, HotspotParams{ClientWifi: &disc}); err != nil {
		t.Fatal(err)
	}
	if !d.restartReq.Load() {
		t.Fatal("AP moved off xsap0 without a restart")
	}
}

// connHotspot reports a changing Wi-Fi client connection.
type connHotspot struct {
	fakeHotspot
	conn atomic.Value // string
}

func (c *connHotspot) ClientConn(context.Context, string) (string, error) {
	return c.conn.Load().(string), nil
}

// TestAskClientWifiLeft: the laptop leaves the network (moved, slept) while
// xs-hubd asks: it stops asking and starts again.
func TestAskClientWifiLeft(t *testing.T) {
	dir := shortTemp(t)
	hs := &connHotspot{}
	hs.conn.Store("Office")
	cfg := DaemonConfig{Dir: dir, Socket: filepath.Join(dir, "s.sock"), Hotspot: hs, ClientWifiRecheck: 20 * time.Millisecond,
		Log: slog.New(slog.DiscardHandler)}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- AskClientWifi(ctx, cfg, &netcfg.ClientWifiError{Iface: "wlan9", Conn: "Office"}) }()
	time.Sleep(100 * time.Millisecond)
	select {
	case err := <-done:
		t.Fatalf("returned while still on the network: %v", err)
	default:
	}
	hs.conn.Store("")
	if err := <-done; err != nil {
		t.Fatal(err)
	}
}

// sharingHotspot is a Windows-like hotspot (tunnelSharer) that records
// whether the TUN existed when it started.
type sharingHotspot struct {
	fakeHotspot
	tunMade *bool
	tunAtUp bool
}

func (h *sharingHotspot) Up(ctx context.Context, c netcfg.HotspotConfig) (netcfg.HotspotState, error) {
	h.tunAtUp = *h.tunMade
	return h.fakeHotspot.Up(ctx, c)
}

func (h *sharingHotspot) Reshare(context.Context, netcfg.HotspotConfig) (netcfg.HotspotState, bool, error) {
	return netcfg.HotspotState{}, false, nil
}

// TestEnrolledWindowsHubMakesTUNBeforeHotspot: an enrolled hub with a
// tunnel-sharing hotspot (Windows) creates the TUN first, so Mobile Hotspot
// starts on the tunnel at once; startTunnel reuses that device (phase 19).
func TestEnrolledWindowsHubMakesTUNBeforeHotspot(t *testing.T) {
	dir := shortTemp(t)
	srv, _ := crypto.GenerateKeypair()
	if err := SaveEnrollment(dir, Enrollment{Name: "hub1", ServerKey: crypto.EncodeKey(srv.Public),
		Endpoints: []string{"udp://127.0.0.1:9"}, TunnelIP: "100.64.0.2", MTU: 1280}); err != nil {
		t.Fatal(err)
	}
	made := 0
	tunMade := false
	hs := &sharingHotspot{tunMade: &tunMade}
	d, err := NewDaemon(DaemonConfig{Dir: dir, LanIface: "wlan9", Hotspot: hs, CtlPort: -1, DataPort: -1,
		LanPrefix: netip.MustParsePrefix("127.0.0.1/8"), Log: slog.New(slog.DiscardHandler),
		NewTUN: func(c tun.Config) (tun.Device, error) {
			made++
			tunMade = true
			dev, _ := tun.Pipe(c.Name, "app", c.MTU)
			return dev, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	if !hs.tunAtUp || made != 1 || d.preTUN == nil {
		t.Fatalf("TUN before hotspot %v, made %d, pre %v", hs.tunAtUp, made, d.preTUN)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	eventually(t, "tunnel device in use", func() bool {
		d.mu.Lock()
		defer d.mu.Unlock()
		return d.tunDev != nil && d.preTUN == nil
	})
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if made != 1 {
		t.Fatalf("NewTUN called %d times, want 1 (the early device reused)", made)
	}
}
