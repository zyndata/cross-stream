package hub

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"net/url"
	"time"

	"github.com/zyndata/cross-stream/internal/bond"
	"github.com/zyndata/cross-stream/internal/hub/netcfg"
	"github.com/zyndata/cross-stream/internal/hub/uplink"
)

// Captive portals on local uplinks (phase 19, architecture.md "Local
// uplinks on the hub"). A link is probed when its path starts and when its
// path has not been alive for CaptiveFailAfter — never while it is healthy.
// A captive link stays down; it is checked again every CaptiveRecheck for
// CaptiveGiveUp, then only on the next uplink change or a manual check.
const (
	UplinkCaptive    = "captive" // UplinkStatus.State: behind a login page
	CaptiveRecheck   = 30 * time.Second
	CaptiveGiveUp    = 10 * time.Minute
	CaptiveFailAfter = 15 * time.Second
	PortalRouteFor   = 10 * time.Minute
	// CaptiveHint is shown (xs-hubctl, English like every terminal tool) when
	// the login page cannot be opened through the uplink from the laptop; the
	// GUI says the same in its own language.
	CaptiveHint = "Log in to this network on a phone or turn the hub off for a moment"
)

// captiveInfo is the last probe of one link (guarded by Daemon.upMu).
type captiveInfo struct {
	probe   uplink.Probe
	checked time.Time
	first   time.Time // first captive result of this episode
	probing bool
	givenUp bool         // no more rechecks until the uplink changes
	iface   string       // the interface the probe used
	addr    netip.Prefix // and its address: a change resets the info
}

// CaptiveStatus is UplinkStatus.Captive.
type CaptiveStatus struct {
	uplink.Probe
	Checked    time.Time `json:"checked"`
	Rechecking bool      `json:"rechecking"` // captive and still checked every 30 s
}

// CaptiveParams are the params of uplinks.captive.
type CaptiveParams struct {
	Uplink string `json:"uplink"` // id or name
	Open   bool   `json:"open"`   // add the portal host route (10 min) for the browser
}

// CaptiveResult is the result of uplinks.captive.
type CaptiveResult struct {
	Uplink string `json:"uplink"`
	Name   string `json:"name"`
	uplink.Probe
	Route    []string `json:"route,omitempty"`     // portal addresses routed through the link
	RouteErr string   `json:"route_err,omitempty"` // why the route was not added
	Hint     string   `json:"hint,omitempty"`
}

// MethodUplinksCaptive checks a link for a captive portal now (phase 19).
const MethodUplinksCaptive = "uplinks.captive"

func (d *Daemon) probeCaptive(ctx context.Context, iface string) uplink.Probe {
	if d.cfg.ProbeCaptive != nil {
		return d.cfg.ProbeCaptive(ctx, iface)
	}
	return uplink.ProbeCaptive(ctx, d.cfg.CaptiveURL, d.cfg.UplinkControl(iface))
}

func (d *Daemon) captiveOff() bool { return d.cfg.CaptiveURL == "off" && d.cfg.ProbeCaptive == nil }

// captiveLocked reports a link held down by a captive portal.
func (d *Daemon) captiveLocked(id string) bool {
	ci := d.up.captive[id]
	return ci != nil && ci.probe.State == uplink.NetCaptive
}

// startProbeLocked probes a link in the background (upMu held).
func (d *Daemon) startProbeLocked(id string, i uplink.Iface, why string) {
	if d.captiveOff() || d.ctx == nil {
		return
	}
	ci := d.up.captive[id]
	if ci == nil {
		ci = &captiveInfo{}
		d.up.captive[id] = ci
	}
	if ci.probing {
		return
	}
	ci.probing, ci.iface, ci.addr = true, i.Name, i.Addr
	d.log.Debug("local uplink captive probe", "uplink", id, "iface", i.Name, "why", why)
	go func() {
		p := d.probeCaptive(d.ctx, i.Name)
		d.upMu.Lock()
		defer d.upMu.Unlock()
		d.applyProbeLocked(id, i, p, time.Now())
	}()
}

