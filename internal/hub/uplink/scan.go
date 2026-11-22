package uplink

import (
	"fmt"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// SysRoot is where sysfs and procfs are read (tests use a fake tree).
type SysRoot string

// Scan lists the interfaces that qualify as uplinks: up, not virtual, not
// in exclude (hotspot, LAN, tunnel), with an IPv4 address and a default
// route of their own. Linux only (elsewhere /proc/net/route is missing).
func Scan(exclude []string) ([]Iface, error) {
	return SysRoot("/").scan(exclude, net.Interfaces, addrsOf)
}

func addrsOf(i net.Interface) ([]net.Addr, error) { return i.Addrs() }

func (r SysRoot) path(p ...string) string { return filepath.Join(append([]string{string(r)}, p...)...) }

func (r SysRoot) scan(exclude []string, list func() ([]net.Interface, error),
	addrs func(net.Interface) ([]net.Addr, error)) ([]Iface, error) {
	b, err := os.ReadFile(r.path("proc", "net", "route"))
	if err != nil {
		return nil, fmt.Errorf("uplink: routes: %w", err)
	}
	routes := ParseDefaultRoutes(string(b))
	ifs, err := list()
	if err != nil {
		return nil, fmt.Errorf("uplink: interfaces: %w", err)
	}
	var out []Iface
	for _, i := range ifs {
		rt, ok := routes[i.Name]
		if !ok || i.Flags&net.FlagUp == 0 || i.Flags&net.FlagLoopback != 0 || Virtual(i.Name) || slices.Contains(exclude, i.Name) {
			continue
		}
		pfx, ok := firstIPv4(addrs(i))
		if !ok {
			continue
		}
		driver := r.driver(i.Name)
		out = append(out, Iface{Name: i.Name, MAC: i.HardwareAddr.String(), Kind: Classify(i.Name, driver, r.devtype(i.Name)),
			Driver: driver, Addr: pfx, Gateway: rt.Gateway, Metric: rt.Metric})
	}
	return out, nil
}

func firstIPv4(as []net.Addr, err error) (netip.Prefix, bool) {
	if err != nil {
		return netip.Prefix{}, false
	}
	for _, a := range as {
		if n, ok := a.(*net.IPNet); ok {
			if ip, ok := netip.AddrFromSlice(n.IP); ok && ip.Unmap().Is4() {
				ones, _ := n.Mask.Size()
				return netip.PrefixFrom(ip.Unmap(), ones), true
			}
		}
	}
	return netip.Prefix{}, false
}

// driver is the kernel driver of an interface ("" if virtual or unknown).
func (r SysRoot) driver(name string) string {
	l, err := os.Readlink(r.path("sys", "class", "net", name, "device", "driver"))
	if err != nil {
		return ""
	}
	return filepath.Base(l)
}

// devtype is DEVTYPE from the interface's uevent (wlan, wwan, bluetooth…).
func (r SysRoot) devtype(name string) string {
	b, err := os.ReadFile(r.path("sys", "class", "net", name, "uevent"))
	if err != nil {
		return ""
	}
	for _, l := range strings.Split(string(b), "\n") {
		if v, ok := strings.CutPrefix(l, "DEVTYPE="); ok {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
