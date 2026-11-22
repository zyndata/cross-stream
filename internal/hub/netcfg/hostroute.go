package netcfg

import (
	"context"
	"errors"
	"fmt"
	"net/netip"
	"runtime"
)

// ErrNoHostRoute is returned where the OS has no host route support (only
// Linux and Windows have it).
var ErrNoHostRoute = errors.New("netcfg: host routes are not supported on " + runtime.GOOS + " yet")

// HostRoute sends traffic to one IPv4 address out of iface (through gw when
// valid), next to the default route: the captive portal login page of a
// local uplink opens in the user's browser while the laptop's default route
// is another link. Del removes it again.
type HostRoute struct {
	Dst   netip.Addr
	Iface string
	GW    netip.Addr // invalid = on-link (point-to-point)
	Run   Runner     // nil = exec
}

func (r HostRoute) args(op string) ([]string, error) {
	if !r.Dst.Is4() || r.Iface == "" {
		return nil, fmt.Errorf("netcfg: host route %v dev %q: want an IPv4 address and an interface", r.Dst, r.Iface)
	}
	a := []string{"-4", "route", op, netip.PrefixFrom(r.Dst, 32).String()}
	if r.GW.IsValid() && op != "del" {
		a = append(a, "via", r.GW.String())
	}
	return append(a, "dev", r.Iface), nil
}

// Add adds or replaces the route.
func (r HostRoute) Add(ctx context.Context) error { return r.do(ctx, "replace") }

// Del removes the route.
func (r HostRoute) Del(ctx context.Context) error { return r.do(ctx, "del") }

func (r HostRoute) do(ctx context.Context, op string) error {
	if runtime.GOOS == "windows" {
		return r.native(op)
	}
	if runtime.GOOS != "linux" {
		return ErrNoHostRoute
	}
	a, err := r.args(op)
	if err != nil {
		return err
	}
	if _, err := runWith(ctx, r.Run, "", "ip", a...); err != nil {
		return fmt.Errorf("netcfg: host route: %w", err)
	}
	return nil
}
