package hub

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"sort"
	"strings"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/hub/netcfg"
	"github.com/zyndata/cross-stream/internal/pairing"
	"github.com/zyndata/cross-stream/internal/tun"
	"github.com/zyndata/cross-stream/internal/wire"
)

// EndpointFallbackAfter is how long a path stays DEAD, while another is ALIVE,
// before its relay moves to the next server endpoint (protocol.md §3); 3 ×
// that while no path is ALIVE.
const EndpointFallbackAfter = 5 * time.Second

// priority ranks: a relay's max_priority can only lower the owner's choice.
var prioRank = map[string]int{"": 0, control.PriorityNormal: 0, control.PrioritySecondary: 1, control.PriorityBackup: 2}

var rankWire = [...]uint8{wire.PriorityNormal, wire.PrioritySecondary, wire.PriorityBackup}

var rankName = [...]string{control.PriorityNormal, control.PrioritySecondary, control.PriorityBackup}

// effectivePriority is the more restrictive of the owner's priority and the
// relay's max_priority (architecture.md "Friend-side priority cap").
func effectivePriority(rec pairing.Relay) string {
	return rankName[max(prioRank[rec.Path.Priority], prioRank[rec.Limits.MaxPriority])]
}

var dirWire = map[string]uint8{"": wire.DirOn, pairing.DirOn: wire.DirOn, pairing.DirBackupOnly: wire.DirBackupOnly,
	pairing.DirOff: wire.DirOff}

// pathConfig is the PATH_CONFIG for a relay: the owner's settings, with the
// priority lowered to the relay's max_priority.
func pathConfig(rec pairing.Relay) wire.PathConfig {
	r := max(prioRank[rec.Path.Priority], prioRank[rec.Limits.MaxPriority])
	return wire.PathConfig{PathID: rec.PathID, Priority: rankWire[r], RateKbps: rec.Path.RateKbps,
		CapMB: rec.Path.CapMB, DirUp: dirWire[rec.Path.Up], DirDown: dirWire[rec.Path.Down]}
}

// addPath makes a connected relay with a known data address a tunnel path.
func (d *Daemon) addPath(rc *relayPath) {
	c := d.client.Load()
	a := rc.data.Load()
	if c == nil || a == nil {
		return
	}
	rec := rc.rec
	if r, ok := d.registryRelay(rec.ID); ok {
		rec = r
	}
	c.AddPath(rec.PathID, *a, rc, pathConfig(rec))
}

// pathName is a path's display name for the server panel: the relay's
// name, or the local link's name (its id when unnamed).
func (d *Daemon) pathName(pathID uint8) string {
	for _, r := range d.reg.List() {
		if r.PathID == pathID {
			return r.Name
		}
	}
	if d.upReg != nil {
		for _, l := range d.upReg.List() {
			if l.PathID == pathID {
				if l.Name != "" {
					return l.Name
				}
				return l.ID
			}
		}
	}
	return ""
}

func (d *Daemon) registryRelay(id string) (pairing.Relay, bool) {
	for _, r := range d.reg.List() {
		if r.ID == id {
			return r, true
		}
	}
	return pairing.Relay{}, false
}

// applyPathConfig pushes a relay's current settings to the tunnel client.
func (d *Daemon) applyPathConfig(id string) {
	rec, ok := d.registryRelay(id)
	c := d.client.Load()
	if !ok || c == nil {
		return
	}
	c.SetPathConfig(rec.PathID, pathConfig(rec))
}

// SetPath changes a relay's path settings (priority, rate, cap, directions).
func (d *Daemon) SetPath(idOrName string, p PathParams) (pairing.Relay, error) {
	var cur pairing.PathSettings
	for _, r := range d.reg.List() {
		if r.ID == idOrName || r.Name == idOrName {
			cur = r.Path
			break
		}
	}
	p.apply(&cur)
	rec, err := d.reg.SetPath(idOrName, cur)
	if err != nil {
		return pairing.Relay{}, err
	}
	d.applyPathConfig(rec.ID)
	d.log.Info("relay path settings", "relay", rec.ID, "priority", cur.Priority, "rate_kbps", cur.RateKbps,
		"cap_mb", cur.CapMB, "up", cur.Up, "down", cur.Down)
	return rec, nil
}

// tickLoop runs once a second: endpoint fallback and tunnel MTU.
func (d *Daemon) tickLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	mtu := 0
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			c := d.client.Load()
			if c == nil {
				continue
			}
			rep := c.Report()
			d.endpointFallback(now, rep.Bond)
			if !rep.Up {
				continue
			}
			if rep.TunnelMTU != mtu {
				mtu = d.applyMTU(ctx, mtu, rep.TunnelMTU)
			}
		}
	}
}

