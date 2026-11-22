package main

import (
	"context"
	"log/slog"
	"os"
	"strings"
	"time"

	"github.com/zyndata/cross-stream/internal/server"
)

// restartDelay lets the panel's reply leave before the server goes down.
const restartDelay = 500 * time.Millisecond

// tunnelPort picks where the tunnel port is configured and how the server
// restarts. On Home Assistant it reads the port from the app's Network
// section into cfg.PublicPort (the source of truth there) and restarts
// through the Supervisor; elsewhere stop ends Run, and Docker's restart
// policy starts the container again.
func tunnelPort(ctx context.Context, cfg *server.Config, cfgPath string, stop func(), log *slog.Logger) (server.PortSource, func(context.Context) error) {
	later := func(f func()) func(context.Context) error {
		return func(context.Context) error {
			time.AfterFunc(restartDelay, f)
			return nil
		}
	}
	if cfg.PortSource == "supervisor" {
		sup := server.SupervisorPort{URL: server.SupervisorURL, Token: os.Getenv("SUPERVISOR_TOKEN"), Main: cfg.ListenPorts[0]}
		if p, err := sup.Configured(ctx); err != nil {
			log.Warn("tunnel port from Home Assistant unavailable, using the listen port", "port", cfg.ListenPorts[0], "err", err)
		} else {
			cfg.PublicPort = p
		}
		return sup, later(func() {
			if err := sup.Restart(context.Background()); err != nil {
				log.Error("restart through Home Assistant failed", "err", err)
			}
		})
	}
	restart := later(func() {
		log.Info("restarting: exiting so the container restart policy starts the server again")
		stop()
	})
	switch {
	case strings.TrimSpace(os.Getenv(server.EnvPrefix+"PUBLIC_PORT")) != "":
		return server.FixedPort{From: "env", Port: cfg.PublicPort}, restart
	case cfgPath == "":
		return server.FixedPort{From: "default", Port: cfg.PublicPort}, restart
	default:
		return server.FilePort{Path: cfgPath}, restart
	}
}
