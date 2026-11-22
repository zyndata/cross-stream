//go:build !windows

package netcfg

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestClientConflict(t *testing.T) {
	ds := []Device{{"wlo1", "wifi", "connected", "Home 5G"}, {"wlx0", "wifi", "disconnected", ""},
		{"wlo2", "wifi", "connected", APConnection}}
	if cw := clientConflict(ds, "wlx0", PhyInfo{}); cw != nil {
		t.Fatal(cw)
	}
	if cw := clientConflict(ds, "wlo2", PhyInfo{}); cw != nil {
		t.Fatal(cw)
	}
	cw := clientConflict(ds, "wlo1", PhyInfo{})
	if cw == nil || cw.Conn != "Home 5G" || cw.CanShare || !strings.Contains(cw.Error(), "nmcli device disconnect wlo1") ||
		strings.Contains(cw.Error(), "__ap") {
		t.Fatalf("%v", cw)
	}
	cw = clientConflict(ds, "wlo1", PhyInfo{APWithManaged: true})
	if cw == nil || !cw.CanShare || !strings.Contains(cw.Error(), "interface add xsap0 type __ap") {
		t.Fatalf("%v", cw)
	}
}

func TestClientChannel(t *testing.T) {
	tests := []struct {
		out  string
		band string
		ch   int
		err  bool
	}{
		{"Interface wlo1\n\ttype managed\n\tchannel 36 (5180 MHz), width: 80 MHz, center1: 5210 MHz\n", "a", 36, false},
		{"\tchannel 6 (2437 MHz), width: 20 MHz, center1: 2437 MHz\n", "bg", 6, false},
		{"\tchannel 37 (6135 MHz), width: 160 MHz\n", "", 0, true},
		{"Interface wlo1\n\ttype managed\n", "", 0, true},
	}
	for _, tc := range tests {
		band, ch, err := clientChannel(tc.out)
		if band != tc.band || ch != tc.ch || (err != nil) != tc.err {
			t.Errorf("%q: %s %d %v", tc.out, band, ch, err)
		}
	}
}

func TestValidClientWifi(t *testing.T) {
	for s, ok := range map[string]bool{"": true, "share": true, "disconnect": true, "ask": false, "Share": false} {
		if ValidClientWifi(s) != ok {
			t.Errorf("%q", s)
		}
	}
}

