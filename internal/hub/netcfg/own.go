package netcfg

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
)

// Own traffic (phase 21, Linux): while LAN.OwnTraffic is set and a path
// carries traffic, the laptop's own packets are routed into the tunnel the
// wg-quick way — specific routes of `main` win (home LAN, Docker bridges,
// the hotspot subnet, captive-portal host routes), everything else that is
// not a daemon socket (Mark) looks up table 100. IPv6 output that is not
// link-local or multicast is rejected (the tunnel is IPv4 only) and
// systemd-resolved sends every lookup to the hub DNS on the tunnel address.
// Without a carrying path all of it is removed: the laptop falls back to
// its own links (owner decision, docs/decisions.md).
//
// Sockets bound to one link (SO_BINDTODEVICE / IP_UNICAST_IF: NetworkManager's
// per-device connectivity check, curl --interface) keep that link's main
// routes (OwnBoundPriority, one rule per link with a default route):
// otherwise NM sees "limited" connectivity, demotes the link's default route
// by 20000 and the laptop falls back to a metered modem after "off".
const (
	OwnBoundPriority    = Priority - 1 // oif <link> lookup main
	OwnSuppressPriority = Priority + 1 // lookup main suppress_prefixlength 0
	OwnPriority         = Priority + 2 // not fwmark Mark lookup 100
)

var markHex = "0x" + strconv.FormatInt(Mark, 16)

// ownRule is one wanted own-traffic policy rule.
type ownRule struct {
	args []string // after "ip rule add" / "ip rule del"
	prio int
	has  []string // substrings of its `ip rule show` line
}

func ownRules() []ownRule {
	t := strconv.Itoa(Table)
	return []ownRule{
		{[]string{"lookup", "main", "suppress_prefixlength", "0", "priority", strconv.Itoa(OwnSuppressPriority)},
			OwnSuppressPriority, []string{"lookup main", "suppress_prefixlength 0"}},
		{[]string{"not", "fwmark", markHex, "lookup", t, "priority", strconv.Itoa(OwnPriority)},
			OwnPriority, []string{"not ", "fwmark " + markHex, "lookup " + t}},
	}
}

// boundRule keeps sockets bound to dev on the main table.
func boundRule(dev string) ownRule {
	return ownRule{[]string{"oif", dev, "lookup", "main", "priority", strconv.Itoa(OwnBoundPriority)},
		OwnBoundPriority, []string{"oif " + dev + " ", "lookup main"}}
}

// boundDevs lists the links of the bound rules in `ip rule show` output.
func boundDevs(rules string) []string {
	var devs []string
	p := strconv.Itoa(OwnBoundPriority) + ":"
	for _, l := range lines(rules) {
		f := strings.Fields(l)
		if len(f) == 0 || f[0] != p || !strings.Contains(l, "lookup main") {
			continue
		}
		for i := 1; i+1 < len(f); i++ {
			if f[i] == "oif" {
				devs = append(devs, f[i+1])
				break
			}
		}
	}
	return devs
}

// defaultDevs lists the links of `ip -4 route show default` (main table)
// output, the tunnel left out, each once.
func defaultDevs(routes, tun string) []string {
	var devs []string
	seen := map[string]bool{tun: true}
	for _, l := range lines(routes) {
		f := strings.Fields(l)
		for i := 0; i+1 < len(f); i++ {
			if f[i] == "dev" && !seen[f[i+1]] {
				seen[f[i+1]] = true
				devs = append(devs, f[i+1])
				break
			}
		}
	}
	return devs
}

// has reports whether `ip rule show` output holds the rule.
func (r ownRule) in(out string) bool {
	p := strconv.Itoa(r.prio) + ":"
	for _, l := range lines(out) {
		if !strings.HasPrefix(l, p) {
			continue
		}
		ok := true
		for _, s := range r.has {
			ok = ok && strings.Contains(l, s)
		}
		if ok {
			return true
		}
	}
	return false
}

// ownActive reports whether the laptop's own traffic is routed into the
// tunnel now: switch on, a path carries traffic and the TUN exists.
func (l LAN) ownActive() bool { return l.OwnTraffic && !l.NoPath && l.tunUp() }

// ownRuleset is the nft part for own traffic: the IPv6 reject (own table of
// family ip6, same name). Empty while own traffic is not active.
func (l LAN) ownRuleset(active bool) string {
	if !active {
		return ""
	}
	var b strings.Builder
	fmt.Fprintf(&b, "table ip6 %[1]s\ndelete table ip6 %[1]s\ntable ip6 %[1]s {\n", NFTTable)
	b.WriteString("\tchain ownv6 {\n\t\ttype filter hook output priority filter; policy accept;\n")
	fmt.Fprintf(&b, "\t\tmeta mark %s accept\n", markHex)
	b.WriteString("\t\toifname \"lo\" accept\n")
	b.WriteString("\t\tip6 daddr { fe80::/10, ff00::/8 } accept\n")
	b.WriteString("\t\treject with icmpv6 type admin-prohibited\n")
	b.WriteString("\t}\n}\n")
	return b.String()
}

