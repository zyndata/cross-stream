// Command xs-hubd is the CrossStream hub daemon. By default it runs the
// Wi-Fi hotspot through NetworkManager, serves DNS and routes hotspot
// clients into the bonded tunnel; -headless uses an existing LAN interface
// instead (Docker test environment).
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net/netip"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/zyndata/cross-stream/internal/hub"
	"github.com/zyndata/cross-stream/internal/hub/hublog"
	"github.com/zyndata/cross-stream/internal/hub/netcfg"
	"github.com/zyndata/cross-stream/internal/hub/uplink"
	"github.com/zyndata/cross-stream/internal/ipc"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if ok, err := runService(run); ok { // Windows service control manager
		if err != nil {
			os.Exit(1)
		}
		return
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:], os.Stderr); err != nil {
		fmt.Fprintln(os.Stderr, "xs-hubd:", err)
		os.Exit(1)
	}
}

// run starts the daemon and blocks until ctx ends; logs go to logOut.
func run(ctx context.Context, args []string, logOut io.Writer) error {
	fs := flag.NewFlagSet("xs-hubd", flag.ContinueOnError)
	headless := fs.Bool("headless", false, "no hotspot: relays and clients are on -lan-iface (test environment)")
	lan := fs.String("lan-iface", "wlan0", "headless: LAN interface for relays and clients")
	wifi := fs.String("wifi-iface", "", "hotspot: Wi-Fi interface for the access point (default: first NetworkManager Wi-Fi device)")
	eth := fs.String("eth-iface", "", "hotspot: also share the tunnel on this Ethernet port (10.42.1.0/24)")
	dnsOn := fs.Bool("dns", false, "headless: also serve DNS on the LAN address (always on with the hotspot)")
	upstreams := fs.String("dns-upstream", "1.1.1.1,9.9.9.9", "DNS servers asked through the tunnel (comma-separated)")
	dir := fs.String("dir", defaultDir, "state directory")
	sock := fs.String("socket", ipc.DefaultSocket, "IPC socket for xs-hubctl and the GUI")
	group := fs.String("socket-group", "crossstream", "group allowed to use the IPC socket (\"\" = root only)")
	tunName := fs.String("tun", "xs0", "tunnel interface name")
	overflow := fs.Uint("overflow-mbps", 0, "use secondary relays only while normal ones send less than this (0 = off)")
	natMode := fs.String("hotspot-nat", "auto", "Windows: NAT hotspot clients in xs-hubd instead of Mobile Hotspot sharing the tunnel: auto (on for Windows 10), on, off")
	captiveURL := fs.String("captive-url", uplink.DefaultProbeURL, "captive portal probe of local uplinks (answers 204; \"off\" = no probes)")
	normalPrio := fs.Bool("normal-priority", false, "Windows: keep the normal process priority (default: above normal)")
	tcpConns := fs.Int("tcp-conns", 0, "test override: TCP connections per tcp:// path (0 = 2, or 4 while the path loses packets)")
	verbose := fs.Bool("v", false, "debug logging")
	showVersion := fs.Bool("version", false, "print version and exit")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *showVersion {
		fmt.Println("xs-hubd", version)
		return nil
	}
	ups, err := parseUpstreams(*upstreams)
	if err != nil {
		return err
	}
	level := slog.LevelInfo
	if *verbose {
		level = slog.LevelDebug
	}
	logs := hublog.NewRing(hublog.DefaultLines) // GUI "Eksportuj logi" / xs-hubctl logs
	logLevel := hublog.NewLevel(level)          // xs-hubctl loglevel / GUI "Detailed logs"
	defer logLevel.Stop()
	log := slog.New(slog.NewTextHandler(io.MultiWriter(logOut, logs), &slog.HandlerOptions{Level: logLevel.Var()}))
	if err := netcfg.SecureDir(*dir); err != nil {
		return err
	}
	cfg := hub.DaemonConfig{Dir: *dir, LanIface: *lan, TunName: *tunName, Socket: *sock, SocketGroup: *group,
		Version: version, NetSetup: true, Log: log, OverflowMbps: uint16(min(*overflow, 0xFFFF)),
		DNS: *dnsOn, DNSUpstreams: ups, Logs: logs, LogLevel: logLevel, CaptiveURL: *captiveURL,
		TCPConns: *tcpConns}
	if !*headless {
		if err := hotspotConfig(ctx, &cfg, *wifi, *eth, *natMode); err != nil {
			return err
		}
	}
	log.Info("xs-hubd starting", "version", version, "lan", cfg.LanIface, "hotspot", !*headless)
	if !*normalPrio {
		if err := raisePriority(); err != nil {
			log.Warn("process priority stays normal", "err", err)
		}
	}
	for {
		d, err := hub.NewDaemon(cfg)
		var cw *netcfg.ClientWifiError
		if errors.As(err, &cw) { // the hotspot card is a Wi-Fi client: ask, then start again
			if err := hub.AskClientWifi(ctx, cfg, cw); err != nil {
				if ctx.Err() != nil { // stopped while asking
					return nil
				}
				return err
			}
			continue
		}
		if err != nil {
			return err
		}
		if err := d.Run(ctx); !errors.Is(err, hub.ErrRestart) {
			return err
		}
		log.Info("xs-hubd restarting with the new enrollment")
	}
}

func parseUpstreams(s string) ([]netip.AddrPort, error) {
	var out []netip.AddrPort
	for _, f := range strings.Split(s, ",") {
		f = strings.TrimSpace(f)
		if f == "" {
			continue
		}
		if ap, err := netip.ParseAddrPort(f); err == nil {
			out = append(out, ap)
			continue
		}
		ip, err := netip.ParseAddr(f)
		if err != nil || !ip.Is4() {
			return nil, fmt.Errorf("-dns-upstream %q: want IPv4 addresses (optionally with :port)", f)
		}
		out = append(out, netip.AddrPortFrom(ip, 53))
	}
	return out, nil
}
