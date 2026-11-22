package netcfg

import (
	"context"
	"encoding/base64"
	"strings"
	"testing"
)

func TestRunPSQuotesVars(t *testing.T) {
	var script string
	run := func(_ context.Context, _ string, name string, args ...string) (string, error) {
		if name != "powershell.exe" || args[len(args)-2] != "-EncodedCommand" {
			t.Fatalf("command %s %v", name, args)
		}
		b, err := base64.StdEncoding.DecodeString(args[len(args)-1])
		if err != nil {
			t.Fatal(err)
		}
		r := make([]rune, len(b)/2)
		for i := range r {
			r[i] = rune(b[2*i]) | rune(b[2*i+1])<<8
		}
		script = string(r)
		return "", nil
	}
	if _, err := runPS(context.Background(), run, "hotspot.ps1", map[string]string{"Ssid": "it's", "Op": "up"}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(script, "$Ssid = 'it''s'\n") || !strings.Contains(script, "$Op = 'up'\n") {
		t.Fatalf("variables not set safely:\n%s", script[:200])
	}
}

func TestPSError(t *testing.T) {
	out := "Mobile Hotspot did not start: WiFiDeviceOff\r\nAt line:70 char:5\r\n+     throw \"x\"\r\n+     ~~~\r\n    + CategoryInfo : OperationStopped\r\n"
	if got := psError(out); !strings.HasPrefix(got, "Mobile Hotspot did not start: WiFiDeviceOff") || strings.Contains(got, "At line") {
		t.Fatalf("psError = %q", got)
	}
}

func TestHotspotScriptRuns(t *testing.T) {
	// Parse check of the embedded script: Op=parse stops right after the
	// WinRT types load, so nothing is started.
	if _, err := runPS(context.Background(), nil, "hotspot.ps1", map[string]string{"Op": "parse"}); err != nil {
		t.Fatal(err)
	}
}

func TestPSOutputUTF8(t *testing.T) {
	got, err := psExec(context.Background(), "", "powershell.exe", "-NoProfile", "-NonInteractive", "-EncodedCommand",
		encodePS("[Console]::OutputEncoding = [Text.Encoding]::UTF8\n'Połączenie'"))
	if err != nil || strings.TrimSpace(got) != "Połączenie" {
		t.Fatalf("got %q %v", got, err)
	}
}
