// Command xs-hubctl is the hub CLI. Every command talks to a running xs-hubd
// over its IPC socket. Without a running daemon, enroll and connect use the
// phase 02 server test mode: straight to the server, no relays.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"os/signal"
	"syscall"

	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/hub"
	"github.com/zyndata/cross-stream/internal/ipc"
	"github.com/zyndata/cross-stream/internal/tun"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: xs-hubctl [-dir path] [-socket path] <command>
  status                             hub mode, tunnel, hotspot clients and relays (JSON)
  relays                             paired relays and their state (JSON)
  events                             status as one JSON line per second (Ctrl-C stops)
  paths [-every 1s] [-n count]       bonded paths' metrics as CSV, one line per path per sample:
                                     rates, RTTs, in-flight cap and bound, app-limited, loss and
                                     drop counters (upload diagnostics; Ctrl-C stops)
  hotspot [-ssid s] [-psk p] [-band a|bg] [-channel n] [-country CC]
                                     show the hotspot, or change it (restarts the AP)
  pair-qr [-no-qr]                   print the XSR1 relay pairing code (QR on a terminal)
  forget <relay id or name>          remove a relay; it is rejected from now on
  path <relay> [-priority p] [-rate-kbps n] [-cap-mb n] [-up m] [-down m]
                                     set a relay's path: priority normal|secondary|backup,
                                     rate limit (0 = none), cap (0 = none), direction
                                     modes on|backup_only|off
  settings [-redundancy off|auto|on] [-mode max_throughput|min_latency] [-aggregation=true|false]
           [-overflow-mbps n] [-pep off|on] [-pep-exclude list] [-own-traffic on|off]
                                     show the hub-wide settings, or change them (own traffic:
                                     this laptop's apps through CrossStream; open connections
                                     restart once)
  speedtest [-path relay|-all] [-dir down|up|both] [-secs 10] [-max-mb 20] [-results]
                                     measure raw relay capacity (uses the relays' mobile data:
                                     up to max-mb per relay and direction), or list results
  bypass [set <domain|cidr>... | clear]
                                     show the bypass list (out through a relay's carrier IP,
                                     not the server), replace it, or empty it
  forwards [set proto/port=target... | clear]
                                     show the port forwards (server public port -> the same
                                     port on a client, target = client MAC or IPv4), replace
                                     them, or remove them
  uplinks [set <id|name> key=value... | forget <id|name> | captive <id|name> [-open]]
                                     local links of the hub (USB tethering, Ethernet, modem,
                                     Wi-Fi client): list, change (enabled, name, ttl, priority,
                                     rate_kbps, up, down, cap_mb, period, reset_day, over_cap,
                                     throttle_kbps, reset=1) or forget; captive checks for a
                                     login page (hotel, train) now, -open routes the portal
                                     through the link for 10 min and opens it in the browser
  usage [-relay r] [-from day] [-to day]
                                     usage history: bytes per relay per day, failovers survived,
                                     speed tests (days YYYY-MM-DD)
  enroll [-via iface] <code>         enroll with an XS1 code from xs-ctl enroll: when xs-hubd
                                     runs, through a phone or local link (none: this computer's
                                     own connection), else straight to the server
  connect -via iface [-tun xs0]      bring up the tunnel straight to the server (server test mode)
  probe [-via iface] [-to host:port,...] [-code XS1...]
                                     one handshake with the identity in -dir, then CLOSE: the
                                     server deploy health check (-code enrolls -dir first)
  logs [-o file]                     recent xs-hubd log, redacted (safe to send for help)
  loglevel [debug|info] [-for 15m]   show or change xs-hubd's log level without a restart;
                                     -for goes back to the start level (-v) after that time
  key [migrate-tpm]                  where the hub key is kept; seal hub.key with the TPM
                                     (/dev/tpmrm0, run as root; the hub identity stays the same)
  update [-install]                  look for a newer release (public GitHub repository); -install
                                     downloads, verifies (SHA256SUMS) and installs it, then restarts
                                     xs-hubd if it ran (root / Administrator). Never checks on its own
  version                            print version`

func main() {
	if err := run(os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, "xs-hubctl:", err)
		os.Exit(1)
	}
}

func run(args []string) error {
	fs := flag.NewFlagSet("xs-hubctl", flag.ContinueOnError)
	dir := fs.String("dir", defaultDir, "hub state directory (key and enrollment)")
	sock := fs.String("socket", ipc.DefaultSocket, "xs-hubd IPC socket")
	verbose := fs.Bool("v", false, "debug logging")
	fs.Usage = func() { fmt.Fprintln(os.Stderr, usage) }
	if err := fs.Parse(args); err != nil {
		return err
	}
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: level}))
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	switch fs.Arg(0) {
	case "status":
		return printCmd(ctx, *sock, hub.MethodStatus)
	case "relays":
		return printCmd(ctx, *sock, hub.MethodRelaysList)
	case "events":
		return eventsCmd(ctx, *sock)
	case "hotspot":
		return hotspotCmd(ctx, *sock, fs.Args()[1:])
	case "pair-qr":
		return pairQRCmd(ctx, *sock, fs.Args()[1:])
	case "forget":
		return forgetCmd(ctx, *sock, fs.Args()[1:])
	case "paths":
		return pathsCmd(ctx, *sock, fs.Args()[1:])
	case "path":
		return pathCmd(ctx, *sock, fs.Args()[1:])
	case "settings":
		return settingsCmd(ctx, *sock, fs.Args()[1:])
	case "speedtest":
		return speedtestCmd(ctx, *sock, fs.Args()[1:])
	case "forwards":
		return forwardsCmd(ctx, *sock, fs.Args()[1:])
	case "uplinks":
		return uplinksCmd(ctx, *sock, fs.Args()[1:])
	case "usage":
		return usageCmd(ctx, *sock, fs.Args()[1:])
	case "bypass":
		return bypassCmd(ctx, *sock, fs.Args()[1:])
	case "enroll":
		return enrollCmd(ctx, *dir, *sock, fs.Args()[1:], log)
	case "connect":
		return connectCmd(ctx, *dir, fs.Args()[1:], log)
	case "probe":
		return probeCmd(ctx, *dir, fs.Args()[1:], log)
	case "logs":
		return logsCmd(ctx, *sock, fs.Args()[1:])
	case "loglevel":
		return loglevelCmd(ctx, *sock, fs.Args()[1:])
	case "key":
		return keyCmd(*dir, fs.Args()[1:])
	case "update":
		return updateCmd(ctx, fs.Args()[1:])
	case "version":
		fmt.Println("xs-hubctl", version)
		return nil
	default:
		return errors.New(usage)
	}
}

func enrollCmd(ctx context.Context, dir, sock string, args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("enroll", flag.ContinueOnError)
	via := fs.String("via", "", "send from this interface's IPv4 address (the route still follows the "+
		"routing table; default: routing table)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 1 {
		return errors.New(usage)
	}
	if *via == "" && daemonRunning(sock) {
		return enrollViaDaemon(ctx, sock, fs.Arg(0))
	}
	conn, err := hub.ListenUDP(*via)
	if err != nil {
		return err
	}
	e, err := hub.EnrollWithCode(ctx, dir, fs.Arg(0), conn, version, log)
	if errors.Is(err, hub.ErrNoResponse) {
		// same hint as the daemon gives without a phone (field test 2026-11-25)
		return fmt.Errorf("enroll: %w", hub.ErrNoAnswerHere)
	}
	if err != nil {
		return err
	}
	fmt.Printf("enrolled as %s, tunnel ip %s\n", e.Name, e.TunnelIP)
	return nil
}

func connectCmd(ctx context.Context, dir string, args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("connect", flag.ContinueOnError)
	via := fs.String("via", "", "local interface to reach the server through (required)")
	tunName := fs.String("tun", "xs0", "TUN interface name")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *via == "" || fs.NArg() != 0 {
		return errors.New(usage)
	}
	e, err := hub.LoadEnrollment(dir)
	if err != nil {
		return err
	}
	key, err := hub.LoadOrCreateKey(dir)
	if err != nil {
		return err
	}
	serverKey, err := crypto.ParseKey(e.ServerKey)
	if err != nil {
		return fmt.Errorf("enrollment server key: %w", err)
	}
	eps, err := hub.ResolveEndpoints(e.Endpoints)
	if err != nil {
		return err
	}
	ip, err := netip.ParseAddr(e.TunnelIP)
	if err != nil {
		return fmt.Errorf("enrollment tunnel ip: %w", err)
	}
	conn, err := hub.ListenUDP(*via)
	if err != nil {
		return err
	}
	dev, err := tun.Create(tun.Config{Name: *tunName, MTU: e.MTU, Prefix: netip.PrefixFrom(ip, 32)})
	if err != nil {
		_ = conn.Close()
		return err
	}
	log.Info("tunnel interface up", "tun", dev.Name(), "ip", ip, "via", *via)
	c := &hub.Client{Key: key, ServerKey: serverKey, Endpoints: eps, Conn: conn, Name: e.Name,
		Version: version, TUN: dev, Log: log}
	err = c.Run(ctx)
	if errors.Is(err, context.Canceled) {
		return nil
	}
	return err
}
