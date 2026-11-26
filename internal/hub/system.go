package hub

import (
	"context"
	"errors"
	"fmt"
	"math"
	"net"
	"net/netip"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/zyndata/cross-stream/internal/dns"
	"github.com/zyndata/cross-stream/internal/hub/netcfg"
)

// System integration timers.
const (
	NetCheckEvery   = 30 * time.Second // watchdog interval (plus netlink events)
	PowerCheckEvery = 10 * time.Second // hotspot clients + sleep inhibitor
	defaultMTU      = 1280             // MSS clamp before the first enrollment
	lookupWait      = 3 * time.Second  // server endpoint lookups (loop guard); the watchdog retries
)

// SleepInhibitor holds the OS sleep/lid lock (netcfg.Inhibitor).
type SleepInhibitor interface {
	Set(on bool) error
	Held() bool
}

// startHotspot loads (or creates) the hotspot settings and starts the AP; the
// hub address on the AP becomes the LAN address.
func (d *Daemon) startHotspot() error {
	host, _ := os.Hostname()
	s, err := LoadHotspotSettings(d.cfg.Dir, host)
	if err != nil {
		return err
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	st, err := d.cfg.Hotspot.Up(ctx, hotspotConfig(s, d.apIface(), d.cfg.EthIface))
	for _, w := range st.Warnings {
		d.log.Warn("hotspot", "warning", w)
	}
	if err != nil {
		d.stopHotspot()
		return err
	}
	d.hs, d.hsState = s, st
	if st.Iface != "" { // Windows: the adapter is known once it runs; Linux: maybe the virtual AP interface
		d.cfg.LanIface = st.Iface
	}
	if !d.lan.IsValid() { // tests bind to LanPrefix instead
		d.lan = netcfg.DefaultAPAddr
		if st.Addr.IsValid() { // Windows: Mobile Hotspot's subnet
			d.lan = st.Addr
		}
	}
	d.log.Info("hotspot up", "iface", st.Iface, "ssid", s.SSID, "band", st.Band, "channel", st.Channel, "width_mhz", st.Width,
		"why", st.ChannelWhy, "scan", st.Scan, "bssid", st.BSSID, "eth", st.EthIface)
	return nil
}

func (d *Daemon) stopHotspot() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	if err := d.cfg.Hotspot.Down(ctx); err != nil {
		d.log.Warn("hotspot stop", "err", err)
		return
	}
	d.log.Info("hotspot stopped")
}

// bssid is the BSSID relays should expect: the one the hotspot reports
// (Windows picks it) or else the configured cloned MAC.
func bssid(s HotspotSettings, st netcfg.HotspotState) string {
	if st.BSSID != "" {
		return strings.ToLower(st.BSSID)
	}
	return strings.ToLower(s.MAC)
}

// tunnelSharer is a hotspot that shares one connection profile picked at
// start (Windows Mobile Hotspot) and must restart to share the tunnel.
type tunnelSharer interface {
	Reshare(ctx context.Context, cfg netcfg.HotspotConfig) (netcfg.HotspotState, bool, error)
}

// reshareHotspot restarts the hotspot on the tunnel once the TUN exists
// (Windows). Relays drop off the Wi-Fi for a few seconds and come back.
func (d *Daemon) reshareHotspot() {
	ts, ok := d.cfg.Hotspot.(tunnelSharer)
	if !ok {
		return
	}
	d.hsMu.Lock()
	defer d.hsMu.Unlock()
	d.mu.Lock()
	off, s := d.hsOff, d.hs
	d.mu.Unlock()
	if off {
		return
	}
	ctx, cancel := context.WithTimeout(d.ctx, 2*time.Minute)
	defer cancel()
	st, changed, err := ts.Reshare(ctx, hotspotConfig(s, d.apIface(), d.cfg.EthIface))
	if err != nil {
		d.log.Warn("hotspot: share the tunnel", "err", err)
		d.mu.Lock()
		d.hsErr = err.Error()
		d.mu.Unlock()
		return
	}
	if !changed {
		return
	}
	d.mu.Lock()
	d.hsState, d.hsErr = st, ""
	d.mu.Unlock()
	for _, w := range st.Warnings {
		d.log.Warn("hotspot", "warning", w)
	}
	d.log.Info("hotspot restarted to share the tunnel", "shared", st.Shared, "iface", st.Iface)
}

