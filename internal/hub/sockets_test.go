package hub

import (
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// socketCtor matches code that opens sockets (or resolves names) on its own.
var socketCtor = regexp.MustCompile(`net\.Dialer\{|net\.ListenConfig\{|net\.Listen\(|net\.ListenUDP\(|net\.ListenTCP\(|` +
	`net\.ListenPacket\(|net\.Dial\(|net\.DialTimeout\(|net\.DialUDP\(|net\.DialTCP\(|http\.Client\{|http\.Get\(|` +
	`http\.DefaultClient|DefaultResolver|net\.Resolver\{`)

// markedSockets lists every socket constructor in xs-hubd's code and why
// its sockets carry the hub mark (phase 21: the own-traffic rule sends
// unmarked laptop traffic into the tunnel). A new constructor fails this
// test until it is marked and listed here.
var markedSockets = map[string]string{
	"netcfg/mark_linux.go: net.Dialer{":          "netcfg.Dialer: Control(nil) marks",
	"netcfg/mark_linux.go: net.ListenConfig{":    "netcfg.ListenConfig: Control(nil) marks",
	"netcfg/mark_other.go: net.Dialer{":          "no marks outside Linux",
	"netcfg/mark_other.go: net.ListenConfig{":    "no marks outside Linux",
	"netcfg/dial_linux.go: net.Dialer{":          "DialVia: BindDevice marks",
	"netcfg/dial_windows.go: net.Dialer{":        "Windows: no marks",
	"localpath.go: net.Dialer{":                  "local path DIRECT dial: lp.control() = UplinkControl = BindDevice (marks)",
	"uplink/captive.go: net.Dialer{":             "captive probe: control = UplinkControl (marks)",
	"uplink/captive.go: http.Client{":            "captive probe: its transport dials with the dialer above",
	"captive.go: DefaultResolver":                "portal name via the system resolver (decisions.md phase 21: daemon lookups)",
	"system.go: DefaultResolver":                 "loop guard server lookup via the system resolver (decisions.md phase 21)",
	"../dns/server.go: net.ListenConfig{":        "hub DNS listeners: Config.Control = netcfg.Control",
	"../direct/stack.go: net.Dialer{":            "default only; the hub sets Config.Dial = netcfg.Dialer",
	"../transport/transport.go: net.Dialer{":     "Dialer.Control from the caller (local paths: BindDevice)",
	"../transport/transport.go: DefaultResolver": "default lookup = system resolver (decisions.md phase 21)",
	"../transport/wss.go: http.Client{":          "WSS: dials with the transport dialer above",
}

func TestEverySocketIsMarked(t *testing.T) {
	seen := map[string]bool{}
	for _, root := range []string{".", "../dns", "../direct", "../transport", "../../cmd/xs-hubd"} {
		err := filepath.WalkDir(root, func(path string, e fs.DirEntry, err error) error {
			if err != nil || e.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return err
			}
			b, err := os.ReadFile(path)
			if err != nil {
				return err
			}
			for _, m := range socketCtor.FindAllString(string(b), -1) {
				key := filepath.ToSlash(path) + ": " + m
				if _, ok := markedSockets[key]; !ok {
					t.Errorf("%s opens sockets without the hub mark check: mark them (netcfg.Control/Dialer/ListenConfig) and list them in markedSockets", key)
				}
				seen[key] = true
			}
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	for k := range markedSockets {
		if !seen[k] {
			t.Errorf("markedSockets lists %q, which no longer exists", k)
		}
	}
}
