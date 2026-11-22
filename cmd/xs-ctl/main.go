// Command xs-ctl is the CrossStream server CLI (enroll, list, revoke, rename,
// sessions, port forwards, web panel admins, audit log).
// It talks to xs-server over its Unix control socket.
package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strconv"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/zyndata/cross-stream/internal/server/admin"
)

// version is set at build time with -ldflags "-X main.version=...".
var version = "dev"

const usage = `usage: xs-ctl [-socket path] <command>
  enroll <name>   create a single-use enrollment code (printed on stdout, expiry on stderr)
  list            enrolled hubs
  revoke <name>   delete a hub's key and close its session
  rename <name> <new-name>
                  rename a hub (an online hub reconnects under the new name)
  sessions [-json] [-watch [-interval 1s]]
                  active sessions and per-path bonding metrics (-watch: refresh until Ctrl-C)
  forward allow <name> <n>
                  let a hub forward up to n public ports (0-20, 0 = none)
  forward list    port forward allowance and active forwards per hub
  info            server ports, endpoints, version
  tunnel-port [port]
                  show the port hubs dial, or change it where it is configured
                  (HA Network section or server.yaml); applied at the next restart.
                  Enrolled hubs keep the old port: enroll them again afterwards
  restart         restart the server (Home Assistant, or Docker with a restart policy)
  audit [-n 50]   newest admin actions (CLI and web panel)
  admin add|passwd|del|list|totp ...
                  web panel admins ("xs-ctl admin" for details)
  version         print version`

func main() {
	if err := run(os.Args[1:], os.Stdin, os.Stdout, os.Stderr); err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "xs-ctl:", err)
		os.Exit(1)
	}
}

func run(args []string, stdin io.Reader, stdout, stderr io.Writer) error {
	fs := flag.NewFlagSet("xs-ctl", flag.ContinueOnError)
	fs.SetOutput(stderr)
	sock := fs.String("socket", envOr("XS_SERVER_CONTROL_SOCKET", "/run/xs-server.sock"), "xs-server control socket")
	fs.Usage = func() { _, _ = fmt.Fprintln(stderr, usage) }
	if err := fs.Parse(args); err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	c := admin.Client{Socket: *sock}
	cmd, rest := fs.Arg(0), fs.Args()
	if len(rest) > 0 {
		rest = rest[1:]
	}
	if (cmd == "enroll" || cmd == "revoke" || cmd == "rename") && slices.ContainsFunc(rest, isHelp) {
		_, _ = fmt.Fprintln(stdout, usage) // not a hub named "-h"
		return nil
	}
	needName := func() (string, error) {
		if len(rest) != 1 || rest[0] == "" {
			return "", fmt.Errorf("%s needs exactly one hub name\n%s", cmd, usage)
		}
		return rest[0], nil
	}
	switch cmd {
	case "enroll":
		name, err := needName()
		if err != nil {
			return err
		}
		r, err := c.Enroll(ctx, name)
		if err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, r.Code)
		_, _ = fmt.Fprintln(stderr, "expires", r.Expires.Format(time.RFC3339))
	case "list":
		hubs, err := c.Hubs(ctx)
		if err != nil {
			return err
		}
		printHubs(stdout, hubs)
	case "revoke":
		name, err := needName()
		if err != nil {
			return err
		}
		if err := c.Revoke(ctx, name); err != nil {
			return err
		}
		_, _ = fmt.Fprintln(stdout, "revoked", name)
	case "sessions":
		return sessionsCmd(c, stdout, stderr, rest)
	case "forward":
		return forwardCmd(ctx, c, stdout, fs.Args()[1:])
	case "rename":
		return renameCmd(ctx, c, stdout, rest)
	case "info":
		return infoCmd(ctx, c, stdout)
	case "tunnel-port":
		return tunnelPortCmd(ctx, c, stdout, rest)
	case "restart":
		return restartCmd(ctx, c, stdout)
	case "audit":
		return auditCmd(ctx, c, stdout, stderr, rest)
	case "admin":
		return adminCmd(ctx, c, stdin, stdout, stderr, rest)
	case "version":
		_, _ = fmt.Fprintln(stdout, "xs-ctl", version)
	default:
		return errors.New(usage)
	}
	return nil
}

func isHelp(a string) bool { return a == "-h" || a == "-help" || a == "--help" }

