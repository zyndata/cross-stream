package netcfg

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// Defaults for the LAN → tunnel routing.
const (
	Table             = 100               // policy routing table for LAN clients
	Priority          = 100               // ip rule priority
	NFTTable          = "crossstream_hub" // the only nftables table the hub touches
	UnreachableMetric = 9999              // table 100 fallback while the tunnel is down
)

// Port is one network the hub serves clients on (hotspot, Ethernet share).
type Port struct {
	Iface string
	Addr  netip.Prefix // hub address with the prefix length, e.g. 10.42.0.1/24
}

// LAN routes the client subnets of the hub ports into the tunnel: policy rule
// `from <subnet> iif <port> lookup 100`; table 100 with the subnet routes,
// `unreachable default` (no leak while the tunnel is down) and, while the
// TUN exists, `default dev <tun>`; nft masquerade of the subnets out of the
// TUN, TCP MSS clamping to the tunnel MTU and the loop guard (ports must not
// reach the server endpoint IPs through the tunnel).
type LAN struct {
	Ports   []Port
	TunName string
	MTU     int          // tunnel MTU (MSS = MTU - 40)
	Blocked []netip.Addr // server endpoint IPs (loop guard)
	// Forwards are the port forwards from the tunnel to clients.
	Forwards []Forward
	// TTL lists local uplinks whose outgoing packets get TTL 65 (tethered
	// phones: carriers spot tethering by TTL 63).
	TTL []string
	// NoPath: no path carries traffic (hub OFFLINE or CONNECTING). Windows
	// then drops the TUN routes and leak filters so the PC keeps its own
	// uplink; Linux drops the own-traffic rules (only client subnets stay
	// routed into the TUN).
	NoPath bool
	// OwnTraffic (Linux) routes the laptop's own traffic into the tunnel
	// while a path carries traffic (own.go). Windows ignores it: its routes
	// always carry the laptop's own traffic.
	OwnTraffic bool
	// TunAddr is the hub's tunnel address: the hub DNS listens there and
	// systemd-resolved is pointed at it while own traffic is routed.
	TunAddr netip.Addr
	// Leaks (Windows) keeps the WFP DNS/IPv6 leak filters, present exactly
	// while the TUN routes are (nil elsewhere).
	Leaks *LeakGuard
	// Forward (Windows NAT mode) turns IP forwarding on for the TUN and the
	// ports: Mobile Hotspot routes its clients only into the profile it
	// shares, the hub NATs them on the TUN (tun.NAT). Linux ignores it.
	Forward bool
	Run     Runner // nil = exec
}

// Ruleset returns the nft script that (re)creates the hub table.
func (l LAN) Ruleset() string {
	var b strings.Builder
	fmt.Fprintf(&b, "table ip %[1]s\ndelete table ip %[1]s\ntable ip %[1]s {\n", NFTTable)
	b.WriteString("\tchain postrouting {\n\t\ttype nat hook postrouting priority srcnat; policy accept;\n")
	for _, p := range l.Ports {
		fmt.Fprintf(&b, "\t\tip saddr %s oifname %q masquerade\n", p.Addr.Masked(), l.TunName)
	}
	b.WriteString("\t}\n")
	b.WriteString(l.forwardChain())
	mss := l.MTU - 40
	b.WriteString("\tchain forward {\n\t\ttype filter hook forward priority mangle; policy accept;\n")
	fmt.Fprintf(&b, "\t\toifname %q tcp flags syn tcp option maxseg size > %[2]d tcp option maxseg size set %[2]d\n", l.TunName, mss)
	fmt.Fprintf(&b, "\t\tiifname %q tcp flags syn tcp option maxseg size > %[2]d tcp option maxseg size set %[2]d\n", l.TunName, mss)
	b.WriteString("\t}\n")
	if len(l.Blocked) > 0 && len(l.Ports) > 0 {
		ifs := make([]string, len(l.Ports))
		for i, p := range l.Ports {
			ifs[i] = strconv.Quote(p.Iface)
		}
		ips := make([]string, len(l.Blocked))
		for i, a := range l.Blocked {
			ips[i] = a.String()
		}
		b.WriteString("\tchain loopguard {\n\t\ttype filter hook forward priority filter; policy accept;\n")
		fmt.Fprintf(&b, "\t\tiifname { %s } ip daddr { %s } drop\n", strings.Join(ifs, ", "), strings.Join(ips, ", "))
		b.WriteString("\t}\n")
	}
	if len(l.TTL) > 0 {
		ifs := make([]string, len(l.TTL))
		for i, n := range l.TTL {
			ifs[i] = strconv.Quote(n)
		}
		b.WriteString("\tchain uplinkttl {\n\t\ttype filter hook postrouting priority mangle; policy accept;\n")
		fmt.Fprintf(&b, "\t\toifname { %s } ip ttl set 65\n", strings.Join(ifs, ", "))
		b.WriteString("\t}\n")
	}
	if l.TunAddr.IsValid() {
		// The hub DNS on the tunnel address is for the laptop itself only.
		b.WriteString("\tchain owndns {\n\t\ttype filter hook input priority filter; policy accept;\n")
		fmt.Fprintf(&b, "\t\tiifname %q meta l4proto { tcp, udp } th dport 53 drop\n", l.TunName)
		b.WriteString("\t}\n")
	}
	b.WriteString("}\n")
	b.WriteString(l.ownRuleset(l.ownActive()))
	return b.String()
}

