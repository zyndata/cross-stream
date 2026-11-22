package netcfg

import (
	"context"
	"net"
	"net/netip"
	"testing"
	"time"
)

func TestParseWinHotspot(t *testing.T) {
	st := parseWinHotspot("warn=5 GHz not supported\r\nband=bg\r\nshared=xs0\r\niface=Połączenie lokalne* 10\r\nmac=0A-11-22-33-44-55\r\naddr=192.168.137.1/24\r\n")
	if st.Iface != "Połączenie lokalne* 10" || st.BSSID != "0a:11:22:33:44:55" || st.Band != "bg" || len(st.Warnings) != 1 ||
		st.Shared != "xs0" || st.Addr != netip.MustParsePrefix("192.168.137.1/24") {
		t.Fatalf("state %+v", st)
	}
}

const arpOut = `
Interfejs: 10.42.0.1 --- 0x1a
  Adres internetowy      Adres fizyczny        Typ
  10.42.0.23            aa-bb-cc-dd-ee-01     dynamiczne
  10.42.0.24            aa-bb-cc-dd-ee-02     dynamiczne
  10.42.0.255           ff-ff-ff-ff-ff-ff     statyczne
  224.0.0.22            01-00-5e-00-00-16     statyczne
`

func TestParseWinHotspotLinkLocal(t *testing.T) {
	// A hotspot adapter that has not got its ICS address yet must not become the LAN.
	if st := parseWinHotspot("iface=Połączenie lokalne* 10\naddr=169.254.51.174/16\n"); st.Addr.IsValid() {
		t.Fatalf("link-local address accepted: %v", st.Addr)
	}
}

func TestCountNeighbours(t *testing.T) {
	if n := countNeighbours(arpOut, netip.MustParsePrefix("10.42.0.1/24")); n != 2 {
		t.Fatalf("count = %d, want 2", n)
	}
}

func TestArpWinIP(t *testing.T) {
	mac, _ := net.ParseMAC("aa:bb:cc:dd:ee:02")
	if a, ok := arpWinIP(arpOut, mac); !ok || a != netip.MustParseAddr("10.42.0.24") {
		t.Fatalf("got %v %v", a, ok)
	}
	other, _ := net.ParseMAC("aa:bb:cc:dd:ee:09")
	if _, ok := arpWinIP(arpOut, other); ok {
		t.Fatal("unknown MAC resolved")
	}
}

func TestPowerRequest(t *testing.T) {
	var p PowerRequest
	for _, on := range []bool{true, true, false} {
		if err := p.Set(on); err != nil {
			t.Fatal(err)
		}
		if p.Held() != on {
			t.Fatalf("held %v after Set(%v)", p.Held(), on)
		}
	}
}

func TestStationsNoReviveDuringStart(t *testing.T) {
	calls := 0
	h := &WinHotspot{Run: func(context.Context, string, string, ...string) (string, error) {
		calls++
		return "", nil
	}}
	c := HotspotConfig{SSID: "XS-Test", PSK: "crossstream1", Band: "a", Country: "PL", MAC: "02:11:22:33:44:55"}
	h.want = &c
	h.op.Lock() // a Reshare is running
	if _, err := h.Stations(context.Background(), "no-such-adapter"); err == nil {
		t.Fatal("missing adapter not reported")
	}
	h.op.Unlock()
	if calls != 0 {
		t.Fatalf("hotspot restarted during a running start (%d runs)", calls)
	}
	h.revived = time.Now() // just started: wait reviveEvery
	if _, err := h.Stations(context.Background(), "no-such-adapter"); err == nil || calls != 0 {
		t.Fatalf("restarted right after a start: err=%v runs=%d", err, calls)
	}
}
