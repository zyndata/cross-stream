package main

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/hub"
	"github.com/zyndata/cross-stream/internal/hub/uplink"
	"github.com/zyndata/cross-stream/internal/ipc"
)

func uplinksCmd(ctx context.Context, sock string, args []string) error {
	if len(args) == 0 {
		var res []hub.UplinkStatus
		if err := ipc.Call(ctx, sock, hub.MethodUplinksList, nil, &res); err != nil {
			return err
		}
		return printJSON(res)
	}
	switch {
	case args[0] == "captive" && len(args) >= 2:
		return captiveCmd(ctx, sock, args[1:])
	case args[0] == "forget" && len(args) == 2:
		return ipc.Call(ctx, sock, hub.MethodUplinksForget, hub.UplinkForgetParams{Uplink: args[1]}, nil)
	case args[0] == "set" && len(args) >= 3:
		var cur []hub.UplinkStatus
		if err := ipc.Call(ctx, sock, hub.MethodUplinksList, nil, &cur); err != nil {
			return err
		}
		var lim *control.Limits
		for _, u := range cur {
			if u.ID == args[1] || u.Name == args[1] {
				l := u.Limits
				lim = &l
			}
		}
		if lim == nil {
			return fmt.Errorf("uplinks: no link %q", args[1])
		}
		p, err := parseUplinkSet(args[1], args[2:], lim)
		if err != nil {
			return err
		}
		var res hub.UplinkStatus
		if err := ipc.Call(ctx, sock, hub.MethodUplinksUpdate, p, &res); err != nil {
			return err
		}
		return printJSON(res)
	}
	return errors.New("uplinks: want no arguments, set <id|name> key=value..., forget <id|name> or captive <id|name> [-open]")
}

// captiveCmd checks a link for a captive portal now; -open routes the
// portal host through the link and opens the login page in the browser.
func captiveCmd(ctx context.Context, sock string, args []string) error {
	p, err := parseCaptive(args)
	if err != nil {
		return err
	}
	var res hub.CaptiveResult
	if err := ipc.Call(ctx, sock, hub.MethodUplinksCaptive, p, &res); err != nil {
		return err
	}
	fmt.Println(captiveText(res))
	if p.Open && res.State == uplink.NetCaptive && res.RouteErr == "" {
		if err := openURL(res.URL); err != nil {
			return fmt.Errorf("uplinks captive: open the browser: %w (open %s yourself)", err, res.URL)
		}
	}
	return nil
}

// parseCaptive accepts the link before or after -open.
func parseCaptive(args []string) (hub.CaptiveParams, error) {
	var p hub.CaptiveParams
	for _, a := range args {
		switch {
		case a == "-open" || a == "--open":
			p.Open = true
		case strings.HasPrefix(a, "-") || p.Uplink != "":
			return p, errors.New("usage: xs-hubctl uplinks captive <id|name> [-open]")
		default:
			p.Uplink = a
		}
	}
	if p.Uplink == "" {
		return p, errors.New("usage: xs-hubctl uplinks captive <id|name> [-open]")
	}
	return p, nil
}

func captiveText(r hub.CaptiveResult) string {
	s := fmt.Sprintf("%s (%s): ", r.Name, r.Uplink)
	switch r.State {
	case uplink.NetOpen:
		s += "internet works (no login page)"
	case uplink.NetCaptive:
		s += "login page " + r.URL
		if len(r.Route) > 0 {
			s += fmt.Sprintf("\nportal %s routed through this link for %s", strings.Join(r.Route, ", "), hub.PortalRouteFor)
		}
		if r.RouteErr != "" {
			s += "\nportal route failed: " + r.RouteErr
		}
	default:
		s += "no internet (" + r.Err + ")"
	}
	if r.Hint != "" {
		s += "\n" + r.Hint
	}
	return s
}

// parseUplinkSet turns key=value pairs into uplinks.update params; limit
// keys change a copy of the current limits.
func parseUplinkSet(id string, kvs []string, lim *control.Limits) (uplink.Params, error) {
	p := uplink.Params{Uplink: id}
	limits := false
	for _, kv := range kvs {
		k, v, ok := strings.Cut(kv, "=")
		if !ok {
			return p, fmt.Errorf("uplinks: %q is not key=value", kv)
		}
		num := func() (int, error) {
			n, err := strconv.Atoi(v)
			if err != nil || n < 0 {
				return 0, fmt.Errorf("uplinks: %s wants a number >= 0", k)
			}
			return n, nil
		}
		var err error
		var n int
		switch k {
		case "enabled", "ttl":
			b, perr := strconv.ParseBool(v)
			if perr != nil {
				return p, fmt.Errorf("uplinks: %s wants true or false", k)
			}
			if k == "enabled" {
				p.Enabled = &b
			} else {
				p.TTL = &b
			}
		case "name":
			p.Name = &v
		case "priority":
			p.Priority = &v
		case "up":
			p.Up = &v
		case "down":
			p.Down = &v
		case "rate_kbps":
			if n, err = num(); err == nil {
				r := uint32(n)
				p.RateKbps = &r
			}
		case "cap_mb":
			n, err = num()
			lim.CapMB, limits = int64(n), true
		case "reset_day":
			n, err = num()
			lim.ResetDay, limits = n, true
		case "throttle_kbps":
			n, err = num()
			lim.ThrottleKbps, limits = n, true
		case "period":
			lim.CapPeriod, limits = v, true
		case "over_cap":
			lim.OverCap, limits = v, true
		case "reset":
			p.ResetUsage = v == "1" || v == "true"
		default:
			return p, fmt.Errorf("uplinks: unknown key %q", k)
		}
		if err != nil {
			return p, err
		}
	}
	if limits {
		p.Limits = lim
	}
	return p, nil
}
