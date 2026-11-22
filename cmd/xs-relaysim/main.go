// Command xs-relaysim is the CrossStream relay core as a Linux binary for
// tests: the same internal/relay code as the Android app, with sockets bound
// to interfaces by name (SO_BINDTODEVICE, run as root).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/pairing"
	"github.com/zyndata/cross-stream/internal/relay"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

// Exit codes.
const (
	exitError     = 1
	exitRejected  = 2
	exitForgotten = 3
	exitStopped   = 4
)

func main() {
	code, err := run(os.Args[1:])
	if err != nil {
		fmt.Fprintln(os.Stderr, "xs-relaysim:", err)
	}
	os.Exit(code)
}

func run(args []string) (int, error) {
	fs := flag.NewFlagSet("xs-relaysim", flag.ContinueOnError)
	qr := fs.String("qr", "", "XSR1 pairing code from xs-hubctl pair-qr (required)")
	wifi := fs.String("wifi-iface", "", "interface towards the hub (Wi-Fi)")
	cell := fs.String("cell-iface", "", "interface towards the server (cellular)")
	name := fs.String("name", "relaysim", "relay name shown on the hub")
	carrier := fs.String("carrier", "", "carrier name")
	dir := fs.String("dir", "/var/lib/xs-relay", "state directory (relay key, paired hub)")
	capMB := fs.Int64("cap-mb", 0, "data cap in MB (0 = none)")
	period := fs.String("cap-period", "", "cap period: session, day or month")
	overCap := fs.String("over-cap", "", "over the cap: stop or throttle")
	throttle := fs.Int("throttle-kbps", 0, "throttle rate over the cap")
	maxPrio := fs.String("max-priority", "", "how the hub may use this relay: normal, secondary or backup")
	tcpConns := fs.Int("tcp-conns", 0, "test override: TCP connections per tcp:// server endpoint (0 = the hub decides)")
	verbose := fs.Bool("v", false, "debug logging")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return exitError, err
	}
	if *showVersion {
		fmt.Println("xs-relaysim", version)
		return 0, nil
	}
	if *qr == "" {
		return exitError, errors.New("-qr is required")
	}
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	p, err := pairing.Decode(*qr)
	if err != nil {
		return exitError, err
	}
	key, err := relay.LoadOrCreateKey(*dir)
	if err != nil {
		return exitError, err
	}
	r, err := relay.New(relay.Config{
		Key: key, Hub: p, Name: *name, Carrier: *carrier, Paired: relay.IsPaired(*dir, p.HPK),
		Limits: control.Limits{MaxPriority: *maxPrio, CapMB: *capMB, CapPeriod: *period, OverCap: *overCap, ThrottleKbps: *throttle},
		Binder: relay.DeviceBinder{WiFi: *wifi, Cellular: *cell}, TCPConns: *tcpConns,
		OnPaired: func(w control.Welcome) {
			if err := relay.SavePaired(*dir, relay.PairedHub{HPK: p.HPK, RelayID: w.RelayID}); err != nil {
				log.Warn("save paired hub", "err", err)
			}
		},
		Log: log,
	})
	if err != nil {
		return exitError, err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	log.Info("xs-relaysim starting", "version", version, "hub", p.Hub, "wifi", *wifi, "cell", *cell)
	err = r.Run(ctx)
	var rej *relay.RejectedError
	var stopped *relay.StoppedError
	switch {
	case errors.Is(err, context.Canceled):
		return 0, nil
	case errors.Is(err, relay.ErrForgotten):
		_ = relay.ForgetPaired(*dir)
		return exitForgotten, err
	case errors.As(err, &rej):
		return exitRejected, err
	case errors.As(err, &stopped):
		return exitStopped, err
	}
	return exitError, err
}
