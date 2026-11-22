package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"net/netip"
	"strconv"
	"strings"

	"github.com/zyndata/cross-stream/internal/bypass"
	"github.com/zyndata/cross-stream/internal/hub"
	"github.com/zyndata/cross-stream/internal/ipc"
)

// bypassCmd shows the bypass list, or replaces it ("set" entries...) or
// empties it ("clear"). An entry that is an IPv4 address or CIDR is a CIDR
// rule, anything else a domain.
func bypassCmd(ctx context.Context, sock string, args []string) error {
	var res hub.BypassInfo
	if len(args) == 0 {
		if err := ipc.Call(ctx, sock, hub.MethodBypassGet, nil, &res); err != nil {
			return err
		}
		return printJSON(res)
	}
	r := bypass.Rules{Domains: []string{}, CIDRs: []string{}}
	switch args[0] {
	case "clear":
	case "set":
		for _, a := range args[1:] {
			for _, e := range strings.Split(a, ",") {
				if e = strings.TrimSpace(e); e == "" {
					continue
				}
				if isCIDR(e) {
					r.CIDRs = append(r.CIDRs, e)
				} else {
					r.Domains = append(r.Domains, e)
				}
			}
		}
	default:
		return errors.New("bypass: want no arguments, set <domain|cidr>..., or clear")
	}
	if err := ipc.Call(ctx, sock, hub.MethodBypassSet, r, &res); err != nil {
		return err
	}
	return printJSON(res)
}

func isCIDR(s string) bool {
	if _, err := netip.ParsePrefix(s); err == nil {
		return true
	}
	_, err := netip.ParseAddr(s)
	return err == nil
}

// forwardsCmd shows the port forwards, or replaces them ("set
// proto/port=target..." with target a client MAC or IPv4) or removes them
// ("clear").
func forwardsCmd(ctx context.Context, sock string, args []string) error {
	var res hub.ForwardsInfo
	if len(args) == 0 {
		if err := ipc.Call(ctx, sock, hub.MethodFwdList, nil, &res); err != nil {
			return err
		}
		return printJSON(res)
	}
	p := hub.ForwardsParams{Forwards: []hub.ForwardRule{}}
	switch args[0] {
	case "clear":
	case "set":
		for _, a := range args[1:] {
			r, err := parseForward(a)
			if err != nil {
				return err
			}
			p.Forwards = append(p.Forwards, r)
		}
	default:
		return errors.New("forwards: want no arguments, set proto/port=target..., or clear")
	}
	if err := ipc.Call(ctx, sock, hub.MethodFwdSet, p, &res); err != nil {
		return err
	}
	return printJSON(res)
}

// parseForward parses "tcp/8080=10.42.0.20" or "udp/41820=aa:bb:cc:dd:ee:ff".
func parseForward(s string) (hub.ForwardRule, error) {
	pp, target, ok := strings.Cut(s, "=")
	proto, port, ok2 := strings.Cut(pp, "/")
	n, err := strconv.ParseUint(port, 10, 16)
	if !ok || !ok2 || err != nil {
		return hub.ForwardRule{}, fmt.Errorf("forward %q: want proto/port=target", s)
	}
	return hub.ForwardRule{Proto: proto, Port: uint16(n), Target: target}, nil
}

// usageCmd prints the usage history (per relay per day, failovers, speed tests).
func usageCmd(ctx context.Context, sock string, args []string) error {
	fs := flag.NewFlagSet("usage", flag.ContinueOnError)
	var p hub.UsageParams
	fs.StringVar(&p.Relay, "relay", "", "relay id or name (default: all)")
	fs.StringVar(&p.From, "from", "", "first day YYYY-MM-DD")
	fs.StringVar(&p.To, "to", "", "last day YYYY-MM-DD")
	if err := fs.Parse(args); err != nil {
		return err
	}
	var res hub.UsageInfo
	if err := ipc.Call(ctx, sock, hub.MethodUsageGet, p, &res); err != nil {
		return err
	}
	return printJSON(res)
}
