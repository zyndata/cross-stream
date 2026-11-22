//go:build !windows

package netcfg

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func testHotspotConfig() HotspotConfig {
	return HotspotConfig{Iface: "wlan9", SSID: "CrossStream-test", PSK: "secret-psk-123", Band: "a", Country: "PL",
		MAC: "02:aa:bb:cc:dd:ee", Addr: DefaultAPAddr}
}

func TestValidateHotspot(t *testing.T) {
	tests := []struct {
		name string
		mod  func(*HotspotConfig)
		ok   bool
	}{
		{"valid", func(*HotspotConfig) {}, true},
		{"empty ssid", func(c *HotspotConfig) { c.SSID = "" }, false},
		{"long ssid", func(c *HotspotConfig) { c.SSID = strings.Repeat("x", 33) }, false},
		{"short psk", func(c *HotspotConfig) { c.PSK = "1234567" }, false},
		{"backslash psk", func(c *HotspotConfig) { c.PSK = `abc\defgh` }, false},
		{"newline ssid", func(c *HotspotConfig) { c.SSID = "a\nb" }, false},
		{"leading space", func(c *HotspotConfig) { c.SSID = " net" }, false},
		{"band", func(c *HotspotConfig) { c.Band = "6" }, false},
		{"country", func(c *HotspotConfig) { c.Country = "pl" }, false},
		{"multicast mac", func(c *HotspotConfig) { c.MAC = "01:00:5e:00:00:01" }, false},
		{"channel", func(c *HotspotConfig) { c.Channel = 300 }, false},
	}
	for _, tc := range tests {
		c := testHotspotConfig()
		tc.mod(&c)
		if err := ValidateHotspot(c); (err == nil) != tc.ok {
			t.Errorf("%s: %v", tc.name, err)
		}
	}
}

// fakeNM is a Runner that plays iw and nmcli.
type fakeNM struct {
	cmds    []string
	phy     string
	devices string
	failUp  map[string]bool // "band=a" etc: fail `connection up` when the AP keyfile holds it
	nmDir   string
	scan    string // iw dev <iface> scan output
	vif     bool   // APVirtualIface exists
	vifNM   string // its NetworkManager state ("" = disconnected)
}

func (f *fakeNM) run(_ context.Context, _, name string, args ...string) (string, error) {
	c := name + " " + strings.Join(args, " ")
	f.cmds = append(f.cmds, c)
	switch {
	case strings.HasPrefix(c, "iw phy"):
		return f.phy, nil
	case strings.HasPrefix(c, "nmcli -t"):
		if f.vif {
			st := f.vifNM
			if st == "" {
				st = "disconnected"
			}
			return f.devices + APVirtualIface + ":wifi:" + st + ":\n", nil
		}
		return f.devices, nil
	case strings.HasPrefix(c, "iw dev wlan9 interface add "+APVirtualIface):
		f.vif = true
	case c == "iw dev "+APVirtualIface+" del":
		f.vif = false
	case c == "iw dev "+APVirtualIface+" info" && !f.vif:
		return "", errors.New("No such device")
	case strings.HasPrefix(c, "nmcli --wait 30 connection up id "+APConnection):
		kf, _ := os.ReadFile(filepath.Join(f.nmDir, "system-connections", APConnection+".nmconnection"))
		for k := range f.failUp {
			if strings.Contains(string(kf), k) {
				return "", errors.New("activation failed")
			}
		}
	case strings.HasPrefix(c, "nmcli --wait 5 connection up id "+EthConnection):
		return "", errors.New("no carrier")
	case strings.HasPrefix(c, "iw dev") && strings.HasSuffix(c, " scan"):
		return f.scan, nil
	case strings.HasPrefix(c, "iw dev") && strings.HasSuffix(c, " info"):
		kf, _ := os.ReadFile(filepath.Join(f.nmDir, "system-connections", APConnection+".nmconnection"))
		w := 20
		if strings.Contains(string(kf), "channel-width=40") {
			w = 40
		}
		return fmt.Sprintf("Interface wlan9\n\ttype AP\n\tchannel 161 (5805 MHz), width: %d MHz, center1: 5815 MHz\n", w), nil
	case strings.HasPrefix(c, "iw dev"):
		return "Station aa:bb:cc:00:00:01 (on wlan9)\n\tinactive time: 10 ms\nStation aa:bb:cc:00:00:02 (on wlan9)\n", nil
	}
	return "", nil
}

func newTestHotspot(t *testing.T, f *fakeNM) *Hotspot {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "sys/class/net/wlan9/phy80211"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "sys/class/net/wlan9/phy80211/name"), []byte("phy7\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	b, err := os.ReadFile("testdata/iw_phy_intel.txt")
	if err != nil {
		t.Fatal(err)
	}
	f.phy, f.nmDir = string(b), filepath.Join(root, "nm")
	if f.devices == "" {
		f.devices = "wlan9:wifi:disconnected:\neth0:ethernet:unavailable:\n"
	}
	return &Hotspot{NMDir: f.nmDir, SysRoot: root, Run: f.run, DeviceWait: 100 * time.Millisecond,
		WaitAddr: func(context.Context, string, netip.Addr) error { return nil }}
}