// ports are the networks the hub serves clients on.
func (d *Daemon) ports() []netcfg.Port {
	ps := []netcfg.Port{{Iface: d.cfg.LanIface, Addr: d.lan}}
	if d.cfg.Hotspot != nil && d.cfg.EthIface != "" {
		ps = append(ps, netcfg.Port{Iface: d.cfg.EthIface, Addr: netcfg.DefaultEthAddr})
	}
	return ps
}

// setupNet installs policy routing, nft rules and the watchdog for the
// daemon's lifetime.
func (d *Daemon) setupNet(ctx context.Context) error {
	lan := netcfg.LAN{Ports: d.ports(), TunName: d.cfg.TunName, MTU: defaultMTU}
	d.mu.Lock()
	e := d.enr
	mode, _ := d.modes.Mode()
	d.mu.Unlock()
	lan.NoPath = !mode.carries()
	lan.Forward = d.cfg.HotspotNAT.IsValid()
	lan.OwnTraffic = d.settings.Load().OwnTraffic
	// Windows: WFP leak filters (nil elsewhere).
	lan.Leaks = netcfg.NewLeakGuard()
	if e != nil { // resolved outside mu: the lookup can block for seconds
		lan.MTU, lan.Blocked, lan.TunAddr = e.MTU, serverIPs(ctx, e.Endpoints), tunnelAddr(*e)
	}
	if lan.OwnTraffic && !netcfg.OwnTrafficAlways {
		d.checkOwnDNS()
	}
	d.mu.Lock()
	d.netLAN = lan
	d.mu.Unlock()
	if err := lan.Setup(ctx); err != nil {
		return err
	}
	d.ensureNet(ctx)
	go netcfg.Watch(ctx, NetCheckEvery, d.log, func() { d.ensureNet(ctx) })
	return nil
}

// ensureNet repairs missing routes, rules or nft rules (watchdog). ensMu: two
// callers at once both saw a rule missing and the second "ip rule add" failed
// with "File exists" (field test 2026-11-25, re-enrollment).
func (d *Daemon) ensureNet(ctx context.Context) {
	d.ensMu.Lock()
	defer d.ensMu.Unlock()
	d.mu.Lock()
	lan, e := d.netLAN, d.enr
	d.mu.Unlock()
	if e != nil && len(lan.Blocked) == 0 && netcfg.LoopGuard { // the lookup failed earlier (no DNS yet): retry
		if ips := serverIPs(ctx, e.Endpoints); len(ips) > 0 {
			if err := d.applyLAN(ctx, func(l *netcfg.LAN) bool { l.Blocked = ips; return true }); err != nil {
				d.log.Warn("nft rules", "err", err)
			}
			d.mu.Lock()
			lan = d.netLAN
			d.mu.Unlock()
		}
	}
	fixed, err := lan.Ensure(ctx)
	if len(fixed) > 0 {
		d.log.Info("network config re-applied", "fixed", strings.Join(fixed, "; "))
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	d.mu.Lock()
	changed := msg != d.netErr
	d.netErr = msg
	d.mu.Unlock()
	if changed && err != nil && ctx.Err() == nil {
		d.log.Warn("network config", "err", err)
	}
}

// netPathState tells the network config whether any path carries traffic
// (Windows: TUN routes only then) and applies it at once.
func (d *Daemon) netPathState(mode Mode) {
	if !d.cfg.NetSetup {
		return
	}
	d.mu.Lock()
	changed := d.netLAN.NoPath == mode.carries()
	d.netLAN.NoPath = !mode.carries()
	d.mu.Unlock()
	if changed {
		d.ensureNet(d.ctx)
	}
}

func (d *Daemon) cleanupNet() {
	d.mu.Lock()
	lan := d.netLAN
	d.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	if err := lan.Cleanup(ctx); err != nil {
		d.log.Warn("network cleanup", "err", err)
	}
}

// netTunnel updates the MSS clamp and loop guard for a (new) enrollment and
// adds the tunnel default route once the TUN exists.
func (d *Daemon) netTunnel(e Enrollment) {
	if !d.cfg.NetSetup {
		return
	}
	ips := serverIPs(d.ctx, e.Endpoints) // outside mu: the lookup can block for seconds
	ip := tunnelAddr(e)
	if err := d.applyLAN(d.ctx, func(l *netcfg.LAN) bool { l.MTU, l.Blocked, l.TunAddr = e.MTU, ips, ip; return true }); err != nil {
		d.log.Warn("nft rules", "err", err)
	}
	d.serveTunnelDNS(ip)
	d.ensureNet(d.ctx)
}

// applyLAN changes the LAN rule set and applies it. Changes come from
// several goroutines (MTU, forwards, TTL, enrollment): nftMu keeps
// "snapshot, then nft" atomic, so an older snapshot is never applied last.
// change reports whether anything changed (false = nothing is applied).
func (d *Daemon) applyLAN(ctx context.Context, change func(*netcfg.LAN) bool) error {
	d.nftMu.Lock()
	defer d.nftMu.Unlock()
	d.mu.Lock()
	changed := change(&d.netLAN)
	lan := d.netLAN
	d.mu.Unlock()
	if !changed {
		return nil
	}
	return lan.ApplyRules(ctx)
}

// serverIPs resolves the server endpoints for the loop guard (best effort,
// each host once, at most lookupWait in all). Without the loop guard
// (Windows) it resolves nothing: before the tunnel is up the hub often has
// no DNS, and the lookups held back the IPC socket for 12-24 s.
func serverIPs(ctx context.Context, eps []string) []netip.Addr {
	if !netcfg.LoopGuard {
		return nil
	}
	ctx, cancel := context.WithTimeout(ctx, lookupWait)
	defer cancel()
	var ips []netip.Addr
	seen := map[string]bool{}
	for _, ep := range eps {
		u, err := url.Parse(ep)
		if err != nil || u.Hostname() == "" || seen[u.Hostname()] {
			continue
		}
		host := u.Hostname()
		seen[host] = true
		if ip, err := netip.ParseAddr(host); err == nil {
			if ip.Is4() && !containsAddr(ips, ip) {
				ips = append(ips, ip)
			}
			continue
		}
		addrs, err := net.DefaultResolver.LookupHost(ctx, host)
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ip, err := netip.ParseAddr(a); err == nil && ip.Is4() && !containsAddr(ips, ip) {
				ips = append(ips, ip)
			}
		}
	}
	return ips
}