// ensureOwn brings the own-traffic rules, the IPv6 table and the resolved
// link settings to the wanted state (active or not) and returns what it
// changed.
func (l LAN) ensureOwn(ctx context.Context, rules string, active bool) ([]string, error) {
	var fixed []string
	var errs []error
	for _, r := range ownRules() {
		in := r.in(rules)
		switch {
		case active && !in:
			if _, err := runWith(ctx, l.Run, "", "ip", append([]string{"rule", "add"}, r.args...)...); err != nil {
				errs = append(errs, err)
			} else {
				fixed = append(fixed, "own rule "+strconv.Itoa(r.prio))
			}
		case !active && in:
			l.deleteOwnRule(ctx, r)
			fixed = append(fixed, "own rule "+strconv.Itoa(r.prio)+" removed")
		}
	}
	f, err := l.ensureBound(ctx, rules, active)
	fixed = append(fixed, f...)
	errs = append(errs, err)
	_, err = runWith(ctx, l.Run, "", "nft", "list", "table", "ip6", NFTTable)
	switch {
	case active && err != nil:
		if err := l.applyNft(ctx); err != nil {
			errs = append(errs, err)
		} else {
			fixed = append(fixed, "nft ip6 table")
		}
	case !active && err == nil:
		if _, err := runWith(ctx, l.Run, "", "nft", "delete", "table", "ip6", NFTTable); err != nil {
			errs = append(errs, err)
		} else {
			fixed = append(fixed, "nft ip6 table removed")
		}
	}
	f, err = l.ensureResolved(ctx, active)
	fixed = append(fixed, f...)
	errs = append(errs, err)
	return fixed, errors.Join(errs...)
}

// ensureBound keeps one bound rule per link with a main default route while
// own traffic is active and removes the others (links gone, or inactive).
func (l LAN) ensureBound(ctx context.Context, rules string, active bool) ([]string, error) {
	var devs []string
	if active {
		out, err := runWith(ctx, l.Run, "", "ip", "-4", "route", "show", "default")
		if err != nil {
			return nil, err
		}
		devs = defaultDevs(out, l.TunName)
	}
	var fixed []string
	var errs []error
	want := map[string]bool{}
	for _, d := range devs {
		want[d] = true
		r := boundRule(d)
		if r.in(rules) {
			continue
		}
		if _, err := runWith(ctx, l.Run, "", "ip", append([]string{"rule", "add"}, r.args...)...); err != nil {
			errs = append(errs, err)
		} else {
			fixed = append(fixed, "bound rule "+d)
		}
	}
	for _, d := range boundDevs(rules) {
		if !want[d] {
			l.deleteOwnRule(ctx, boundRule(d))
			fixed = append(fixed, "bound rule "+d+" removed")
		}
	}
	return fixed, errors.Join(errs...)
}

func (l LAN) deleteOwnRule(ctx context.Context, r ownRule) {
	for range 8 {
		if _, err := runWith(ctx, l.Run, "", "ip", append([]string{"rule", "del"}, r.args...)...); err != nil {
			return
		}
	}
}

// deleteOwn removes own-traffic rules and the IPv6 table (Setup: leftovers
// of a crashed run; Cleanup). Errors mean "nothing there".
func (l LAN) deleteOwn(ctx context.Context) {
	for _, r := range ownRules() {
		l.deleteOwnRule(ctx, r)
	}
	if out, err := runWith(ctx, l.Run, "", "ip", "rule", "show"); err == nil {
		for _, d := range boundDevs(out) {
			l.deleteOwnRule(ctx, boundRule(d))
		}
	}
	_, _ = runWith(ctx, l.Run, "", "nft", "delete", "table", "ip6", NFTTable)
}

// ensureResolved points systemd-resolved at the hub DNS on the tunnel
// address while own traffic is active (all names: routing domain "~."),
// and reverts the link otherwise. Without resolvectl or a running resolved
// it does nothing: DNS stays as the system has it (ResolvedAvailable).
func (l LAN) ensureResolved(ctx context.Context, active bool) ([]string, error) {
	if !l.tunUp() {
		return nil, nil // resolved forgets a link's settings when it goes
	}
	out, err := runWith(ctx, l.Run, "", "resolvectl", "dns", l.TunName)
	if err != nil {
		return nil, nil // no resolved: left alone, status says so
	}
	have := resolvedServers(out)
	if !active || !l.TunAddr.IsValid() {
		if have == "" {
			return nil, nil
		}
		if _, err := runWith(ctx, l.Run, "", "resolvectl", "revert", l.TunName); err != nil {
			return nil, err
		}
		return []string{"resolved " + l.TunName + " reverted"}, nil
	}
	if have == l.TunAddr.String() {
		return nil, nil
	}
	for _, args := range [][]string{
		{"dns", l.TunName, l.TunAddr.String()},
		{"domain", l.TunName, "~."},
		{"default-route", l.TunName, "yes"},
	} {
		if _, err := runWith(ctx, l.Run, "", "resolvectl", args...); err != nil {
			return nil, err
		}
	}
	return []string{"resolved dns " + l.TunName + " " + l.TunAddr.String()}, nil
}

// resolvedServers returns the server list of `resolvectl dns <link>`
// output ("Link 7 (xs0): 100.64.0.2"), trimmed; "" = none.
func resolvedServers(out string) string {
	_, s, ok := strings.Cut(strings.TrimSpace(out), "):")
	if !ok {
		return ""
	}
	return strings.TrimSpace(s)
}

// ResolvedAvailable reports whether systemd-resolved can be configured
// (resolvectl present and answering). Without it own traffic leaves DNS as
// the system has it.
func ResolvedAvailable(ctx context.Context, run Runner) bool {
	_, err := runWith(ctx, run, "", "resolvectl", "status")
	return err == nil
}
