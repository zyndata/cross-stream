package hub

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/hub/netcfg"
	"github.com/zyndata/cross-stream/internal/ipc"
	"github.com/zyndata/cross-stream/internal/pairing"
)

type fakeHotspot struct {
	mu       sync.Mutex
	ups      []netcfg.HotspotConfig
	downs    int
	stations int
}

func (f *fakeHotspot) Up(_ context.Context, c netcfg.HotspotConfig) (netcfg.HotspotState, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.ups = append(f.ups, c)
	if c.SSID == "Broken" {
		return netcfg.HotspotState{}, errors.New("activation failed")
	}
	return netcfg.HotspotState{Iface: c.Iface, Band: c.Band, Channel: 149, BSSID: strings.ToLower(c.MAC)}, nil
}

func (f *fakeHotspot) Down(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.downs++
	return nil
}

func (f *fakeHotspot) Stations(context.Context, string) (int, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stations, nil
}

func (f *fakeHotspot) last() (netcfg.HotspotConfig, int, int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.ups[len(f.ups)-1], len(f.ups), f.downs
}

type fakeInhibitor struct {
	mu sync.Mutex
	on bool
}

func (f *fakeInhibitor) Set(on bool) error { f.mu.Lock(); f.on = on; f.mu.Unlock(); return nil }
func (f *fakeInhibitor) Held() bool        { f.mu.Lock(); defer f.mu.Unlock(); return f.on }

func shortTemp(t *testing.T) string {
	t.Helper()
	dir, err := os.MkdirTemp("", "hub") // Unix socket paths are limited to ~100 bytes
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	return dir
}