func envOr(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

func printHubs(w io.Writer, hubs []admin.Hub) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "NAME\tTUNNEL IP\tONLINE\tENROLLED\tPORT")
	stale := 0
	for _, h := range hubs {
		port := strconv.Itoa(h.Port)
		if h.Reenroll {
			port += " (enroll again)"
			stale++
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%v\t%s\t%s\n", h.Name, h.TunnelIP, h.Online, h.Enrolled.Format(time.RFC3339), port)
	}
	_ = tw.Flush()
	if stale > 0 {
		_, _ = fmt.Fprintf(w, "warning: %d hub(s) were enrolled with another tunnel port: revoke them and enroll them again with a new code\n", stale)
	}
}

// forwardCmd runs "forward allow <name> <n>" and "forward list".
func forwardCmd(ctx context.Context, c admin.Client, stdout io.Writer, args []string) error {
	switch {
	case len(args) == 3 && args[0] == "allow":
		n, err := strconv.Atoi(args[2])
		if err != nil {
			return fmt.Errorf("forward allow: %q is not a number", args[2])
		}
		if err := c.ForwardAllow(ctx, args[1], n); err != nil {
			return err
		}
		_, _ = fmt.Fprintf(stdout, "%s may forward %d ports\n", args[1], n)
		return nil
	case len(args) == 1 && args[0] == "list":
		hubs, err := c.Hubs(ctx)
		if err != nil {
			return err
		}
		printForwards(stdout, hubs)
		return nil
	}
	return errors.New(usage)
}

func printForwards(w io.Writer, hubs []admin.Hub) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "HUB\tALLOWED\tACTIVE")
	for _, h := range hubs {
		ports := make([]string, 0, len(h.Forwards))
		for _, f := range h.Forwards {
			ports = append(ports, fmt.Sprintf("%s/%d", f.Proto, f.Port))
		}
		active := strings.Join(ports, ", ")
		if active == "" {
			active = "-"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\n", h.Name, h.FwdAllow, active)
	}
	_ = tw.Flush()
}

func printSessions(w io.Writer, ss []admin.Session, now time.Time) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "HUB\tTUNNEL IP\tPATHS\tRX\tTX\tRATE RX/TX\tQUEUE/DROP/SKIP\tLAST SEEN")
	for _, s := range ss {
		paths := make([]string, 0, len(s.Paths))
		for _, p := range s.Paths {
			paths = append(paths, fmt.Sprintf("%d=%s", p.ID, p.Addr))
		}
		seen := "never"
		if !s.LastSeen.IsZero() {
			seen = now.Sub(s.LastSeen).Round(time.Second).String() + " ago"
		}
		_, _ = fmt.Fprintf(tw, "%s\t%s\t%s\t%s\t%s\t%s/s / %s/s\t%d/%d/%d\t%s\n", s.Hub, s.TunnelIP, strings.Join(paths, ","),
			bytes(float64(s.RxBytes)), bytes(float64(s.TxBytes)), bytes(s.RxRate), bytes(s.TxRate),
			s.Bond.Queue, s.Bond.Dropped, s.Bond.Skipped, seen)
	}
	_ = tw.Flush()
	printPaths(w, ss)
}

// printPaths lists the bonding metrics of every path (server side: TX is
// the download direction).
func printPaths(w io.Writer, ss []admin.Session) {
	tw := tabwriter.NewWriter(w, 0, 0, 2, ' ', 0)
	_, _ = fmt.Fprintln(tw, "\nHUB\tPATH\tADDR\tSTATE\tPRIORITY\tSRTT\tJITTER\tLOSS\tBTLBW\tRATE TX/RX\tDATA TX/RX\tMTU")
	for _, s := range ss {
		for _, p := range s.Paths {
			st := p.Stats
			_, _ = fmt.Fprintf(tw, "%s\t%d\t%s\t%s\t%s\t%v\t%v\t%.1f%%\t%.1f Mb/s\t%s/s / %s/s\t%s / %s\t%d\n",
				s.Hub, p.ID, p.Addr, st.State, st.Priority, st.SRTT.Round(time.Millisecond), st.Jitter.Round(time.Millisecond),
				st.Loss*100, float64(st.BtlBw)*8/1e6, bytes(float64(st.TxRate)), bytes(float64(st.RxRate)),
				bytes(float64(st.TxData)), bytes(float64(st.RxData)), st.MTU)
		}
	}
	_ = tw.Flush()
}

// bytes formats n as B, KiB, MiB or GiB.
func bytes(n float64) string {
	units := []string{"B", "KiB", "MiB", "GiB", "TiB"}
	i := 0
	for n >= 1024 && i < len(units)-1 {
		n /= 1024
		i++
	}
	if i == 0 {
		return fmt.Sprintf("%.0f %s", n, units[i])
	}
	return fmt.Sprintf("%.1f %s", n, units[i])
}
