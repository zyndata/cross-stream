// Command xs-server is the CrossStream server: it accepts enrolled hubs over
// UDP and forwards their traffic to the internet with NAT.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"io/fs"
	"log/slog"
	"net"
	"net/netip"
	"os"
	"os/signal"
	"path/filepath"
	"runtime"
	"syscall"
	"time"

	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/hub/hublog"
	"github.com/zyndata/cross-stream/internal/server"
	"github.com/zyndata/cross-stream/internal/tun"
	"github.com/zyndata/cross-stream/internal/webui"
	webassets "github.com/zyndata/cross-stream/webui"
)

// logRingLines is how many recent log lines the web panel can show from.
const logRingLines = 2000

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "xs-server:", err)
		os.Exit(1)
	}
}

func run() error {
	cfgPath := flag.String("config", "/data/server.yaml", "config file (missing = defaults; XS_SERVER_* env overrides)")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()
	if *showVersion {
		fmt.Println("xs-server", version)
		return nil
	}
	cfg, err := server.LoadConfig(*cfgPath, os.Getenv)
	if err != nil {
		return err
	}
	var level slog.Level
	if err := level.UnmarshalText([]byte(cfg.LogLevel)); err != nil {
		return fmt.Errorf("log level: %w", err)
	}
	ring := hublog.NewRing(logRingLines) // web panel "Logs" (redacted on read)
	log := slog.New(slog.NewTextHandler(io.MultiWriter(os.Stderr, ring), &slog.HandlerOptions{Level: level}))
	log.Info("xs-server starting", "version", version)
	if runtime.GOOS == "linux" {
		server.ApplySysctls("/proc/sys", server.HardeningSysctls, log)
	}

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return fmt.Errorf("data dir: %w", err)
	}
	key, err := loadOrCreateKey(filepath.Join(cfg.DataDir, "server.key"), log)
	if err != nil {
		return err
	}
	state, err := server.OpenState(filepath.Join(cfg.DataDir, "state.json"), nil)
	if err != nil {
		return err
	}
	dev, err := tun.Create(tun.Config{Name: cfg.TunName, MTU: cfg.MTU,
		Prefix: netip.PrefixFrom(server.ServerIP, server.TunnelPrefix.Bits())})
	if err != nil {
		return err
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ports, restart := tunnelPort(ctx, &cfg, *cfgPath, stop, log)
	srv, err := server.New(server.Options{
		Config: cfg, Key: key, State: state, TUN: dev, Log: log,
		Firewall: server.NFT{TunName: dev.Name(), Prefix: server.TunnelPrefix, Egress: cfg.Egress()},
		Detect:   server.DetectOutbound,
		Version:  version,
		Logs:     ring.Redacted,
		Ports:    ports,
		Restart:  restart,
	})
	if err != nil {
		_ = dev.Close()
		return err
	}
	if cfg.WebEnabled() {
		// The panel is optional: a busy port or an unwritable cert must not
		// take the tunnel down with it.
		if err := startPanel(ctx, cfg, srv, log); err != nil {
			log.Error("web panel disabled", "err", err)
		}
	}
	if cfg.IngressEnabled() {
		if err := startIngress(ctx, cfg, srv, log); err != nil {
			log.Error("ingress panel disabled", "err", err)
		}
	}
	if err := srv.Listen(ctx); err != nil {
		return err
	}
	return srv.Run(ctx)
}

// startPanel opens the web panel's HTTPS listener and serves it until ctx
// is done.
func startPanel(ctx context.Context, cfg server.Config, srv *server.Server, log *slog.Logger) error {
	cert, created, err := webui.LoadCert(cfg.WebCert, cfg.WebKey, cfg.DataDir, time.Now())
	if err != nil {
		return err
	}
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.WebListen)
	if err != nil {
		return fmt.Errorf("web panel listen: %w", err)
	}
	wlog := log.With("component", "web")
	wlog.Info("web panel listening", "addr", ln.Addr().String(), "cert_sha256", webui.Fingerprint(cert),
		"new_cert", created, "public", cfg.WebAllowPublic)
	panel := webui.New(webui.Options{Admin: srv.Admin(), Assets: webassets.Assets(),
		Deny: []netip.Prefix{server.TunnelPrefix}, Cert: cert, Log: wlog})
	go func() {
		if err := panel.Serve(ctx, ln); err != nil {
			wlog.Error("web panel stopped", "err", err)
		}
	}()
	return nil
}

// startIngress opens the plain-HTTP panel listener for Home Assistant
// ingress and serves it until ctx is done.
func startIngress(ctx context.Context, cfg server.Config, srv *server.Server, log *slog.Logger) error {
	ln, err := (&net.ListenConfig{}).Listen(ctx, "tcp", cfg.WebIngressListen)
	if err != nil {
		return fmt.Errorf("ingress panel listen: %w", err)
	}
	wlog := log.With("component", "web-ingress")
	wlog.Info("ingress panel listening", "addr", ln.Addr().String(), "allow", cfg.WebIngressAllow)
	panel := webui.New(webui.Options{Admin: srv.Admin(), Assets: webassets.Assets(),
		Deny: []netip.Prefix{server.TunnelPrefix}, Log: wlog, Ingress: true, IngressAllow: cfg.IngressAllow()})
	go func() {
		if err := panel.ServeIngress(ctx, ln); err != nil {
			wlog.Error("ingress panel stopped", "err", err)
		}
	}()
	return nil
}

// loadOrCreateKey loads the server static key, generating it on first start.
func loadOrCreateKey(path string, log *slog.Logger) (crypto.Keypair, error) {
	kp, err := crypto.LoadKeypair(path)
	if err == nil {
		return kp, nil
	}
	if !errors.Is(err, fs.ErrNotExist) {
		return crypto.Keypair{}, fmt.Errorf("server key: %w", err)
	}
	if kp, err = crypto.GenerateKeypair(); err != nil {
		return crypto.Keypair{}, fmt.Errorf("server key: %w", err)
	}
	if err := crypto.SaveKeypair(path, kp); err != nil {
		return crypto.Keypair{}, fmt.Errorf("server key: %w", err)
	}
	log.Info("generated server key", "key", kp)
	return kp, nil
}
