package netcfg

import (
	"context"
	"errors"
	"math"
	"os"
	"strings"
	"testing"
)

func readTestdata(t *testing.T, name string) string {
	t.Helper()
	b, err := os.ReadFile("testdata/" + name)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestParseScan(t *testing.T) {
	scan := ParseScan(readTestdata(t, "iw_scan.txt"))
	if len(scan) != 10 {
		t.Fatalf("%d BSSes, want 10: %+v", len(scan), scan)
	}
	if scan[0] != (BSS{MHz: 5745, Signal: -48}) || scan[9] != (BSS{MHz: 2447, Signal: -75}) {
		t.Fatalf("first/last %+v %+v", scan[0], scan[9])
	}
	if got := ParseScan("BSS aa(on wlan0)\n\tfreq: 2412\n\tsignal: -60.00 dBm\nBSS bb(on wlan0)\n\tSSID: nofreq\n"); len(got) != 1 || got[0].MHz != 2412 {
		t.Fatalf("integer freq / missing freq: %+v", got)
	}
	if got := ParseScan(""); got == nil || len(got) != 0 {
		t.Fatalf("empty scan = %#v, want empty non-nil", got)
	}
}

func TestChannelLoad(t *testing.T) {
	scan := []BSS{{2437, -45}, {2447, -75}, {5745, -95}}
	tests := []struct {
		mhz  int
		want float64
	}{
		{2437, 1 + 0.4*0.6}, // same channel at full weight + channel 8 (2 away)
		{2412, 0},           // channel 1: 5 channels from 6
		{2462, 0.4 * 0.4},   // channel 11: 3 from channel 8
		{5745, 0.05},        // a -95 dBm network still counts a little
		{5765, 0},           // 5 GHz channels do not overlap
	}
	for _, tt := range tests {
		if got := channelLoad(scan, tt.mhz); math.Abs(got-tt.want) > 1e-9 {
			t.Errorf("load(%d) = %v, want %v", tt.mhz, got, tt.want)
		}
	}
}

func TestChooseChannelFromScan(t *testing.T) {
	info := ParsePhy(readTestdata(t, "iw_phy_intel.txt")) // 36-48 no-IR: only 149-165 on 5 GHz
	scan := ParseScan(readTestdata(t, "iw_scan.txt"))
	tests := []struct {
		band     string
		want     int
		wantBand string
		wantCh   int
		why      string
	}{
		{"a", 0, "a", 161, "auto: least loaded channel 161"}, // 149, 153, 157 busy; 36 not allowed anyway
		{"bg", 0, "bg", 11, "auto: least loaded channel 11"}, // 6 strongest, 1 one strong network
		{"a", 153, "a", 153, "configured"},                   // configured channel wins over the scan
		{"a", 36, "a", 161, "auto"},                          // configured but no-IR: automatic
		{"bg", 6, "bg", 6, "configured"},
	}
	for _, tt := range tests {
		c, err := ChooseChannel(info, tt.band, tt.want, "PL", scan)
		if err != nil {
			t.Fatal(err)
		}
		if c.Band != tt.wantBand || c.Channel != tt.wantCh || !strings.HasPrefix(c.Why, tt.why) {
			t.Errorf("ChooseChannel(%s, %d) = %+v, want %s/%d %q", tt.band, tt.want, c, tt.wantBand, tt.wantCh, tt.why)
		}
	}
	// Quiet air: ties go to the preference order.
	c, err := ChooseChannel(info, "a", 0, "PL", []BSS{})
	if err != nil || c.Channel != 149 || !strings.Contains(c.Why, "0 networks") {
		t.Fatalf("empty scan: %+v %v", c, err)
	}
	// No scan: first usable channel, as before phase 14.
	if c, _ := ChooseChannel(info, "bg", 0, "PL", nil); c.Channel != 1 || !strings.Contains(c.Why, "no scan") {
		t.Fatalf("nil scan: %+v", c)
	}
}

func TestScanSummary(t *testing.T) {
	info := ParsePhy(readTestdata(t, "iw_phy_intel.txt"))
	got := ScanSummary(info, ParseScan(readTestdata(t, "iw_scan.txt")))
	want := "1: 1 (-50 dBm), 6: 2 (-45 dBm), 8: 1 (-75 dBm), 11: 1 (-80 dBm), 36: 1 (-40 dBm), 149: 2 (-48 dBm), 153: 1 (-60 dBm), 157: 1 (-85 dBm)"
	if got != want {
		t.Fatalf("summary\n got %s\nwant %s", got, want)
	}
	if ScanSummary(info, nil) != "no networks seen" {
		t.Fatal("empty summary")
	}
}

func TestScanNeighboursRetries(t *testing.T) {
	var cmds []string
	calls := 0
	run := func(_ context.Context, _, name string, args ...string) (string, error) {
		cmds = append(cmds, name+" "+strings.Join(args, " "))
		if name == "iw" {
			calls++
			if calls == 1 {
				return "", errors.New("command failed: Network is down (-100)")
			}
		}
		return "BSS aa(on wlan9)\n\tfreq: 2412.0\n\tsignal: -50.00 dBm\n", nil
	}
	scan, err := ScanNeighbours(context.Background(), run, "wlan9")
	if err != nil || len(scan) != 1 {
		t.Fatalf("scan %+v %v", scan, err)
	}
	if strings.Join(cmds, "|") != "iw dev wlan9 scan|ip link set dev wlan9 up|iw dev wlan9 scan" {
		t.Fatalf("commands %q", cmds)
	}
	fail := func(context.Context, string, string, ...string) (string, error) {
		return "", errors.New("command failed: Operation not supported (-95)")
	}
	if _, err := ScanNeighbours(context.Background(), fail, "wlan9"); err == nil {
		t.Fatal("failed scan returned no error")
	}
}