// applyProbeLocked records a probe result and starts or stops the path when
// the captive state changed (upMu held).
func (d *Daemon) applyProbeLocked(id string, i uplink.Iface, p uplink.Probe, now time.Time) {
	ci := d.up.captive[id]
	if ci == nil || ci.iface != i.Name || ci.addr != i.Addr {
		return // the uplink changed while probing: a new probe follows
	}
	was := ci.probe.State
	ci.probing, ci.probe, ci.checked = false, p, now
	if d.ctx != nil && d.ctx.Err() != nil {
		return
	}
	switch {
	case p.State == uplink.NetCaptive && was != uplink.NetCaptive:
		ci.first, ci.givenUp = now, false
		d.log.Info("local uplink behind a captive portal: path held down", "uplink", id, "iface", i.Name,
			"portal", portalHost(p.URL))
	case p.State != uplink.NetCaptive && was == uplink.NetCaptive:
		ci.first, ci.givenUp = time.Time{}, false
		d.log.Info("local uplink captive portal gone", "uplink", id, "iface", i.Name, "state", p.State)
	case p.State == uplink.NetNoAccess && was != uplink.NetNoAccess:
		d.log.Info("local uplink: no internet behind it", "uplink", id, "iface", i.Name, "err", p.Err)
	}
	if (was == uplink.NetCaptive) != (p.State == uplink.NetCaptive) {
		d.reconcileLocked()
	}
}

// captiveScanLocked drops the probe info of links that changed (gone,
// another interface or address): a captive link is then tried again.
func (d *Daemon) captiveScanLocked(present map[string]uplink.Iface) {
	for id, ci := range d.up.captive {
		if i, ok := present[id]; !ok || (!ci.probing && (i.Name != ci.iface || i.Addr != ci.addr)) {
			delete(d.up.captive, id)
		}
	}
}

// captiveTickLocked rechecks captive links and probes paths that do not
// come alive (upMu held; alive: path id → bond state ALIVE).
func (d *Daemon) captiveTickLocked(lps []*localPath, alive map[uint8]bool, now time.Time) {
	if d.captiveOff() {
		return
	}
	for id, ci := range d.up.captive {
		if ci.probe.State != uplink.NetCaptive || ci.givenUp || ci.probing {
			continue
		}
		i, ok := d.up.present[id]
		switch {
		case !ok:
		case now.Sub(ci.first) >= CaptiveGiveUp:
			ci.givenUp = true
			d.log.Info("local uplink still captive: no more rechecks until it changes", "uplink", id)
		case now.Sub(ci.checked) >= CaptiveRecheck:
			d.startProbeLocked(id, i, "captive recheck")
		}
	}
	for _, lp := range lps {
		if alive[lp.pid] {
			lp.badSince, lp.failProbed = time.Time{}, false
			continue
		}
		if lp.badSince.IsZero() {
			lp.badSince = now
		}
		if now.Sub(lp.badSince) >= CaptiveFailAfter && !lp.failProbed {
			if i, ok := d.up.present[lp.id]; ok {
				lp.failProbed = true
				d.startProbeLocked(lp.id, i, "path not alive")
			}
		}
	}
}

// localAlive returns the bond state of the local paths (ALIVE or not).
func (d *Daemon) localAlive(lps []*localPath) map[uint8]bool {
	alive := make(map[uint8]bool, len(lps))
	c := d.client.Load()
	if c == nil || len(lps) == 0 {
		return alive
	}
	for _, p := range c.Report().Bond.Paths {
		alive[p.ID] = p.State == bond.Alive.String()
	}
	return alive
}

// captiveStatusLocked is the probe info of a link for UplinkStatus.
func (d *Daemon) captiveStatusLocked(id string) *CaptiveStatus {
	ci := d.up.captive[id]
	if ci == nil || ci.checked.IsZero() {
		return nil
	}
	return &CaptiveStatus{Probe: ci.probe, Checked: ci.checked,
		Rechecking: ci.probe.State == uplink.NetCaptive && !ci.givenUp}
}