func containsAddr(ips []netip.Addr, ip netip.Addr) bool {
	for _, x := range ips {
		if x == ip {
			return true
		}
	}
	return false
}

// startDNS serves DNS on every port address (and, once enrolled, on the
// tunnel address for the laptop's own lookups), forwarded through the tunnel.
func (d *Daemon) startDNS(ctx context.Context) {
	var listen []netip.AddrPort
	for _, p := range d.ports() {
		listen = append(listen, netip.AddrPortFrom(p.Addr.Addr(), 53))
	}
	srv := dns.New(dns.Config{Listen: listen, Upstreams: d.cfg.DNSUpstreams, Dial: netcfg.DialVia(d.cfg.TunName), Log: d.log,
		OnAnswer: d.learnBypass, Canary: d.bp.Active, Control: netcfg.Control(nil)})
	d.mu.Lock()
	d.dnsSrv = srv
	e := d.enr
	d.mu.Unlock()
	go srv.Run(ctx)
	if e != nil {
		d.serveTunnelDNS(tunnelAddr(*e))
	}
}

// tunnelAddr is the enrollment's tunnel address (invalid while pending).
func tunnelAddr(e Enrollment) netip.Addr {
	ip, _ := netip.ParseAddr(e.TunnelIP)
	return ip
}

// systemLoop updates the mode every second and, every PowerCheckEvery, the
// hotspot client count and the sleep inhibitor.
func (d *Daemon) systemLoop(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var lastPower, lastFwd time.Time
	lastUsage := time.Now()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			d.updateMode(now)
			d.uplinkTick(now)
			d.bypassTick(now)
			d.eventTick(d.Status())
			if now.Sub(lastUsage) >= UsageSaveEvery {
				lastUsage = now
				d.saveUsage(now)
			}
			if now.Sub(lastFwd) >= FwdResolveGap {
				lastFwd = now
				d.resolveForwards(false)
			}
			if now.Sub(lastPower) >= PowerCheckEvery {
				lastPower = now
				d.updatePower(ctx)
			}
		}
	}
}

func (d *Daemon) updateMode(now time.Time) {
	in := ModeInputs{Silence: time.Duration(math.MaxInt64)}
	if c := d.client.Load(); c != nil {
		in.Session = c.Up()
		if last := c.LastRecv(); !last.IsZero() {
			in.Silence = now.Sub(last)
		}
	}
	d.mu.Lock()
	in.PathsAlive = len(d.relays) + d.localsUp()
	d.modes.DirectEnabled = d.dstack != nil
	prev, _ := d.modes.Mode()
	mode, changed := d.modes.Update(now, in)
	d.mu.Unlock()
	if changed {
		d.log.Info("hub mode", "mode", mode, "from", prev, "relays", in.PathsAlive, "tunnel_up", in.TunnelUp())
		d.applyMode(now, prev, mode)
		d.netPathState(mode)
		d.emit(Event{Type: EvModeChanged, At: now, Mode: mode, Reason: string(prev)})
	}
	d.drainTick(now)
}

