package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"strings"

	"github.com/zyndata/cross-stream/internal/hub"
)

// probeCmd is the server health check used by deploy/server/remote-deploy.sh.
func probeCmd(ctx context.Context, dir string, args []string, log *slog.Logger) error {
	fs := flag.NewFlagSet("probe", flag.ContinueOnError)
	to := fs.String("to", "", "server host:port list to send to instead of the enrolled endpoints")
	via := fs.String("via", "", "send from this interface's IPv4 address (the route still follows the "+
		"routing table; default: routing table)")
	code := fs.String("code", "", "XS1 code to enroll -dir with when it has no enrollment yet")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if fs.NArg() != 0 {
		return errors.New(usage)
	}
	var eps []string
	for _, s := range strings.Split(*to, ",") {
		if s = strings.TrimSpace(s); s != "" {
			eps = append(eps, "udp://"+s)
		}
	}
	addrs, err := hub.ResolveEndpoints(eps)
	if len(eps) == 0 {
		addrs, err = nil, nil
	}
	if err != nil {
		return err
	}
	r, err := hub.ProbeServer(ctx, dir, *via, addrs, *code, version, log)
	if err != nil {
		return err
	}
	how := "probe"
	if r.Enrolled {
		how = "enrolled, probe"
	}
	fmt.Printf("%s OK: hub %s, tunnel ip %s, handshake %d ms\n", how, r.Name, r.TunnelIP, r.RTT.Milliseconds())
	return nil
}
