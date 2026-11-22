package hub

import (
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/zyndata/cross-stream/internal/hub/netcfg"
	"github.com/zyndata/cross-stream/internal/wire"
)

// ForwardsFile holds the port forwards (architecture.md "Port forwarding").
const ForwardsFile = "forwards.json"

// Port forward limits on the hub side (the server checks them again).
const (
	MaxForwards   = 20
	FwdPortMin    = 1024
	FwdPortMax    = 49151
	FwdResolveGap = 10 * time.Second // client addresses are re-resolved this often
)

// Forward states in ForwardStatus.
const (
	FwdActive    = "active"     // server accepted, client found
	FwdPending   = "pending"    // waiting for the server (or no tunnel)
	FwdRefused   = "refused"    // see Reason
	FwdNoClient  = "no_client"  // the target MAC has no address now
	FwdNotOnline = "not_online" // hub not enrolled / tunnel down
)

// ErrForward is returned for an invalid forward list.
var ErrForward = errors.New("forwards: invalid entry")

// ForwardRule is one port forward the owner set: public port → the same
// port on a client chosen by MAC (re-resolved from DHCP leases) or IPv4.
type ForwardRule struct {
	Proto  string `json:"proto"` // tcp or udp
	Port   uint16 `json:"port"`
	Target string `json:"target"` // client MAC or IPv4 address
	Name   string `json:"name,omitempty"`
}

// ForwardStatus is a rule with its state.
type ForwardStatus struct {
	ForwardRule
	Client string `json:"client,omitempty"` // resolved client address
	State  string `json:"state"`
	Reason string `json:"reason,omitempty"` // refused: not_allowed, restricted, out_of_range, in_use
}

// ForwardsInfo is the result of forwards.list / forwards.set.
type ForwardsInfo struct {
	Server   string          `json:"server,omitempty"` // public host to reach the ports on
	Forwards []ForwardStatus `json:"forwards"`
}

func (r ForwardRule) wire() wire.Forward {
	p := wire.ProtoTCP
	if r.Proto == "udp" {
		p = wire.ProtoUDP
	}
	return wire.Forward{Proto: p, Port: r.Port}
}

// normForwards checks a list: tcp/udp, port 1024-49151, unique per
// protocol, at most MaxForwards, target a MAC or an IPv4 address in lan.
func normForwards(in []ForwardRule, lan []netip.Prefix) ([]ForwardRule, error) {
	if len(in) > MaxForwards {
		return nil, fmt.Errorf("%w: at most %d forwards", ErrForward, MaxForwards)
	}
	out := make([]ForwardRule, 0, len(in))
	seen := make(map[wire.Forward]bool)
	for _, r := range in {
		r.Proto = strings.ToLower(strings.TrimSpace(r.Proto))
		r.Target = strings.TrimSpace(r.Target)
		r.Name = strings.TrimSpace(r.Name)
		if r.Proto != "tcp" && r.Proto != "udp" {
			return nil, fmt.Errorf("%w: protocol %q (tcp or udp)", ErrForward, r.Proto)
		}
		if r.Port < FwdPortMin || r.Port > FwdPortMax {
			return nil, fmt.Errorf("%w: port %d outside %d-%d", ErrForward, r.Port, FwdPortMin, FwdPortMax)
		}
		if seen[r.wire()] {
			return nil, fmt.Errorf("%w: %s/%d twice", ErrForward, r.Proto, r.Port)
		}
		seen[r.wire()] = true
		if m, err := net.ParseMAC(r.Target); err == nil && len(m) == 6 {
			r.Target = m.String()
		} else if a, err := netip.ParseAddr(r.Target); err == nil && a.Is4() && inAny(lan, a) {
			r.Target = a.String()
		} else {
			return nil, fmt.Errorf("%w: target %q (client MAC or an IPv4 address on the hub network)", ErrForward, r.Target)
		}
		out = append(out, r)
	}
	return out, nil
}

func inAny(ps []netip.Prefix, a netip.Addr) bool {
	for _, p := range ps {
		if p.Masked().Contains(a) {
			return true
		}
	}
	return false
}

func (d *Daemon) lanPrefixes() []netip.Prefix {
	var out []netip.Prefix
	for _, p := range d.ports() {
		out = append(out, p.Addr)
	}
	return out
}

// loadForwards reads forwards.json.
func (d *Daemon) loadForwards() error {
	var l []ForwardRule
	if _, err := loadJSON(d.cfg.Dir, ForwardsFile, &l); err != nil {
		return err
	}
	d.fwdMu.Lock()
	d.fwdRules = l
	d.fwdMu.Unlock()
	return nil
}