func (d *Daemon) updatePower(ctx context.Context) {
	clients := 0
	if d.cfg.Hotspot != nil {
		n, err := d.cfg.Hotspot.Stations(ctx, d.cfg.LanIface)
		switch {
		case errors.Is(err, netcfg.ErrHotspotRestarted):
			d.log.Warn("hotspot", "event", err)
		case err != nil:
			d.log.Debug("hotspot stations", "err", err)
		}
		clients = n
	}
	d.mu.Lock()
	d.clients = clients
	relays := len(d.relays)
	was := d.inhibit
	d.mu.Unlock()
	if d.cfg.Inhibitor == nil {
		return
	}
	want := clients+relays > 0
	if err := d.cfg.Inhibitor.Set(want); err != nil {
		d.log.Warn("sleep inhibitor", "err", err)
	}
	held := d.cfg.Inhibitor.Held()
	d.mu.Lock()
	d.inhibit = held
	d.mu.Unlock()
	if held != was {
		d.log.Info("sleep and lid switch inhibited", "on", held, "clients", clients, "relays", relays)
	}
}

// HotspotGet returns the hotspot settings and state.
func (d *Daemon) HotspotGet() HotspotInfo {
	d.mu.Lock()
	defer d.mu.Unlock()
	return HotspotInfo{Enabled: d.cfg.Hotspot != nil, On: d.cfg.Hotspot != nil && !d.hsOff, Settings: d.hs,
		State: d.hsState, Clients: d.clients, Error: d.hsErr}
}

// HotspotSet changes the hotspot settings, saves them and restarts the AP
// with them. If the AP does not start, the previous settings are restored.
// Relays must be paired again when SSID or PSK change (new pairing code).
func (d *Daemon) HotspotSet(ctx context.Context, p HotspotParams) (HotspotInfo, error) {
	if d.cfg.Hotspot == nil {
		return HotspotInfo{}, errors.New("hotspot.set: xs-hubd runs headless (no hotspot)")
	}
	d.hsMu.Lock()
	defer d.hsMu.Unlock()
	d.mu.Lock()
	old := d.hs
	d.mu.Unlock()
	next := p.apply(old)
	if next == old {
		return d.HotspotGet(), nil
	}
	cfg := hotspotConfig(next, d.apIface(), d.cfg.EthIface)
	if err := netcfg.ValidateHotspot(cfg); err != nil {
		return HotspotInfo{}, err
	}
	if !netcfg.ValidClientWifi(next.ClientWifi) {
		return HotspotInfo{}, fmt.Errorf("hotspot.set: client_wifi %q: want share, disconnect or empty (ask)", next.ClientWifi)
	}
	if err := SaveHotspotSettings(d.cfg.Dir, next); err != nil {
		return HotspotInfo{}, err
	}
	d.mu.Lock()
	off := d.hsOff
	d.mu.Unlock()
	if off { // stopped: the new settings apply on the next start
		d.mu.Lock()
		d.hs = next
		d.mu.Unlock()
		if err := d.issuer.SetWiFi(next.SSID, bssid(next, netcfg.HotspotState{}), next.PSK); err != nil {
			return HotspotInfo{}, err
		}
		return d.HotspotGet(), nil
	}
	st, err := d.cfg.Hotspot.Up(ctx, cfg)
	if err != nil {
		rerr := SaveHotspotSettings(d.cfg.Dir, old)
		st2, uerr := d.cfg.Hotspot.Up(ctx, hotspotConfig(old, d.apIface(), d.cfg.EthIface))
		d.mu.Lock()
		d.hsState, d.hsErr = st2, ""
		if uerr != nil {
			d.hsErr = uerr.Error()
		}
		d.mu.Unlock()
		return HotspotInfo{}, fmt.Errorf("hotspot.set: %w (previous settings restored)", errors.Join(err, rerr, uerr))
	}
	d.mu.Lock()
	d.hs, d.hsState, d.hsErr = next, st, ""
	d.mu.Unlock()
	d.restartIfMoved(st)
	if err := d.issuer.SetWiFi(next.SSID, bssid(next, st), next.PSK); err != nil {
		return HotspotInfo{}, err
	}
	for _, w := range st.Warnings {
		d.log.Warn("hotspot", "warning", w)
	}
	d.log.Info("hotspot settings changed", "ssid", next.SSID, "band", st.Band, "channel", st.Channel)
	return d.HotspotGet(), nil
}
