package wfp

import (
	"fmt"
	"strings"
	"testing"
)

func describe(fs []Filter) string {
	var b []string
	for _, f := range fs {
		act := "block"
		if f.Permit {
			act = "permit"
		}
		cond := "all"
		switch {
		case f.App != "":
			cond = "app=" + f.App
		case f.Loopback:
			cond = "loopback"
		case f.LocalLUID != 0:
			cond = fmt.Sprintf("luid=%#x", f.LocalLUID)
		case f.RemotePort != 0:
			cond = fmt.Sprintf("port=%d", f.RemotePort)
		case f.RemoteNet.IsValid():
			cond = "net=" + f.RemoteNet.String()
		}
		b = append(b, fmt.Sprintf("%s %s %s/%d", f.Layer, act, cond, f.Weight))
	}
	return strings.Join(b, "\n")
}

func TestWanted(t *testing.T) {
	tests := []struct {
		name   string
		active bool
		luid   uint64
		app    string
		want   string
	}{
		{"no path: nothing", false, 7, `C:\x\xs-hubd.exe`, ""},
		{"no tunnel: nothing", true, 0, `C:\x\xs-hubd.exe`, ""},
		{"carrying", true, 0x70, `C:\x\xs-hubd.exe`, strings.Join([]string{
			`connect-v4 permit app=C:\x\xs-hubd.exe/12`,
			"connect-v4 permit loopback/12",
			"connect-v4 permit luid=0x70/12",
			`connect-v6 permit app=C:\x\xs-hubd.exe/12`,
			"connect-v6 permit loopback/12",
			"connect-v6 permit luid=0x70/12",
			"connect-v4 block port=53/4",
			"connect-v6 permit net=fe80::/10/12",
			"connect-v6 permit net=ff00::/8/12",
			"connect-v6 block all/4",
		}, "\n")},
		{"no app path", true, 0x70, "", strings.Join([]string{
			"connect-v4 permit loopback/12",
			"connect-v4 permit luid=0x70/12",
			"connect-v6 permit loopback/12",
			"connect-v6 permit luid=0x70/12",
			"connect-v4 block port=53/4",
			"connect-v6 permit net=fe80::/10/12",
			"connect-v6 permit net=ff00::/8/12",
			"connect-v6 block all/4",
		}, "\n")},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := describe(Wanted(tc.active, tc.luid, tc.app)); got != tc.want {
				t.Fatalf("got\n%s\nwant\n%s", got, tc.want)
			}
		})
	}
}

// Every permit must outweigh every block: WFP picks the heaviest match.
func TestWantedWeights(t *testing.T) {
	for _, f := range Wanted(true, 1, "a") {
		if f.Weight > 15 || (f.Permit && f.Weight <= blockWeight) || (!f.Permit && f.Weight >= permitWeight) {
			t.Fatalf("bad weight %+v", f)
		}
	}
}