// ForwardsSet replaces and saves the list, asks the server for the ports
// and updates the hub DNAT rules.
func (d *Daemon) ForwardsSet(in []ForwardRule) (ForwardsInfo, error) {
	l, err := normForwards(in, d.lanPrefixes())
	if err != nil {
		return ForwardsInfo{}, err
	}
	if err := saveJSON(d.cfg.Dir, ForwardsFile, l); err != nil {
		return ForwardsInfo{}, err
	}
	d.fwdMu.Lock()
	d.fwdRules = l
	d.fwdMu.Unlock()
	d.log.Info("port forwards set", "n", len(l))
	d.pushForwards()
	d.resolveForwards(true)
	return d.ForwardsList(), nil
}

// pushForwards gives the session client the wanted list.
func (d *Daemon) pushForwards() {
	c := d.client.Load()
	if c == nil {
		return
	}
	d.fwdMu.Lock()
	want := make([]wire.Forward, 0, len(d.fwdRules))
	for _, r := range d.fwdRules {
		want = append(want, r.wire())
	}
	d.fwdMu.Unlock()
	c.SetForwards(want)
}

// resolveForwards finds the client address of every rule and, when the
// result changed (or force), applies the hub DNAT rules.
func (d *Daemon) resolveForwards(force bool) {
	d.fwdMu.Lock()
	rules := slices.Clone(d.fwdRules)
	d.fwdMu.Unlock()
	addrs := make(map[string]netip.Addr, len(rules))
	var nf []netcfg.Forward
	for _, r := range rules {
		a, ok := d.resolveTarget(r.Target)
		if !ok {
			continue
		}
		addrs[r.Target] = a
		nf = append(nf, netcfg.Forward{Proto: r.Proto, Port: r.Port, To: a})
	}
	d.fwdMu.Lock()
	d.fwdAddrs = addrs
	d.fwdMu.Unlock()
	if !d.cfg.NetSetup {
		return
	}
	err := d.applyLAN(d.ctx, func(l *netcfg.LAN) bool {
		changed := force || !slices.Equal(l.Forwards, nf)
		l.Forwards = nf
		return changed
	})
	if err != nil {
		d.log.Warn("port forward rules", "err", err)
	}
}

func (d *Daemon) resolveTarget(t string) (netip.Addr, bool) {
	if a, err := netip.ParseAddr(t); err == nil {
		return a, true
	}
	m, err := net.ParseMAC(t)
	if err != nil {
		return netip.Addr{}, false
	}
	return d.cfg.ResolveMAC(m)
}

// fwdReasons names the FWD_ACK refusal codes.
var fwdReasons = map[uint8]string{wire.FwdNotAllowed: "not_allowed", wire.FwdRestricted: "restricted",
	wire.FwdOutOfRange: "out_of_range", wire.FwdInUse: "in_use"}

// ForwardsList returns the rules with their state.
func (d *Daemon) ForwardsList() ForwardsInfo {
	d.fwdMu.Lock()
	rules, addrs := slices.Clone(d.fwdRules), d.fwdAddrs
	d.fwdMu.Unlock()
	info := ForwardsInfo{Forwards: make([]ForwardStatus, 0, len(rules))}
	d.mu.Lock()
	if d.enr != nil {
		info.Server = serverHost(d.enr.Endpoints)
	}
	d.mu.Unlock()
	var ack *wire.FwdAck
	c := d.client.Load()
	if c != nil {
		ack = c.ForwardAck()
	}
	for _, r := range rules {
		st := ForwardStatus{ForwardRule: r, State: FwdPending}
		if a, ok := addrs[r.Target]; ok {
			st.Client = a.String()
		}
		switch {
		case c == nil || !c.Up():
			st.State = FwdNotOnline
		case ack == nil:
		case slices.Contains(ack.Forwards, r.wire()):
			st.State = FwdActive
			if st.Client == "" {
				st.State = FwdNoClient
			}
		default:
			for _, x := range ack.Refused {
				if x.Forward == r.wire() {
					st.State, st.Reason = FwdRefused, fwdReasons[x.Reason]
				}
			}
		}
		info.Forwards = append(info.Forwards, st)
	}
	return info
}

// serverHost is the host of the first server endpoint (where the
// forwarded ports are reachable).
func serverHost(eps []string) string {
	for _, ep := range eps {
		if u, err := url.Parse(ep); err == nil && u.Hostname() != "" {
			return u.Hostname()
		}
	}
	return ""
}