func TestHotspotUpDown(t *testing.T) {
	f := &fakeNM{scan: readTestdata(t, "iw_scan.txt")}
	h := newTestHotspot(t, f)
	c := testHotspotConfig()
	c.EthIface, c.EthAddr = "eth0", DefaultEthAddr
	ctx := context.Background()
	st, err := h.Up(ctx, c)
	if err != nil {
		t.Fatal(err)
	}
	if st.Band != "a" || st.Channel != 161 || st.BSSID != "02:aa:bb:cc:dd:ee" ||
		!strings.HasPrefix(st.ChannelWhy, "auto: least loaded channel 161") || !strings.Contains(st.Scan, "149: 2 (-48 dBm)") {
		t.Fatalf("state %+v", st)
	}
	all := strings.Join(f.cmds, "|")
	down, scan, up := strings.Index(all, "nmcli connection down id "+APConnection), strings.Index(all, "iw dev wlan9 scan"),
		strings.Index(all, "nmcli --wait 30 connection up id "+APConnection)
	if down < 0 || scan < down || up < scan {
		t.Fatalf("want AP down, scan, AP up in order: %q", f.cmds)
	}
	if len(st.Warnings) != 1 || !strings.Contains(st.Warnings[0], "Ethernet share port eth0") {
		t.Fatalf("warnings %q", st.Warnings)
	}
	ap := filepath.Join(f.nmDir, "system-connections", APConnection+".nmconnection")
	kf, err := os.ReadFile(ap)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"mode=ap", "ssid=CrossStream-test", "band=a", "channel=161", "cloned-mac-address=02:AA:BB:CC:DD:EE",
		"psk=secret-psk-123", "method=shared", "address1=10.42.0.1/24", "[ipv6]\nmethod=disabled", "autoconnect-priority=100",
		"interface-name=wlan9"} {
		if !strings.Contains(string(kf), want) {
			t.Fatalf("AP keyfile lacks %q:\n%s", want, kf)
		}
	}
	if fi, _ := os.Stat(ap); fi.Mode().Perm() != 0o600 {
		t.Fatalf("keyfile mode %v", fi.Mode())
	}
	eth, err := os.ReadFile(filepath.Join(f.nmDir, "system-connections", EthConnection+".nmconnection"))
	if err != nil || !strings.Contains(string(eth), "address1=10.42.1.1/24") {
		t.Fatalf("eth keyfile %s %v", eth, err)
	}
	dm, err := os.ReadFile(filepath.Join(f.nmDir, dnsmasqConf))
	if err != nil || !strings.Contains(string(dm), "port=0") {
		t.Fatalf("dnsmasq drop-in %q %v", dm, err)
	}
	for _, c := range f.cmds {
		if strings.Contains(c, "secret-psk") {
			t.Fatalf("PSK on a command line: %s", c)
		}
	}
	if !strings.Contains(strings.Join(f.cmds, "|"), "iw reg set PL") {
		t.Fatalf("no regulatory domain: %q", f.cmds)
	}
	// Up again is fine (idempotent) and keeps the same profile UUID.
	if _, err := h.Up(ctx, c); err != nil {
		t.Fatal(err)
	}
	kf2, _ := os.ReadFile(ap)
	if string(kf2) != string(kf) {
		t.Fatal("second Up changed the profile")
	}
	if n, err := h.Stations(ctx, "wlan9"); err != nil || n != 2 {
		t.Fatalf("stations %d %v", n, err)
	}
	f.cmds = nil
	if err := h.Down(ctx); err != nil {
		t.Fatal(err)
	}
	want := "nmcli connection delete id " + APConnection + "|nmcli connection delete id " + EthConnection
	if strings.Join(f.cmds, "|") != want {
		t.Fatalf("down ran %q", f.cmds)
	}
	for _, p := range []string{ap, filepath.Join(f.nmDir, dnsmasqConf)} {
		if _, err := os.Stat(p); err == nil {
			t.Fatalf("%s left behind", p)
		}
	}
	f.cmds = nil
	if err := h.Down(ctx); err != nil || len(f.cmds) != 0 {
		t.Fatalf("second Down: %v %q", err, f.cmds)
	}
}

