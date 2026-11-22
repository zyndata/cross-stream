package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"

	"github.com/zyndata/cross-stream/internal/server/admin"
)

// sourceNames says where the tunnel port is configured, for people.
var sourceNames = map[string]string{
	"ha":      "Home Assistant app, Network section",
	"file":    "server.yaml (public_port)",
	"env":     "environment variable XS_SERVER_PUBLIC_PORT (read-only here)",
	"default": "default, no config file (read-only here)",
}

// tunnelPortCmd shows the tunnel port, or with a port argument changes it
// where it is configured (applied at the next restart).
func tunnelPortCmd(ctx context.Context, c admin.Client, stdout io.Writer, args []string) error {
	in, err := c.Info(ctx)
	if err != nil {
		return err
	}
	if len(args) == 0 {
		printTunnelPort(stdout, in)
		return nil
	}
	if len(args) != 1 {
		return errors.New("usage: xs-ctl tunnel-port [port]")
	}
	port, err := strconv.Atoi(args[0])
	if err != nil {
		return fmt.Errorf("tunnel port %q: not a number", args[0])
	}
	if err := c.SetTunnelPort(ctx, port); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(stdout, "tunnel port set to %d in: %s\n", port, sourceNames[in.TunnelPortSource])
	_, _ = fmt.Fprintf(stdout, "next: forward %d (UDP and TCP) on the router to this server, then xs-ctl restart.\n", port)
	if n := hubCount(ctx, c); n > 0 {
		_, _ = fmt.Fprintf(stdout, "warning: the %d enrolled hub(s) keep the old port: after the restart revoke and enroll each again.\n", n)
	}
	return nil
}

func printTunnelPort(w io.Writer, in admin.ServerInfo) {
	_, _ = fmt.Fprintf(w, "tunnel port %d (configured: %d, in: %s)\n", in.TunnelPort, in.TunnelPortConfigured, sourceNames[in.TunnelPortSource])
	if in.TunnelPortErr != "" {
		_, _ = fmt.Fprintln(w, "error reading the configured port:", in.TunnelPortErr)
	}
	if in.RestartRequired {
		_, _ = fmt.Fprintln(w, "restart required: xs-ctl restart")
	}
	if len(in.Reenroll) > 0 {
		_, _ = fmt.Fprintf(w, "enroll again (other tunnel port): %s\n", strings.Join(in.Reenroll, ", "))
	}
}

func hubCount(ctx context.Context, c admin.Client) int {
	hubs, err := c.Hubs(ctx)
	if err != nil {
		return 0
	}
	return len(hubs)
}

// restartCmd asks the server to restart (Home Assistant: through the
// Supervisor; Docker: the server exits and the restart policy starts it).
func restartCmd(ctx context.Context, c admin.Client, stdout io.Writer) error {
	if err := c.Restart(ctx); err != nil {
		return err
	}
	_, _ = fmt.Fprintln(stdout, "restarting")
	return nil
}