// route is one wanted table 100 route: the `ip route replace` arguments and
// the line prefix `ip route show table 100` prints for it.
type route struct {
	args []string
	show string
}

func (l LAN) routes(tunUp bool) []route {
	t := strconv.Itoa(Table)
	var rs []route
	for _, p := range l.Ports {
		sub := p.Addr.Masked().String()
		rs = append(rs, route{[]string{"route", "replace", sub, "dev", p.Iface, "table", t}, sub + " dev " + p.Iface + " "})
	}
	m := strconv.Itoa(UnreachableMetric)
	rs = append(rs, route{[]string{"route", "replace", "unreachable", "default", "metric", m, "table", t}, "unreachable default "})
	if tunUp {
		rs = append(rs, route{[]string{"route", "replace", "default", "dev", l.TunName, "table", t}, "default dev " + l.TunName + " "})
	}
	return rs
}

// ruleArgs returns the `ip rule add` arguments and a description of the
// policy rule for port p.
func ruleArgs(p Port) (add []string, desc string) {
	sub, t := p.Addr.Masked().String(), strconv.Itoa(Table)
	return []string{"rule", "add", "from", sub, "iif", p.Iface, "lookup", t, "priority", strconv.Itoa(Priority)},
		"from " + sub + " iif " + p.Iface + " lookup " + t
}

// hasRule reports whether `ip rule show` output holds the rule of port p
// (iproute2 may print "[detached]" after the iif name).
func hasRule(out string, p Port) bool {
	from := "from " + p.Addr.Masked().String() + " iif " + p.Iface + " "
	for _, l := range lines(out) {
		if strings.Contains(l, from) && strings.Contains(l, " lookup "+strconv.Itoa(Table)+" ") {
			return true
		}
	}
	return false
}

func (l LAN) validate() error {
	if l.TunName == "" || l.MTU < 576 || len(l.Ports) == 0 {
		return errors.New("netcfg: ports, tun name and MTU are required")
	}
	for _, p := range l.Ports {
		if p.Iface == "" || !p.Addr.IsValid() || !p.Addr.Addr().Is4() {
			return fmt.Errorf("netcfg: port %q needs an IPv4 address", p.Iface)
		}
	}
	return nil
}

// tunUp reports whether the TUN interface exists.
func (l LAN) tunUp() bool {
	_, err := net.InterfaceByName(l.TunName)
	return err == nil
}

// setupNft enables IPv4 forwarding and installs the policy rules and the nft
// table, replacing leftovers of a crashed run. Routes need the ports up:
// call Ensure next (and from the watchdog).
func (l LAN) setupNft(ctx context.Context) error {
	if err := l.validate(); err != nil {
		return err
	}
	if err := enableForwarding(); err != nil {
		return err
	}
	l.deleteRules(ctx)
	l.deleteOwn(ctx) // own-traffic leftovers of a crashed run: Ensure adds them when wanted
	for _, p := range l.Ports {
		add, _ := ruleArgs(p)
		if _, err := runWith(ctx, l.Run, "", "ip", add...); err != nil {
			return fmt.Errorf("netcfg setup: %w", err)
		}
	}
	return l.applyNft(ctx)
}