func TestHotspotFallback24(t *testing.T) {
	f := &fakeNM{failUp: map[string]bool{"band=a": true}, scan: readTestdata(t, "iw_scan.txt")}
	h := newTestHotspot(t, f)
	st, err := h.Up(context.Background(), testHotspotConfig())
	if err != nil {
		t.Fatal(err)
	}
	// The 2.4 GHz retry also takes the least loaded channel from the same scan.
	if st.Band != "bg" || st.Channel != 11 || len(st.Warnings) != 1 || !strings.Contains(st.Warnings[0], "2.4 GHz") ||
		!strings.Contains(st.ChannelWhy, "channel 11") {
		t.Fatalf("state %+v", st)
	}
	if n := strings.Count(strings.Join(f.cmds, "|"), " scan"); n != 1 {
		t.Fatalf("%d scans, want 1", n)
	}

	// A configured channel skips the scan.
	f = &fakeNM{}
	h = newTestHotspot(t, f)
	c := testHotspotConfig()
	c.Channel = 157
	if st, err = h.Up(context.Background(), c); err != nil || st.Channel != 157 || st.ChannelWhy != "configured" {
		t.Fatalf("configured: %+v %v", st, err)
	}
	if strings.Contains(strings.Join(f.cmds, "|"), "scan") {
		t.Fatalf("scanned with a configured channel: %q", f.cmds)
	}
}

func TestHotspotPreflightErrors(t *testing.T) {
	f := &fakeNM{devices: "wlan9:wifi:connected:Home\n"}
	h := newTestHotspot(t, f)
	if _, err := h.Up(context.Background(), testHotspotConfig()); err == nil || !strings.Contains(err.Error(), "as a client") {
		t.Fatalf("client conflict: %v", err)
	}
	f = &fakeNM{}
	h = newTestHotspot(t, f)
	c := testHotspotConfig()
	c.Iface = "eth0"
	if _, err := h.Up(context.Background(), c); err == nil || !strings.Contains(err.Error(), "not a Wi-Fi interface") {
		t.Fatalf("non-wifi: %v", err)
	}
	f = &fakeNM{}
	h = newTestHotspot(t, f)
	f.phy = "Wiphy phy7\n\tSupported interface modes:\n\t\t * managed\n\t\t * monitor\n\tBand 1:\n"
	h.Run = func(ctx context.Context, stdin, name string, args ...string) (string, error) {
		if name == "iw" && args[0] == "phy" {
			return f.phy, nil
		}
		return f.run(ctx, stdin, name, args...)
	}
	if _, err := h.Up(context.Background(), testHotspotConfig()); err == nil || !strings.Contains(err.Error(), "no AP mode") {
		t.Fatalf("no AP: %v", err)
	}
}

func TestProfileUUID(t *testing.T) {
	u := profileUUID(APConnection, "wlan0")
	if len(u) != 36 || u[14] != '4' || u != profileUUID(APConnection, "wlan0") || u == profileUUID(EthConnection, "wlan0") {
		t.Fatalf("uuid %s", u)
	}
}

func TestInhibitor(t *testing.T) {
	if _, err := exec.LookPath("cat"); err != nil {
		t.Skip("no cat")
	}
	i := &Inhibitor{Command: []string{"cat"}}
	if i.Held() {
		t.Fatal("held before Set")
	}
	if err := i.Set(true); err != nil {
		t.Fatal(err)
	}
	if err := i.Set(true); err != nil || !i.Held() {
		t.Fatalf("not held: %v", err)
	}
	if err := i.Set(false); err != nil || i.Held() {
		t.Fatalf("still held: %v", err)
	}
	bad := &Inhibitor{Command: []string{"false"}}
	if err := bad.Set(true); err != nil {
		t.Skip("no false")
	}
	deadline := time.Now().Add(2 * time.Second)
	for bad.Held() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if bad.Held() {
		t.Fatal("exited helper still reported held")
	}
}

// TestHotspot40MHz: an HT40 card's 5 GHz AP asks NetworkManager for 40 MHz
// (automatic gave 20 MHz on an Intel AX201) and falls back to the automatic
// width when the AP does not start so.
func TestHotspot40MHz(t *testing.T) {
	tests := []struct {
		name      string
		failUp    map[string]bool
		wantWidth int
		wantWarn  string
	}{
		{"40 MHz", nil, 40, ""},
		{"40 MHz refused", map[string]bool{"channel-width=40": true}, 20, "automatic width"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			f := &fakeNM{failUp: tt.failUp, scan: readTestdata(t, "iw_scan.txt")}
			h := newTestHotspot(t, f)
			st, err := h.Up(context.Background(), testHotspotConfig())
			if err != nil {
				t.Fatal(err)
			}
			if st.Band != "a" || st.Width != tt.wantWidth {
				t.Fatalf("band %s width %d, want a / %d (%+v)", st.Band, st.Width, tt.wantWidth, st)
			}
			if got := strings.Join(st.Warnings, "|"); (tt.wantWarn == "") != (got == "") || !strings.Contains(got, tt.wantWarn) {
				t.Fatalf("warnings %q, want %q", got, tt.wantWarn)
			}
		})
	}
	if w := parseWidth("\tchannel 149 (5745 MHz), width: 20 MHz, center1: 5745 MHz\n"); w != 20 {
		t.Fatalf("parseWidth %d", w)
	}
	if !ParsePhy(readTestdata(t, "iw_phy_intel.txt")).HT40 || ParsePhy("Wiphy phy0\n").HT40 {
		t.Fatal("HT40 detection")
	}
}
