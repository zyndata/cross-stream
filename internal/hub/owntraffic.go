package hub

import (
	"context"
	"net/netip"
	"time"

	"github.com/zyndata/cross-stream/internal/hub/netcfg"
)

// Own traffic (phase 21): the hub laptop's own apps use the bond too. On
// Linux it is a setting (netcfg.LAN.OwnTraffic, rules only while a path
// carries traffic); on Windows it is always on (netcfg.OwnTrafficAlways).

// Values of Status.OwnDNS.
const (
	OwnDNSResolved = "resolved" // systemd-resolved sends every lookup to the hub DNS
	OwnDNSSystem   = "system"   // no resolved: DNS stays as the system has it
)

// setOwnTraffic applies the own-traffic setting to the network config.
func (d *Daemon) setOwnTraffic(on bool) {
	if !d.cfg.NetSetup || netcfg.OwnTrafficAlways {
		return
	}
	d.mu.Lock()
	changed := d.netLAN.OwnTraffic != on
	d.netLAN.OwnTraffic = on
	ctx := d.ctx
	d.mu.Unlock()
	if on {
		d.checkOwnDNS()
	}
	if changed && ctx != nil {
		d.ensureNet(ctx) // rules, the IPv6 reject table and resolved follow
		d.log.Info("own traffic through CrossStream", "on", on)
	}
}

// checkOwnDNS records whether systemd-resolved can be pointed at the hub DNS.
func (d *Daemon) checkOwnDNS() {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v := OwnDNSSystem
	if netcfg.ResolvedAvailable(ctx, nil) {
		v = OwnDNSResolved
	} else {
		d.log.Warn("own traffic: systemd-resolved not found, the laptop's DNS stays as it is")
	}
	d.ownDNS.Store(&v)
}

// ownTrafficStatus fills the own-traffic part of Status.
func (d *Daemon) ownTrafficStatus(st *Status, set *Settings) {
	st.OwnTraffic = set.OwnTraffic || netcfg.OwnTrafficAlways
	if st.OwnTraffic && !netcfg.OwnTrafficAlways {
		if v := d.ownDNS.Load(); v != nil {
			st.OwnDNS = *v
		}
	}
}

// serveTunnelDNS makes the hub DNS listen on the tunnel address too (Linux:
// resolved is pointed there while own traffic is on, docs/decisions.md).
func (d *Daemon) serveTunnelDNS(ip netip.Addr) {
	if netcfg.OwnTrafficAlways || !ip.IsValid() {
		return
	}
	d.mu.Lock()
	srv, ctx := d.dnsSrv, d.ctx
	have := d.dnsTun == ip
	if srv != nil && !have {
		d.dnsTun = ip
	}
	d.mu.Unlock()
	if srv == nil || have || ctx == nil {
		return
	}
	go srv.Serve(ctx, netip.AddrPortFrom(ip, 53))
}