func TestHotspotDaemonIPC(t *testing.T) {
	dir := shortTemp(t)
	sock := filepath.Join(dir, "s.sock")
	hs := &fakeHotspot{stations: 2}
	inh := &fakeInhibitor{}
	d, err := NewDaemon(DaemonConfig{Dir: dir, LanIface: "wlan9", Hotspot: hs, Inhibitor: inh, Socket: sock,
		CtlPort: -1, DataPort: -1, LanPrefix: netip.MustParsePrefix("127.0.0.1/8"), Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	first, n, _ := hs.last()
	if n != 1 || !strings.HasPrefix(first.SSID, "CrossStream") || first.Band != "a" || first.Country != "PL" ||
		len(first.PSK) != 12 || first.Addr != netcfg.DefaultAPAddr || first.Iface != "wlan9" {
		t.Fatalf("first Up %+v", first)
	}
	// Windows has no file modes: the state directory ACL protects it (xs-hubd).
	if fi, err := os.Stat(filepath.Join(dir, HotspotFile)); err != nil || (runtime.GOOS != "windows" && fi.Mode().Perm() != 0o600) {
		t.Fatalf("hotspot.json %v %v", fi, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	call := func(method string, params, result any) error {
		t.Helper()
		cctx, ccancel := context.WithTimeout(ctx, 5*time.Second)
		defer ccancel()
		var err error
		for range 100 {
			if err = ipc.Call(cctx, sock, method, params, result); err == nil || !strings.Contains(err.Error(), "connect") {
				return err
			}
			time.Sleep(20 * time.Millisecond)
		}
		return err
	}

	var q PairQR
	if err := call(MethodPairingQR, nil, &q); err != nil {
		t.Fatal(err)
	}
	png, err := base64.StdEncoding.DecodeString(q.PNG)
	if err != nil || !bytes.HasPrefix(png, []byte("\x89PNG")) {
		t.Fatalf("png %v", err)
	}
	p, err := pairing.Decode(q.Code)
	if err != nil || p.SSID != first.SSID || p.PSK != first.PSK || p.BSSID != strings.ToLower(first.MAC) {
		t.Fatalf("payload %+v %v", p, err)
	}
	var noPNG PairQR
	if err := call(MethodPairingQR, QRParams{NoPNG: true}, &noPNG); err != nil || noPNG.PNG != "" || noPNG.Code != q.Code {
		t.Fatalf("no_png %v", err)
	}

	ssid := "NewNet"
	var info HotspotInfo
	if err := call(MethodHotspotSet, HotspotParams{SSID: &ssid}, &info); err != nil {
		t.Fatal(err)
	}
	if !info.Enabled || info.Settings.SSID != "NewNet" || info.Settings.PSK != first.PSK || info.State.Channel != 149 {
		t.Fatalf("after set %+v", info)
	}
	if err := call(MethodPairingQR, QRParams{NoPNG: true}, &q); err != nil {
		t.Fatal(err)
	}
	if p, _ := pairing.Decode(q.Code); p.SSID != "NewNet" {
		t.Fatalf("pairing code still has %q", p.SSID)
	}
	if s, err := LoadHotspotSettings(dir, ""); err != nil || s.SSID != "NewNet" {
		t.Fatalf("saved %+v %v", s, err)
	}

	short := "short"
	if err := call(MethodHotspotSet, HotspotParams{PSK: &short}, nil); err == nil || !strings.Contains(err.Error(), "psk") {
		t.Fatalf("short psk: %v", err)
	}
	broken := "Broken"
	if err := call(MethodHotspotSet, HotspotParams{SSID: &broken}, nil); err == nil || !strings.Contains(err.Error(), "restored") {
		t.Fatalf("failing Up: %v", err)
	}
	if last, _, _ := hs.last(); last.SSID != "NewNet" {
		t.Fatalf("previous settings not restored: %q", last.SSID)
	}
	if err := call(MethodHotspotGet, nil, &info); err != nil || info.Settings.SSID != "NewNet" || info.Error != "" {
		t.Fatalf("get %+v %v", info, err)
	}

	var rl []RelayStatus
	if err := call(MethodRelaysList, nil, &rl); err != nil || rl == nil || len(rl) != 0 {
		t.Fatalf("relays.list %v %v", rl, err)
	}

	// Events: status every second; the power check runs on the first tick.
	sctx, scancel := context.WithTimeout(ctx, 5*time.Second)
	defer scancel()
	var got []Status
	err = ipc.Subscribe(sctx, sock, MethodEventsSubscribe, nil, func(method string, params json.RawMessage) error {
		var st Status
		if method != EventStatus || json.Unmarshal(params, &st) != nil {
			t.Fatalf("event %s %s", method, params)
		}
		got = append(got, st)
		if len(got) == 3 {
			return errStop
		}
		return nil
	})
	if !errors.Is(err, errStop) {
		t.Fatalf("subscribe: %v", err)
	}
	last := got[len(got)-1]
	if last.Mode != ModeOffline || last.Clients != 2 || !last.Inhibited || !inh.Held() {
		t.Fatalf("status %+v", last)
	}

	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if _, _, downs := hs.last(); downs != 1 {
		t.Fatalf("hotspot stopped %d times", downs)
	}
	if inh.Held() {
		t.Fatal("inhibitor held after stop")
	}
}

var errStop = errors.New("stop")

func TestHeadlessHotspotSet(t *testing.T) {
	dir := shortTemp(t)
	d, err := NewDaemon(DaemonConfig{Dir: dir, CtlPort: -1, DataPort: -1, LanPrefix: netip.MustParsePrefix("127.0.0.1/8"),
		Log: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = d.ctlLn.Close(); _ = d.data.Close() }()
	ssid := "x"
	if _, err := d.HotspotSet(context.Background(), HotspotParams{SSID: &ssid}); err == nil {
		t.Fatal("hotspot.set in headless mode")
	}
	if info := d.HotspotGet(); info.Enabled {
		t.Fatal("headless hotspot enabled")
	}
	if p, _, _ := d.issuer.Current(); p.SSID != "" || p.PSK != "" {
		t.Fatalf("headless payload has Wi-Fi fields: %+v", p)
	}
}

func TestDefaultHotspot(t *testing.T) {
	s, err := defaultHotspot("my laptop_ÄÖ-1.local")
	if err != nil {
		t.Fatal(err)
	}
	c := hotspotConfig(s, "wlan0", "eth0")
	if err := netcfg.ValidateHotspot(c); err != nil {
		t.Fatal(err)
	}
	if s.SSID != "CrossStream-mylaptop-1local" || c.EthAddr != netcfg.DefaultEthAddr {
		t.Fatalf("defaults %+v", s)
	}
	long, _ := defaultHotspot(strings.Repeat("h", 40))
	if len(long.SSID) != 32 {
		t.Fatalf("long ssid %q", long.SSID)
	}
	other, _ := defaultHotspot("x")
	if other.PSK == s.PSK || other.MAC == s.MAC {
		t.Fatal("defaults not random")
	}
}