// UplinksCaptive probes a link now (xs-hubctl uplinks captive, GUI login
// button); with Open and a captive result it routes the portal host
// through the link for PortalRouteFor so the user's browser reaches it.
func (d *Daemon) UplinksCaptive(ctx context.Context, p CaptiveParams) (CaptiveResult, error) {
	d.upMu.Lock()
	l, ok := d.upReg.Get(p.Uplink)
	i, present := d.up.present[l.ID]
	d.upMu.Unlock()
	if !ok {
		return CaptiveResult{}, fmt.Errorf("uplinks.captive: no local link %q", p.Uplink)
	}
	if !present {
		return CaptiveResult{}, fmt.Errorf("uplinks.captive: %s (%s) is not plugged in", l.Name, l.ID)
	}
	if d.captiveOff() {
		return CaptiveResult{}, errors.New("uplinks.captive: probes are off (xs-hubd -captive-url off)")
	}
	pr := d.probeCaptive(ctx, i.Name)
	d.upMu.Lock()
	ci := d.up.captive[l.ID]
	if ci == nil {
		ci = &captiveInfo{}
		d.up.captive[l.ID] = ci
	}
	if !ci.probing {
		ci.iface, ci.addr = i.Name, i.Addr
		d.applyProbeLocked(l.ID, i, pr, time.Now())
		ci.givenUp = false // a manual check restarts the rechecks
	}
	d.upMu.Unlock()
	res := CaptiveResult{Uplink: l.ID, Name: l.Name, Probe: pr}
	if pr.State != uplink.NetCaptive {
		return res, nil
	}
	res.Hint = CaptiveHint
	if p.Open {
		res.Route, res.RouteErr = d.portalRoute(ctx, pr.URL, i)
		if res.RouteErr == "" {
			res.Hint = ""
		}
	}
	return res, nil
}

// portalRoute routes the portal host's addresses through the link for
// PortalRouteFor (each new call restarts the time).
func (d *Daemon) portalRoute(ctx context.Context, portal string, i uplink.Iface) ([]string, string) {
	host := portalHost(portal)
	if host == "" {
		return nil, "no portal host in " + portal
	}
	var ips []netip.Addr
	if a, err := netip.ParseAddr(host); err == nil {
		ips = []netip.Addr{a}
	} else {
		rctx, cancel := context.WithTimeout(ctx, uplink.ProbeTimeout)
		ips, err = net.DefaultResolver.LookupNetIP(rctx, "ip4", host)
		cancel()
		if err != nil {
			return nil, "resolve " + host + ": " + err.Error()
		}
	}
	var out []string
	for _, a := range ips[:min(len(ips), 4)] {
		r := netcfg.HostRoute{Dst: a.Unmap(), Iface: i.Name, GW: i.Gateway}
		if err := d.routePortal(ctx, r, true); err != nil {
			return out, err.Error()
		}
		out = append(out, r.Dst.String())
		d.upMu.Lock()
		if old, ok := d.up.routes[r.Dst]; ok {
			old.t.Stop()
		}
		pr := &portalRoute{r: r}
		pr.t = time.AfterFunc(PortalRouteFor, func() { d.dropPortalRoute(pr) })
		d.up.routes[r.Dst] = pr
		d.upMu.Unlock()
	}
	d.log.Info("captive portal host routed through the uplink", "iface", i.Name, "portal", host,
		"addrs", len(out), "for", PortalRouteFor)
	return out, ""
}

func (d *Daemon) routePortal(ctx context.Context, r netcfg.HostRoute, add bool) error {
	if d.cfg.PortalRoute != nil {
		return d.cfg.PortalRoute(ctx, r, add)
	}
	if add {
		return r.Add(ctx)
	}
	return r.Del(ctx)
}

// portalRoute is one routed portal address and its removal timer.
type portalRoute struct {
	r netcfg.HostRoute
	t *time.Timer
}

func (d *Daemon) dropPortalRoute(pr *portalRoute) {
	r := pr.r
	d.upMu.Lock()
	mine := d.up.routes[r.Dst] == pr
	if mine {
		delete(d.up.routes, r.Dst)
	}
	d.upMu.Unlock()
	if !mine { // replaced by a newer route (or removed at stop)
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := d.routePortal(ctx, r, false); err != nil {
		d.log.Debug("captive portal route removal", "err", err)
	}
}

// dropPortalRoutesLocked removes every portal route (daemon stop; upMu held).
func (d *Daemon) dropPortalRoutesLocked() {
	for dst, pr := range d.up.routes {
		if pr.t.Stop() {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			_ = d.routePortal(ctx, pr.r, false)
			cancel()
		}
		delete(d.up.routes, dst)
	}
}

// portalHost is the host of a login page URL (logged instead of the URL,
// whose query often holds the client's MAC and address).
func portalHost(s string) string {
	u, err := url.Parse(s)
	if err != nil {
		return ""
	}
	return u.Hostname()
}