// applyMTU sets the TUN MTU and the MSS clamp (only with NetSetup).
func (d *Daemon) applyMTU(ctx context.Context, old, mtu int) int {
	if !d.cfg.NetSetup {
		return mtu
	}
	if err := tun.SetMTU(d.cfg.TunName, mtu); err != nil {
		d.log.Warn("set tunnel MTU", "mtu", mtu, "err", err)
		return old
	}
	d.mss.Store(int32(mtu - 40))
	if err := d.applyLAN(ctx, func(l *netcfg.LAN) bool { l.MTU = mtu; return true }); err != nil {
		d.log.Warn("MSS clamp", "mtu", mtu, "err", err)
	}
	d.log.Info("tunnel MTU follows the smallest path", "mtu", mtu)
	return mtu
}

// sendEndpoint tells a relay its (new) server endpoint and marks its path as
// a stream path or not in the bond engine. rc.ep is read under d.mu.
func (d *Daemon) sendEndpoint(rc *relayPath) {
	d.mu.Lock()
	ep := rc.ep
	d.mu.Unlock()
	d.log.Info("relay server endpoint", "relay", rc.rec.ID, "endpoint", ep)
	if c := d.client.Load(); c != nil {
		c.SetPathStream(rc.rec.PathID, rc.Stream())
	}
	if err := rc.ctl.Send(&control.Endpoint{ServerEP: ep, TCPConns: rc.tcpConns()}); err != nil {
		d.log.Debug("send endpoint", "relay", rc.rec.ID, "err", err)
	}
}

// sendConns tells a relay a new number of TCP connections for its endpoint
// (the relay resizes its connection set without a redial).
func (d *Daemon) sendConns(rc *relayPath, n int, m *control.Stats) {
	d.mu.Lock()
	ep := rc.ep
	d.mu.Unlock()
	d.log.Info("relay tcp connections", "relay", rc.rec.ID, "tcp_conns", n, "retrans", m.TCPRetrans, "sent", m.TCPSent)
	if err := rc.ctl.Send(&control.Endpoint{ServerEP: ep, TCPConns: n}); err != nil {
		d.log.Debug("send endpoint", "relay", rc.rec.ID, "err", err)
	}
}

// orderedEndpoints returns the valid endpoint URIs in fallback order: UDP,
// then TCP, then WSS (the code's order within a transport).
func orderedEndpoints(eps []string) []string {
	type ep struct {
		uri  string
		rank int
	}
	var all []ep
	for _, s := range eps {
		if e, err := wire.ParseEndpoint(s); err == nil {
			all = append(all, ep{e.String(), e.Transport.Rank()})
		}
	}
	slices.SortStableFunc(all, func(a, b ep) int { return a.rank - b.rank })
	out := make([]string, len(all))
	for i, e := range all {
		out[i] = e.uri
	}
	return out
}

// transportOf returns the transport name of an endpoint URI ("" if none).
func transportOf(uri string) string {
	if e, err := wire.ParseEndpoint(uri); err == nil {
		return string(e.Transport)
	}
	return ""
}

// publicIP returns the address of an observed "ip:port", or "" when it says
// nothing about the uplink: a private or loopback source means the server
// sits behind a NAT or proxy that hides the real one (Docker Desktop shows
// every path as its bridge gateway).
func publicIP(obs string) string {
	ap, err := netip.ParseAddrPort(obs)
	if err != nil {
		return ""
	}
	a := ap.Addr().Unmap()
	if a.IsPrivate() || a.IsLoopback() || a.IsLinkLocalUnicast() {
		return ""
	}
	return a.String()
}

// sharedIPWarnings lists paths (relays and local links) that reach the
// server from the same public IP (same uplink: bonding them gains nothing)
// and marks such local links with SameIPAs (GUI: "turn Wi-Fi off on the
// phone" for a USB-tethered phone on the same Wi-Fi).
func sharedIPWarnings(relays []RelayStatus, uplinks []UplinkStatus) []string {
	byIP := make(map[string][]string)
	add := func(name, obs string) {
		if ip := publicIP(obs); ip != "" {
			byIP[ip] = append(byIP[ip], name)
		}
	}
	for _, r := range relays {
		add(r.Name, r.Observed)
	}
	for _, u := range uplinks {
		add(u.Name, u.Observed)
	}
	for i := range uplinks {
		u := &uplinks[i]
		ip := publicIP(u.Observed)
		if ip == "" {
			continue
		}
		for _, n := range byIP[ip] {
			if n != u.Name {
				u.SameIPAs = n
				break
			}
		}
	}
	var out []string
	for ip, names := range byIP {
		if len(names) > 1 {
			kind := "relays"
			if len(uplinks) > 0 {
				kind = "paths"
			}
			out = append(out, fmt.Sprintf("%s %s share public IP %s (same uplink, no gain)", kind, strings.Join(names, ", "), ip))
		}
	}
	sort.Strings(out)
	return out
}
