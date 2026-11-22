package hub

import (
	"crypto/rand"
	"net/netip"
	"time"

	"github.com/zyndata/cross-stream/internal/bypass"
	"github.com/zyndata/cross-stream/internal/direct"
	"github.com/zyndata/cross-stream/internal/wire"
)

// BypassFile holds the bypass list (architecture.md "Bypass list").
const BypassFile = "bypass.json"

// BypassInfo is the result of bypass.get / bypass.set.
type BypassInfo struct {
	bypass.Rules
	Learned int `json:"learned"` // addresses learned from DNS answers, still valid
	Relays  int `json:"relays"`  // relays that can carry bypass traffic now
}

// loadBypass reads bypass.json into the set.
func (d *Daemon) loadBypass() error {
	var r bypass.Rules
	if _, err := loadJSON(d.cfg.Dir, BypassFile, &r); err != nil {
		return err
	}
	r, err := r.Normalize()
	if err != nil {
		return err
	}
	d.bpRules.Store(&r)
	d.bp.SetRules(r)
	return nil
}

// BypassGet returns the list and its state.
func (d *Daemon) BypassGet() BypassInfo {
	info := BypassInfo{Learned: d.bp.Learned(), Relays: len(d.directRelays())}
	if r := d.bpRules.Load(); r != nil {
		info.Rules = *r
	} else {
		info.Rules = bypass.Rules{Domains: []string{}, CIDRs: []string{}}
	}
	return info
}

// BypassSet replaces and saves the list and turns the relays' proxy role
// on or off.
func (d *Daemon) BypassSet(r bypass.Rules) (BypassInfo, error) {
	r, err := r.Normalize()
	if err != nil {
		return BypassInfo{}, err
	}
	if err := saveJSON(d.cfg.Dir, BypassFile, r); err != nil {
		return BypassInfo{}, err
	}
	d.bpRules.Store(&r)
	d.bp.SetRules(r)
	d.log.Info("bypass list set", "domains", len(r.Domains), "cidrs", len(r.CIDRs))
	d.bypassTick(time.Now())
	return d.BypassGet(), nil
}

// learnBypass is the DNS forwarder hook.
func (d *Daemon) learnBypass(name string, ips []netip.Addr, ttl time.Duration) {
	if d.bp.Learn(name, ips, ttl, time.Now()) {
		d.log.Debug("bypass addresses", "name", name, "ips", ips)
	}
}

// bypassTick (every second and on list changes) expires learned addresses,
// keeps the proxy role in line with the list and notes whether a relay can
// carry bypass traffic (logged once when none can).
func (d *Daemon) bypassTick(now time.Time) {
	d.bp.Expire(now)
	active := d.bp.Active()
	ready := active && len(d.directRelays()) > 0
	if was := d.bpReady.Swap(ready); active && !ready && (was || !d.bpWarned.Load()) {
		d.bpWarned.Store(true)
		d.log.Warn("bypass: no relay can proxy, bypassed traffic goes through the tunnel")
	}
	if ready {
		d.bpWarned.Store(false)
	}
	switch {
	case active && d.proxyTok.Load() == nil:
		var t [wire.ProxyTokenLen]byte
		_, _ = rand.Read(t[:])
		d.proxyTok.Store(&t)
		d.sendModeAll()
		d.log.Info("bypass on: relays run the proxy role")
	case !active && d.proxyTok.Load() != nil && d.route.Load() == routeTunnel:
		d.endProxy("bypass list empty")
	}
}

// bypassPacket reports whether a packet from the TUN goes to the netstack
// because of the bypass list: its destination is bypassed (or it belongs to
// an open bypass flow) and a relay can proxy it.
func (d *Daemon) bypassPacket(ds *direct.Stack, pkt []byte) bool {
	if !d.bp.Active() || !d.bpReady.Load() || len(pkt) < 20 || pkt[0]>>4 != 4 {
		return false
	}
	return d.bp.Contains(netip.AddrFrom4([4]byte(pkt[16:20]))) || ds.Owns(pkt)
}
