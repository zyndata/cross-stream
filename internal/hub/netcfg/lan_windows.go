package netcfg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"

	"github.com/zyndata/cross-stream/internal/iphlp"
)

// On Windows the hub port is the Mobile Hotspot adapter, and Mobile Hotspot
// itself does DHCP, forwarding and the NAT of clients to the tunnel address
// once it shares the tunnel's connection profile (WinHotspot.Reshare). Its
// sharing cannot be changed through HNetCfg while it runs, so LAN only keeps
// the routes: Windows has no source policy routing, so while the TUN exists
// everything not on-link goes into it (0.0.0.0/1 + 128.0.0.0/1, the hub's own
// traffic included); relays are on-link and local uplinks use
// interface-bound sockets. The MSS clamp is done by the hub on the packets
// themselves; port forwards, the loop guard and the uplink TTL rule have no
// Windows equivalent here (docs/decisions.md).

// LoopGuard reports whether LAN uses Blocked: no on Windows, so the hub
// skips resolving the server endpoints for it.
const LoopGuard = false

// ErrNoForwards is returned by ApplyRules on Windows when port forwards are set.
var ErrNoForwards = errors.New("port forwarding is not available on a Windows hub")

// tunRoutes send all non-local traffic into the TUN (more specific than any
// default route, so no metric games).
var tunRoutes = []netip.Prefix{netip.MustParsePrefix("0.0.0.0/1"), netip.MustParsePrefix("128.0.0.0/1")}

// Setup adds the TUN routes when the TUN exists (the watchdog does it later
// otherwise).
func (l LAN) Setup(ctx context.Context) error {
	if err := l.validate(); err != nil {
		return err
	}
	_, err := l.Ensure(ctx)
	return err
}

// Ensure re-adds missing TUN routes and returns what it fixed. With NoPath
// it removes them instead: a hub without a working path must not take the
// PC's own internet away (phase 18).
func (l LAN) Ensure(context.Context) ([]string, error) {
	if err := l.validate(); err != nil {
		return nil, err
	}
	tun, err := net.InterfaceByName(l.TunName)
	if err != nil {
		return nil, nil // no tunnel yet: hotspot clients stay local
	}
	ifc := iphlp.Iface{Index: uint32(tun.Index)}
	have, err := routesOn(ifc)
	if err != nil {
		return nil, fmt.Errorf("netcfg ensure: %w", err)
	}
	var fixed []string
	var errs []error
	for _, p := range tunRoutes {
		r := iphlp.Route{Dst: p}
		if l.NoPath {
			if !have[p] {
				continue
			}
			if err := iphlp.DeleteRoute(ifc, r); err != nil {
				errs = append(errs, err)
			} else {
				fixed = append(fixed, "route "+p.String()+" removed (no path)")
			}
			continue
		}
		if have[p] {
			continue
		}
		if err := iphlp.AddRoute(ifc, r); err != nil {
			errs = append(errs, err)
		} else {
			fixed = append(fixed, "route "+p.String())
		}
	}
	if l.Forward {
		f, err := l.ensureForwarding(tun.Index)
		fixed = append(fixed, f...)
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return fixed, fmt.Errorf("netcfg ensure: %w", err)
	}
	return fixed, nil
}

// ensureForwarding turns IPv4 forwarding on for the TUN and every port that
// exists (NAT mode).
func (l LAN) ensureForwarding(tunIdx int) ([]string, error) {
	idx := map[int]string{tunIdx: l.TunName}
	for _, p := range l.Ports {
		if ifc, err := net.InterfaceByName(p.Iface); err == nil {
			idx[ifc.Index] = p.Iface
		}
	}
	var fixed []string
	var errs []error
	for i, name := range idx {
		changed, err := iphlp.Configure(iphlp.Iface{Index: uint32(i)}, iphlp.Settings{Forwarding: true})
		if err != nil {
			errs = append(errs, err)
		} else if changed {
			fixed = append(fixed, "forwarding on "+name)
		}
	}
	return fixed, errors.Join(errs...)
}

// ApplyRules has nothing to apply on Windows; it refuses port forwards.
func (l LAN) ApplyRules(context.Context) error {
	if err := l.validate(); err != nil {
		return err
	}
	if len(l.Forwards) > 0 {
		return ErrNoForwards
	}
	return nil
}

// Cleanup removes the TUN routes (they also go with the adapter).
func (l LAN) Cleanup(context.Context) error {
	if tun, err := net.InterfaceByName(l.TunName); err == nil {
		for _, p := range tunRoutes {
			_ = iphlp.DeleteRoute(iphlp.Iface{Index: uint32(tun.Index)}, iphlp.Route{Dst: p})
		}
	}
	return nil
}

// InterfaceExists reports whether an interface with this name (alias) exists.
func InterfaceExists(name string) bool {
	if name == "" {
		return false
	}
	_, err := net.InterfaceByName(name)
	return err == nil
}

// routesOn returns the IPv4 prefixes routed through the interface.
func routesOn(ifc iphlp.Iface) (map[netip.Prefix]bool, error) {
	rs, err := iphlp.Routes(ifc)
	if err != nil {
		return nil, err
	}
	have := map[netip.Prefix]bool{}
	for _, r := range rs {
		have[r.Dst] = true
	}
	return have, nil
}