// ensureNft re-applies whatever is missing (watchdog: NM or DHCP rewrote the
// routes, the TUN came or went). It changes nothing that is already right,
// so it does not trigger its own netlink events. It returns what it fixed.
func (l LAN) ensureNft(ctx context.Context) ([]string, error) {
	if err := l.validate(); err != nil {
		return nil, err
	}
	var fixed []string
	var errs []error
	if !forwardingOn() {
		if err := enableForwarding(); err != nil {
			errs = append(errs, err)
		} else {
			fixed = append(fixed, "ip_forward")
		}
	}
	out, err := runWith(ctx, l.Run, "", "ip", "route", "show", "table", strconv.Itoa(Table))
	if err != nil && strings.Contains(err.Error(), "table does not exist") {
		out, err = "", nil // empty table: every route is missing
	}
	if err != nil {
		errs = append(errs, err)
	} else {
		have := lines(out)
		for _, r := range l.routes(l.tunUp()) {
			if hasPrefix(have, r.show) {
				continue
			}
			if _, err := runWith(ctx, l.Run, "", "ip", r.args...); err != nil {
				errs = append(errs, err)
			} else {
				fixed = append(fixed, strings.TrimSpace(r.show))
			}
		}
	}
	if out, err = runWith(ctx, l.Run, "", "ip", "rule", "show"); err != nil {
		errs = append(errs, err)
	} else {
		for _, p := range l.Ports {
			add, show := ruleArgs(p)
			if hasRule(out, p) {
				continue
			}
			if _, err := runWith(ctx, l.Run, "", "ip", add...); err != nil {
				errs = append(errs, err)
			} else {
				fixed = append(fixed, "rule "+show)
			}
		}
		f, err := l.ensureOwn(ctx, out, l.ownActive())
		fixed = append(fixed, f...)
		errs = append(errs, err)
	}
	if _, err := runWith(ctx, l.Run, "", "nft", "list", "table", "ip", NFTTable); err != nil {
		if err := l.applyNft(ctx); err != nil {
			errs = append(errs, err)
		} else {
			fixed = append(fixed, "nft table")
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fixed, fmt.Errorf("netcfg ensure: %w", err)
	}
	return fixed, nil
}

func lines(s string) []string {
	ls := strings.Split(s, "\n")
	for i, l := range ls {
		ls[i] = strings.TrimSpace(l) + " "
	}
	return ls
}

func hasPrefix(ls []string, p string) bool {
	for _, l := range ls {
		if strings.HasPrefix(l, p) {
			return true
		}
	}
	return false
}

// applyNft re-applies the nft table, e.g. so the MSS clamp follows a new
// tunnel MTU (l.MTU) or the loop guard new server IPs; routing is unchanged.
func (l LAN) applyNft(ctx context.Context) error {
	if err := l.validate(); err != nil {
		return err
	}
	if _, err := runWith(ctx, l.Run, l.Ruleset(), "nft", "-f", "-"); err != nil {
		return fmt.Errorf("netcfg rules: %w", err)
	}
	return nil
}

const ipForward = "/proc/sys/net/ipv4/ip_forward"

func forwardingOn() bool {
	b, err := os.ReadFile(ipForward)
	return err == nil && strings.TrimSpace(string(b)) == "1"
}

// enableForwarding turns on IPv4 forwarding. In a container /proc/sys is
// read-only; that is fine when forwarding is already on (compose sysctls).
func enableForwarding() error {
	err := os.WriteFile(ipForward, []byte("1\n"), 0o644)
	if err == nil || forwardingOn() {
		return nil
	}
	return fmt.Errorf("netcfg: enable ip_forward: %w", err)
}

// cleanupNft removes the policy rules, table 100 and the nft table. Errors are
// joined; it does not stop at the first one.
func (l LAN) cleanupNft(ctx context.Context) error {
	l.deleteRules(ctx)
	l.deleteOwn(ctx)
	var errs []error
	if _, err := l.ensureResolved(ctx, false); err != nil {
		errs = append(errs, err)
	}
	if _, err := runWith(ctx, l.Run, "", "ip", "route", "flush", "table", strconv.Itoa(Table)); err != nil {
		errs = append(errs, err)
	}
	if _, err := runWith(ctx, l.Run, "", "nft", "delete", "table", "ip", NFTTable); err != nil {
		errs = append(errs, err)
	}
	if err := errors.Join(errs...); err != nil {
		return fmt.Errorf("netcfg cleanup: %w", err)
	}
	return nil
}

// deleteRules removes every copy of our policy rules (none is fine).
func (l LAN) deleteRules(ctx context.Context) {
	for _, p := range l.Ports {
		sub := p.Addr.Masked().String()
		for range 8 {
			if _, err := runWith(ctx, l.Run, "", "ip", "rule", "del", "from", sub, "iif", p.Iface, "lookup", strconv.Itoa(Table)); err != nil {
				break
			}
		}
	}
}

// InterfaceIPv4 returns the first IPv4 address of iface with its prefix
// length, e.g. 10.42.0.1/24.
func InterfaceIPv4(iface string) (netip.Prefix, error) {
	ifc, err := net.InterfaceByName(iface)
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("interface %s: %w", iface, err)
	}
	addrs, err := ifc.Addrs()
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("interface %s: %w", iface, err)
	}
	for _, a := range addrs {
		ipn, ok := a.(*net.IPNet)
		if !ok || ipn.IP.To4() == nil {
			continue
		}
		ones, _ := ipn.Mask.Size()
		ip, _ := netip.AddrFromSlice(ipn.IP.To4())
		return netip.PrefixFrom(ip, ones), nil
	}
	return netip.Prefix{}, fmt.Errorf("interface %s has no IPv4 address", iface)
}
