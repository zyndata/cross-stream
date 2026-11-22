package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/zyndata/cross-stream/internal/hub"
	"github.com/zyndata/cross-stream/internal/ipc"
)

// settingsCmd shows or changes the hub-wide bonding settings.
func settingsCmd(ctx context.Context, sock string, args []string) error {
	fs := flag.NewFlagSet("settings", flag.ContinueOnError)
	red := fs.String("redundancy", "", "off, auto or on")
	mode := fs.String("mode", "", "max_throughput or min_latency")
	agg := fs.Bool("aggregation", true, "batch small packets")
	overflow := fs.Uint("overflow-mbps", 0, "secondary relays unused below this normal-relay rate (0 = off)")
	pep := fs.String("pep", "", "off or on: split new TCP connections (PEP)")
	pepEx := fs.String("pep-exclude", "", `comma-separated ports, ranges and IPv4 networks kept out of PEP ("" = none, e.g. 22,5000-5100,10.0.0.0/8)`)
	if err := fs.Parse(args); err != nil {
		return err
	}
	var p hub.SettingsParams
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "redundancy":
			p.Redundancy = red
		case "mode":
			p.Mode = mode
		case "aggregation":
			p.Aggregation = agg
		case "overflow-mbps":
			v := uint16(min(*overflow, 0xFFFF))
			p.OverflowMbps = &v
		case "pep":
			p.PEP = pep
		case "pep-exclude":
			list := []string{}
			for _, s := range strings.Split(*pepEx, ",") {
				if s = strings.TrimSpace(s); s != "" {
					list = append(list, s)
				}
			}
			p.PEPExclude = &list
		}
	})
	var res hub.Settings
	method, params := hub.MethodSettingsGet, any(nil)
	if fs.NFlag() > 0 {
		method, params = hub.MethodSettingsSet, p
	}
	if err := ipc.Call(ctx, sock, method, params, &res); err != nil {
		return err
	}
	return printJSON(res)
}

// speedtestCmd runs a speed test (or lists stored results).
func speedtestCmd(ctx context.Context, sock string, args []string) error {
	fs := flag.NewFlagSet("speedtest", flag.ContinueOnError)
	path := fs.String("path", "", "relay id or name (default: all relays at once)")
	all := fs.Bool("all", false, "all relays at once (default)")
	dir := fs.String("dir", "both", "down, up or both")
	secs := fs.Int("secs", 10, "seconds per direction (max 30)")
	maxMB := fs.Uint("max-mb", 20, "filler cap per relay and direction, MB (it uses the relays' mobile data)")
	results := fs.Bool("results", false, "print the stored results instead of running a test")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *results {
		return printCmd(ctx, sock, hub.MethodSpeedTestList)
	}
	if *all && *path != "" {
		return errors.New("speedtest: -path and -all exclude each other")
	}
	p := hub.SpeedTestParams{Relay: *path, Dir: *dir, Secs: *secs, MaxMB: uint16(min(*maxMB, 0xFFFF))}
	fmt.Fprintf(os.Stderr, "speed test: up to %d MB per relay and direction of the relays' mobile data\n", p.MaxMB)
	ctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
	defer cancel()
	var res hub.SpeedResult
	if err := ipc.Call(ctx, sock, hub.MethodSpeedTestRun, p, &res); err != nil {
		return err
	}
	return printJSON(res)
}