// TestHotspotClientWifi: the AP card is a Wi-Fi client ("Office" on
// channel 161); each ClientWifi choice.
func TestHotspotClientWifi(t *testing.T) {
	const client = "wlan9:wifi:connected:Office\neth0:ethernet:unavailable:\n"
	tests := []struct {
		name     string
		choice   string
		failUp   string // AP keyfile text that fails `connection up`
		vifNM    string // NetworkManager state of xsap0
		wantErr  bool
		tried    string // ClientWifiError.Tried (wantErr)
		iface    string // AP interface (no error)
		wantCmd  string
		wantVif  bool
		noCmd    string
		keyfile  []string
		rejected []string
	}{
		{name: "ask", wantErr: true, noCmd: "connection up"},
		{name: "disconnect", choice: ClientWifiDisconnect, iface: "wlan9", wantCmd: "nmcli device disconnect wlan9",
			keyfile: []string{"interface-name=wlan9"}},
		{name: "share", choice: ClientWifiShare, iface: APVirtualIface, wantVif: true,
			wantCmd: "iw dev wlan9 interface add xsap0 type __ap addr 02:aa:bb:cc:dd:ee", noCmd: " scan",
			keyfile: []string{"interface-name=xsap0", "band=a", "channel=161"}, rejected: []string{"channel-width"}},
		{name: "share fails", choice: ClientWifiShare, failUp: "interface-name=xsap0", wantErr: true, tried: ClientWifiShare,
			wantCmd: "iw dev xsap0 del"},
		{name: "share unavailable", choice: ClientWifiShare, vifNM: "unavailable", wantErr: true, tried: ClientWifiShare,
			wantCmd: "iw dev xsap0 del", noCmd: "connection up"},
		{name: "bad choice", choice: "ask", wantErr: true, noCmd: "connection up"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			f := &fakeNM{devices: client, vifNM: tc.vifNM}
			if tc.failUp != "" {
				f.failUp = map[string]bool{tc.failUp: true}
			}
			h := newTestHotspot(t, f)
			twoClients(t, f)
			c := testHotspotConfig()
			c.ClientWifi = tc.choice
			st, err := h.Up(context.Background(), c)
			all := strings.Join(f.cmds, "|")
			if tc.wantCmd != "" && !strings.Contains(all, tc.wantCmd) {
				t.Errorf("want %q in %q", tc.wantCmd, f.cmds)
			}
			if tc.noCmd != "" && strings.Contains(all, tc.noCmd) {
				t.Errorf("unexpected %q in %q", tc.noCmd, f.cmds)
			}
			if f.vif != tc.wantVif {
				t.Errorf("vif exists = %v", f.vif)
			}
			if tc.wantErr {
				var cw *ClientWifiError
				if tc.name != "bad choice" && (!errors.As(err, &cw) || cw.Conn != "Office" || !cw.CanShare || cw.Tried != tc.tried ||
					(tc.tried != "") != (cw.Err != nil)) {
					t.Fatalf("want ClientWifiError, got %#v", err)
				}
				if err == nil {
					t.Fatal("no error")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if st.Iface != tc.iface {
				t.Errorf("iface %q", st.Iface)
			}
			kf, _ := os.ReadFile(filepath.Join(f.nmDir, "system-connections", APConnection+".nmconnection"))
			for _, k := range tc.keyfile {
				if !strings.Contains(string(kf), k+"\n") {
					t.Errorf("keyfile lacks %q:\n%s", k, kf)
				}
			}
			for _, k := range tc.rejected {
				if strings.Contains(string(kf), k) {
					t.Errorf("keyfile has %q", k)
				}
			}
		})
	}
}

// twoClients makes the Intel test card allow two managed interfaces (a
// card that can share; the real AX201 cannot).
func twoClients(t *testing.T, f *fakeNM) {
	t.Helper()
	const intel = "#{ managed } <= 1, #{ AP, P2P-client, P2P-GO } <= 1"
	if !strings.Contains(f.phy, intel) {
		t.Fatal("testdata combination changed")
	}
	f.phy = strings.Replace(f.phy, intel, "#{ managed } <= 2, #{ AP, P2P-client, P2P-GO } <= 1", 1)
}

// TestHotspotIntelCannotShare: the AX201's combinations do not allow a
// share; the saved choice asks again with share hidden.
func TestHotspotIntelCannotShare(t *testing.T) {
	f := &fakeNM{devices: "wlan9:wifi:connected:Office\n"}
	h := newTestHotspot(t, f)
	c := testHotspotConfig()
	c.ClientWifi = ClientWifiShare
	_, err := h.Up(context.Background(), c)
	var cw *ClientWifiError
	if !errors.As(err, &cw) || cw.CanShare || cw.Err != nil || f.vif || strings.Contains(err.Error(), "__ap") {
		t.Fatalf("%v vif=%v", err, f.vif)
	}
}

// TestHotspotShareDown: Down deletes the virtual AP interface; a later Up
// with the client gone moves the AP back onto the card.
func TestHotspotShareDown(t *testing.T) {
	f := &fakeNM{devices: "wlan9:wifi:connected:Office\n"}
	h := newTestHotspot(t, f)
	twoClients(t, f)
	c := testHotspotConfig()
	c.ClientWifi = ClientWifiShare
	ctx := context.Background()
	if _, err := h.Up(ctx, c); err != nil || !f.vif {
		t.Fatalf("%v vif=%v", err, f.vif)
	}
	if _, err := h.Up(ctx, c); err != nil || strings.Count(strings.Join(f.cmds, "|"), "interface add") != 1 {
		t.Fatalf("second Up re-adds the interface: %v %q", err, f.cmds)
	}
	f.devices = "wlan9:wifi:disconnected:\n"
	st, err := h.Up(ctx, c)
	if err != nil || f.vif || st.Iface != "wlan9" {
		t.Fatalf("client gone: %v vif=%v iface=%s", err, f.vif, st.Iface)
	}
	if _, err := h.Up(ctx, c); err != nil {
		t.Fatal(err)
	}
	f.devices = "wlan9:wifi:connected:Office\n"
	if _, err := h.Up(ctx, c); err != nil {
		t.Fatal(err)
	}
	if err := h.Down(ctx); err != nil || f.vif {
		t.Fatalf("down: %v vif=%v", err, f.vif)
	}
}

func TestClientConn(t *testing.T) {
	f := &fakeNM{devices: "wlan9:wifi:connected:Office\n"}
	h := newTestHotspot(t, f)
	ctx := context.Background()
	if c, err := h.ClientConn(ctx, "wlan9"); err != nil || c != "Office" {
		t.Fatalf("%q %v", c, err)
	}
	f.devices = "wlan9:wifi:connected:" + APConnection + "\n"
	if c, err := h.ClientConn(ctx, "wlan9"); err != nil || c != "" {
		t.Fatalf("our AP counted as a client: %q %v", c, err)
	}
	f.devices = "wlan9:wifi:disconnected:\n"
	if c, err := h.ClientConn(ctx, "wlan9"); err != nil || c != "" {
		t.Fatalf("%q %v", c, err)
	}
}
