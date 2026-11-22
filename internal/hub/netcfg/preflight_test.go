package netcfg

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParsePhyIntel(t *testing.T) {
	b, err := os.ReadFile("testdata/iw_phy_intel.txt")
	if err != nil {
		t.Fatal(err)
	}
	info := ParsePhy(string(b))
	if !info.AP || info.APWithManaged { // AP + 1 managed, but never 2 managed (phase 18, EBUSY)
		t.Fatalf("AP %v, AP+managed %v", info.AP, info.APWithManaged)
	}
	byNum := map[int]Channel{}
	for _, c := range info.Channels {
		byNum[c.Num] = c
	}
	for _, tc := range []struct {
		num                   int
		noIR, disabled, radar bool
	}{
		{1, false, false, false}, {14, false, true, false}, {36, true, false, false},
		{52, true, false, true}, {68, false, true, false}, {149, false, false, false},
	} {
		c, ok := byNum[tc.num]
		if !ok || c.NoIR != tc.noIR || c.Disabled != tc.disabled || c.Radar != tc.radar {
			t.Errorf("channel %d: %+v (found %v)", tc.num, c, ok)
		}
	}
	if byNum[149].MHz != 5745 {
		t.Fatalf("149 MHz %d", byNum[149].MHz)
	}
}

func TestComboAPManaged(t *testing.T) {
	tests := []struct {
		combo string
		want  bool
	}{
		{"* #{ managed } <= 1, #{ AP, P2P-client, P2P-GO } <= 1, #{ P2P-device } <= 1, total <= 3, #channels <= 1", false}, // Intel AX201
		{"* #{ managed } <= 2048, #{ AP, mesh point } <= 8, total <= 2048, #channels <= 1", true},
		{"* #{ managed } <= 2, #{ AP } <= 1, total <= 3, #channels <= 1", true},
		{"* #{ managed } <= 1, #{ P2P-client, P2P-GO } <= 1, #{ P2P-device } <= 1, total <= 3, #channels <= 2", false},
		{"* #{ managed, AP } <= 1, total <= 1, #channels <= 1", false},
		{"* #{ managed, AP } <= 2, total <= 2, #channels <= 1", true},
		{"* #{ managed } <= 1, #{ AP } <= 1, total <= 1, #channels <= 1", false},
	}
	for _, tc := range tests {
		if got := comboAPManaged(tc.combo); got != tc.want {
			t.Errorf("%q: %v", tc.combo, got)
		}
	}
}

func TestPickChannel(t *testing.T) {
	five := []Channel{{Num: 36, MHz: 5180, NoIR: true}, {Num: 52, MHz: 5260, Radar: true}, {Num: 149, MHz: 5745}}
	two := []Channel{{Num: 1, MHz: 2412}, {Num: 6, MHz: 2437}, {Num: 11, MHz: 2462}, {Num: 13, MHz: 2472}}
	noIR5 := []Channel{{Num: 36, MHz: 5180, NoIR: true}, {Num: 149, MHz: 5745, NoIR: true}}
	tests := []struct {
		name    string
		chans   []Channel
		band    string
		want    int
		gotBand string
		gotCh   int
		warn    bool
		wantErr bool
	}{
		{"5 GHz first usable", append(five, two...), "a", 0, "a", 149, false, false},
		{"5 GHz configured", append([]Channel{{Num: 44, MHz: 5220}}, five...), "a", 44, "a", 44, false, false},
		{"configured no-IR skipped", append(five, two...), "a", 36, "a", 149, false, false},
		{"5 GHz all no-IR", append(noIR5, two...), "a", 0, "bg", 1, true, false},
		{"2.4 configured", two, "bg", 13, "bg", 13, false, false},
		{"2.4 default", two, "bg", 0, "bg", 1, false, false},
		{"no channel list", nil, "a", 0, "a", 0, false, false},
		{"nothing usable", noIR5, "a", 0, "", 0, true, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			band, ch, warn, err := PickChannel(PhyInfo{AP: true, Channels: tc.chans}, tc.band, tc.want, "PL")
			if (err != nil) != tc.wantErr || band != tc.gotBand || ch != tc.gotCh || (warn != "") != tc.warn {
				t.Fatalf("got %q %d warn %q err %v", band, ch, warn, err)
			}
		})
	}
}

func TestRfkill(t *testing.T) {
	tests := []struct {
		typ, soft, hard string
		want            string
	}{
		{"wlan", "0", "0", ""},
		{"wlan", "1", "0", "rfkill unblock wifi"},
		{"wlan", "0", "1", "hardware switch"},
		{"bluetooth", "1", "1", ""},
	}
	for _, tc := range tests {
		root := t.TempDir()
		d := filepath.Join(root, "sys/class/rfkill/rfkill0")
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
		for f, v := range map[string]string{"type": tc.typ, "soft": tc.soft, "hard": tc.hard} {
			if err := os.WriteFile(filepath.Join(d, f), []byte(v+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		err := rfkillBlocked(root)
		if (err == nil) != (tc.want == "") || (err != nil && !strings.Contains(err.Error(), tc.want)) {
			t.Errorf("%+v: %v", tc, err)
		}
	}
}

func TestSplitTerse(t *testing.T) {
	got := splitTerse(`wlo1:wifi:connected:My\:Net\\5G`)
	want := []string{"wlo1", "wifi", "connected", `My:Net\5G`}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Fatalf("%q", got)
	}
}
