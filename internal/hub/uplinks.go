package hub

import (
	"context"
	"net/netip"
	"slices"
	"time"

	"github.com/zyndata/cross-stream/internal/bond"
	"github.com/zyndata/cross-stream/internal/datacap"
	"github.com/zyndata/cross-stream/internal/direct"
	"github.com/zyndata/cross-stream/internal/hub/netcfg"
	"github.com/zyndata/cross-stream/internal/hub/uplink"
	"github.com/zyndata/cross-stream/internal/pairing"
	"github.com/zyndata/cross-stream/internal/wire"
)

// Local uplink timing.
const (
	// UplinkScanEvery: interface scan period (netlink changes also trigger
	// a scan, debounced by 1 s).
	UplinkScanEvery = 10 * time.Second
	// ModemCheckEvery: how often ModemManager is asked about signal.
	ModemCheckEvery = 10 * time.Second
)

// Why a known link does not run (UplinkStatus.State).
const (
	UplinkOff        = "off"        // disabled by the owner
	UplinkAbsent     = "absent"     // interface not present or not qualified
	UplinkLimit      = "limit"      // data cap reached (stop mode)
	UplinkConnecting = "connecting" // running, no server connection yet
	UplinkUp         = "up"
)

// UplinkStatus is one known local uplink in Status and uplinks.list.
type UplinkStatus struct {
	uplink.Link
	State     string          `json:"state"`
	Present   bool            `json:"present"`
	Addr      string          `json:"addr,omitempty"` // interface IPv4/prefix
	Driver    string          `json:"driver,omitempty"`
	Since     time.Time       `json:"since,omitzero"`
	Endpoint  string          `json:"endpoint,omitempty"`
	Transport string          `json:"transport,omitempty"`
	Observed  string          `json:"observed,omitempty"` // public address the server sees
	UsedNow   uint64          `json:"used_period_bytes"`  // bytes in the current cap period
	Period    time.Time       `json:"period_start,omitzero"`
	Bond      *bond.PathStats `json:"bond,omitempty"`
	Modem     *uplink.Modem   `json:"modem,omitempty"`
	// DirectFlows are the open DIRECT flows through this link.
	DirectFlows int `json:"direct_flows"`
	// SameIPAs names another path with the same public IP (a phone on USB
	// tethering that still uses Wi-Fi tethers that Wi-Fi, not cellular).
	SameIPAs string `json:"same_ip_as,omitempty"`
	// Captive is the last captive portal probe (phase 19); State is
	// "captive" while it found a login page.
	Captive *CaptiveStatus `json:"captive,omitempty"`
	// TCPConns is the live number of TCP connections of a tcp:// path.
	TCPConns int `json:"tcp_conns,omitempty"`
}

// uplinkState is the manager's state (guarded by Daemon.upMu).
type uplinkState struct {
	present map[string]uplink.Iface // link id → interface
	meters  map[string]*datacap.Meter
	rpOld   map[string]int // interface → rp_filter before the hub changed it
	modems  map[string]uplink.Modem
	ttl     []string
	scanErr string
	scanned bool                        // a scan has run: later appearances are logged as "back"
	lastMM  time.Time                   // uplinkScan only (one goroutine): read without upMu
	captive map[string]*captiveInfo     // link id → last captive portal probe
	routes  map[netip.Addr]*portalRoute // portal hosts routed through a link
}

// openUplinks loads the registry of local links.
func (d *Daemon) openUplinks() error {
	r, err := uplink.Open(d.cfg.Dir)
	if err != nil {
		return err
	}
	d.upReg = r
	d.up = uplinkState{present: map[string]uplink.Iface{}, meters: map[string]*datacap.Meter{},
		rpOld: map[string]int{}, modems: map[string]uplink.Modem{}, captive: map[string]*captiveInfo{},
		routes: map[netip.Addr]*portalRoute{}}
	d.locals = make(map[string]*localPath)
	return nil
}

// defaultScan lists qualifying interfaces except the hub's own LAN,
// Ethernet share port and tunnel.
func (d *Daemon) defaultScan() ([]uplink.Iface, error) {
	ex := []string{d.cfg.LanIface, d.cfg.TunName}
	if d.wifi != "" && d.wifi != d.cfg.LanIface { // AP next to a Wi-Fi client: the card stays out too
		ex = append(ex, d.wifi)
	}
	if d.cfg.EthIface != "" {
		ex = append(ex, d.cfg.EthIface)
	}
	return netcfg.ScanUplinks(ex)
}

// localPaths returns the running local paths sorted by path id. Caller
// holds mu.
func (d *Daemon) localPaths() []*localPath {
	out := make([]*localPath, 0, len(d.locals))
	for _, lp := range d.locals {
		out = append(out, lp)
	}
	slices.SortFunc(out, func(a, b *localPath) int { return int(a.pid) - int(b.pid) })
	return out
}

// uplinkLoop scans the interfaces until ctx ends.
func (d *Daemon) uplinkLoop(ctx context.Context) {
	d.uplinkScan(ctx)
	netcfg.Watch(ctx, UplinkScanEvery, d.log, func() { d.uplinkScan(ctx) })
	d.upMu.Lock()
	defer d.upMu.Unlock()
	d.mu.Lock()
	lps := d.localPaths()
	d.mu.Unlock()
	for _, lp := range lps {
		d.stopLocalLocked(lp)
	}
	d.dropPortalRoutesLocked()
}

// uplinkScan finds the present interfaces, records new links and starts or
// stops local paths.
func (d *Daemon) uplinkScan(ctx context.Context) {
	ifs, err := d.cfg.ScanUplinks()
	now := time.Now()
	var modems map[string]uplink.Modem // asked outside upMu: mmcli may be slow
	if slices.ContainsFunc(ifs, func(i uplink.Iface) bool { return i.Kind == uplink.KindModem }) &&
		d.cfg.ModemInfo != nil && now.Sub(d.up.lastMM) >= ModemCheckEvery {
		d.up.lastMM = now
		mctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		modems = d.cfg.ModemInfo(mctx)
		cancel()
	}
	d.upMu.Lock()
	defer d.upMu.Unlock()
	if err != nil {
		if err.Error() != d.up.scanErr {
			d.log.Warn("local uplinks: scan", "err", err)
		}
		d.up.scanErr = err.Error()
		return
	}
	d.up.scanErr = ""
	present := make(map[string]uplink.Iface, len(ifs))
	for _, i := range ifs {
		l, isNew, err := d.upReg.Seen(i, now)
		if err != nil {
			d.log.Warn("local uplink registry", "iface", i.Name, "err", err)
			continue
		}
		if isNew {
			d.log.Info("local uplink found", "uplink", l.ID, "iface", i.Name, "kind", i.Kind, "enabled", l.Enabled)
		} else if _, was := d.up.present[l.ID]; !was && d.up.scanned {
			d.log.Info("local uplink back", "uplink", l.ID, "iface", i.Name)
		}
		present[l.ID] = i
	}
	for id, i := range d.up.present {
		if _, ok := present[id]; !ok {
			d.log.Info("local uplink gone", "uplink", id, "iface", i.Name)
		}
	}
	d.captiveScanLocked(present)
	d.up.present, d.up.scanned = present, true
	if modems != nil {
		d.up.modems = modems
	}
	d.reconcileLocked()
}

// meterLocked returns a link's data meter, created from its saved count.
func (d *Daemon) meterLocked(l uplink.Link) *datacap.Meter {
	m := d.up.meters[l.ID]
	if m == nil {
		m = datacap.New(l.Limits, time.Now)
		if l.Used > 0 {
			m.Restore(l.UsedSince, l.Used)
		}
		d.up.meters[l.ID] = m
	} else if m.Limits() != l.Limits {
		m.Update(l.Limits)
	}
	return m
}

// linkState is why a link runs or not (without connection state).
func (d *Daemon) linkStateLocked(l uplink.Link) string {
	if !l.Enabled {
		return UplinkOff
	}
	if _, ok := d.up.present[l.ID]; !ok {
		return UplinkAbsent
	}
	if _, stop := d.meterLocked(l).State(); stop {
		return UplinkLimit
	}
	if d.captiveLocked(l.ID) {
		return UplinkCaptive
	}
	return UplinkUp
}

// reconcileLocked starts the local paths that should run and stops the
// others (with upMu held).
func (d *Daemon) reconcileLocked() {
	links := d.upReg.List()
	var ttl []string
	for _, l := range links {
		want := d.linkStateLocked(l) == UplinkUp
		i := d.up.present[l.ID]
		d.mu.Lock()
		lp := d.locals[l.ID]
		d.mu.Unlock()
		if lp != nil && (!want || lp.iface != i.Name) {
			why := "leaving: " + d.linkStateLocked(l) // off, absent, limit or captive
			lp.stopWhy.Store(&why)
			d.stopLocalLocked(lp)
			lp = nil
		}
		if want && lp == nil {
			d.startLocalLocked(l, i)
		} else if lp != nil {
			if c := d.client.Load(); c != nil && lp.up.Load() {
				c.SetPathConfig(l.PathID, uplinkConfig(l))
			}
		}
		if want && l.TTL {
			ttl = append(ttl, i.Name)
		}
	}
	var forgotten []*localPath
	d.mu.Lock()
	for id, lp := range d.locals {
		if !slices.ContainsFunc(links, func(l uplink.Link) bool { return l.ID == id }) {
			forgotten = append(forgotten, lp)
		}
	}
	d.mu.Unlock()
	for _, lp := range forgotten {
		d.stopLocalLocked(lp)
	}
	d.applyTTLLocked(ttl)
}

// uplinkConfig is the PATH_CONFIG of a local link (the owner's settings;
// caps are enforced by the hub's meter, not the bond cap).
func uplinkConfig(l uplink.Link) wire.PathConfig {
	ps := l.Path
	ps.CapMB = 0
	return pathConfig(pairing.Relay{PathID: l.PathID, Path: ps})
}

// syntheticAddr is a local path's fixed address in the session.
func syntheticAddr(pid uint8) netip.AddrPort {
	return netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 77, 0, pid}), 0)
}

func (d *Daemon) startLocalLocked(l uplink.Link, i uplink.Iface) {
	lp := &localPath{d: d, id: l.ID, pid: l.PathID, iface: i.Name, kind: l.Kind, addr: syntheticAddr(l.PathID),
		meter: d.meterLocked(l), since: time.Now(), kick: make(chan struct{}, 1), done: make(chan struct{})}
	lp.initConns(d.cfg.TCPConns)
	ctx, cancel := context.WithCancel(d.ctx)
	lp.cancel = cancel
	d.mu.Lock()
	lp.setEP(d.serverEP)
	if ep, ok := d.lastEP[l.ID]; ok && d.enr != nil && slices.Contains(orderedEndpoints(d.enr.Endpoints), ep) {
		lp.setEP(ep)
	}
	d.locals[l.ID] = lp
	d.mu.Unlock()
	if d.cfg.NetSetup && netcfg.RPFilter {
		if old, err := netcfg.DefaultProcSys.SetRPFilter(i.Name, 2); err != nil {
			d.log.Warn("local uplink rp_filter", "iface", i.Name, "err", err)
		} else if _, saved := d.up.rpOld[i.Name]; !saved {
			d.up.rpOld[i.Name] = old
		}
	}
	d.log.Info("local uplink started", "uplink", l.ID, "name", l.Name, "iface", i.Name, "kind", l.Kind, "path", l.PathID)
	go lp.run(ctx)
	if ci := d.up.captive[l.ID]; ci == nil || ci.probe.State != uplink.NetOpen || time.Since(ci.checked) >= CaptiveRecheck {
		d.startProbeLocked(l.ID, i, "uplink up")
	}
}

func (d *Daemon) stopLocalLocked(lp *localPath) {
	d.mu.Lock()
	if d.locals[lp.id] == lp {
		delete(d.locals, lp.id)
	}
	d.mu.Unlock()
	lp.cancel()
	<-lp.done
	d.countLocal(lp, time.Now())
	if old, ok := d.up.rpOld[lp.iface]; ok && d.cfg.NetSetup {
		delete(d.up.rpOld, lp.iface)
		_, _ = netcfg.DefaultProcSys.SetRPFilter(lp.iface, old) // gone with the device when unplugged
	}
	d.log.Info("local uplink stopped", "uplink", lp.id, "iface", lp.iface)
}

// applyTTLLocked sets the TTL rule interfaces (nft, NetSetup only).
func (d *Daemon) applyTTLLocked(ttl []string) {
	slices.Sort(ttl)
	if slices.Equal(ttl, d.up.ttl) || !d.cfg.NetSetup {
		d.up.ttl = ttl
		return
	}
	d.up.ttl = ttl
	if err := d.applyLAN(d.ctx, func(l *netcfg.LAN) bool { l.TTL = ttl; return true }); err != nil {
		d.log.Warn("local uplink TTL rule", "err", err)
	}
}

// localUp adds a connected local path to the session.
func (d *Daemon) localUp(lp *localPath) {
	lp.up.Store(true)
	l, ok := d.upReg.Get(lp.id)
	if c := d.client.Load(); c != nil && ok {
		c.AddPath(lp.pid, lp.addr, lp, uplinkConfig(l))
	}
	d.emit(Event{Type: EvRelayJoined, Relay: lp.id, Name: l.Name})
}

// localDown takes a local path out of the session: removed (PATH_GONE)
// when stopped, DEAD and probed when only its connection failed.
func (d *Daemon) localDown(lp *localPath, stopped bool) {
	if !lp.up.Swap(false) {
		return
	}
	reason := "connection lost"
	if c := d.client.Load(); c != nil {
		if stopped {
			c.RemovePath(lp.pid)
			reason = "stopped"
			if w := lp.stopWhy.Load(); w != nil {
				reason = *w
			}
		} else {
			c.PathLost(lp.pid)
		}
	}
	d.noteFailover(time.Now())
	if ds := d.directStack(); ds != nil && stopped {
		ds.ResetRelay(lp.id)
	}
	l, _ := d.upReg.Get(lp.id)
	d.emit(Event{Type: EvRelayLeft, Relay: lp.id, Name: l.Name, Reason: reason})
}

// countLocal adds a local path's new bytes to the usage history (upMu held).
func (d *Daemon) countLocal(lp *localPath, now time.Time) {
	tx, rx := lp.tx.Load(), lp.rx.Load()
	if tx == lp.usedTx && rx == lp.usedRx {
		return
	}
	l, _ := d.upReg.Get(lp.id)
	d.usage.Add(lp.id, l.Name, tx-lp.usedTx, rx-lp.usedRx, now)
	lp.usedTx, lp.usedRx = tx, rx
}

// uplinkTick runs once a second: usage history and the cap of running
// local paths.
func (d *Daemon) uplinkTick(now time.Time) {
	d.mu.Lock()
	running := d.localPaths()
	d.mu.Unlock()
	alive := d.localAlive(running) // outside the locks: Report takes its own
	d.upMu.Lock()
	defer d.upMu.Unlock()
	d.mu.Lock()
	lps := d.localPaths()
	d.mu.Unlock()
	d.captiveTickLocked(lps, alive, now)
	capped := false
	for _, lp := range lps {
		d.countLocal(lp, now)
		lp.tickConns(now)
		if _, stop := lp.meter.State(); stop {
			capped = true
		}
	}
	if capped || d.limitRolled() {
		d.reconcileLocked()
	}
}

// limitRolled reports a link stopped by its cap whose period has rolled.
func (d *Daemon) limitRolled() bool {
	for _, l := range d.upReg.List() {
		if m := d.up.meters[l.ID]; m != nil && l.Enabled {
			d.mu.Lock()
			running := d.locals[l.ID] != nil
			d.mu.Unlock()
			if _, stop := m.State(); !stop && !running {
				if _, ok := d.up.present[l.ID]; ok {
					return true
				}
			}
		}
	}
	return false
}

// saveUplinks stores the data counters of the local links.
func (d *Daemon) saveUplinks() {
	d.upMu.Lock()
	for id, m := range d.up.meters {
		since, used := m.Snapshot()
		d.upReg.SetUsed(id, used, since)
	}
	d.upMu.Unlock()
	if err := d.upReg.Save(); err != nil {
		d.log.Warn("save local uplinks", "err", err)
	}
}

// UplinksUpdate changes a local link's settings (uplinks.update).
func (d *Daemon) UplinksUpdate(p uplink.Params) (UplinkStatus, error) {
	d.upMu.Lock()
	l, err := d.upReg.Update(p, time.Now())
	if err == nil {
		m := d.meterLocked(l)
		if p.ResetUsage {
			m.Reset()
		}
		d.reconcileLocked()
	}
	d.upMu.Unlock()
	if err != nil {
		return UplinkStatus{}, err
	}
	d.log.Info("local uplink settings", "uplink", l.ID, "name", l.Name, "enabled", l.Enabled, "ttl", l.TTL,
		"priority", l.Path.Priority, "cap_mb", l.Limits.CapMB, "period", l.Limits.CapPeriod)
	for _, u := range d.UplinksList() {
		if u.ID == l.ID {
			return u, nil
		}
	}
	return UplinkStatus{Link: l}, nil
}

// UplinksForget removes a link from the registry (uplinks.forget).
func (d *Daemon) UplinksForget(id string) error {
	d.upMu.Lock()
	defer d.upMu.Unlock()
	l, ok := d.upReg.Get(id)
	if err := d.upReg.Forget(id); err != nil {
		return err
	}
	if ok {
		delete(d.up.meters, l.ID)
	}
	d.reconcileLocked()
	return nil
}

// UplinksList returns every known local link with its state.
func (d *Daemon) UplinksList() []UplinkStatus {
	var rep PathReport
	if c := d.client.Load(); c != nil {
		rep = c.Report()
	}
	_, perRelay := d.directStatus()
	return d.uplinkStatus(rep, perRelay)
}

func (d *Daemon) uplinkStatus(rep PathReport, perRelay map[string]int) []UplinkStatus {
	paths := make(map[uint8]bond.PathStats)
	for _, p := range rep.Bond.Paths {
		paths[p.ID] = p
	}
	d.upMu.Lock()
	defer d.upMu.Unlock()
	out := []UplinkStatus{}
	for _, l := range d.upReg.List() {
		us := UplinkStatus{Link: l, State: d.linkStateLocked(l), DirectFlows: perRelay[l.ID]}
		since, used := d.meterLocked(l).Snapshot()
		us.UsedNow, us.Period = used, since
		if i, ok := d.up.present[l.ID]; ok {
			us.Present, us.Addr, us.Driver = true, i.Addr.String(), i.Driver
			if m, ok := d.up.modems[i.Name]; ok {
				us.Modem = &m
			}
		}
		d.mu.Lock()
		if lp := d.locals[l.ID]; lp != nil {
			us.Since, us.Endpoint = lp.since, lp.ep
			us.TCPConns = lp.liveConns()
			if !lp.up.Load() {
				us.State = UplinkConnecting
			}
		}
		d.mu.Unlock()
		us.Transport = transportOf(us.Endpoint)
		us.Captive = d.captiveStatusLocked(l.ID)
		if p, ok := paths[l.PathID]; ok && us.State == UplinkUp {
			us.Bond = &p
		}
		if a, ok := rep.Observed[l.PathID]; ok {
			us.Observed = a.String()
		}
		out = append(out, us)
	}
	return out
}

// addLocalPaths adds the connected local paths to a new tunnel client.
func (d *Daemon) addLocalPaths(c *Client) {
	d.mu.Lock()
	lps := d.localPaths()
	d.mu.Unlock()
	for _, lp := range lps {
		if l, ok := d.upReg.Get(lp.id); ok && lp.up.Load() {
			c.AddPath(lp.pid, lp.addr, lp, uplinkConfig(l))
		}
	}
}

// localsUp counts the connected local paths. Caller holds mu.
func (d *Daemon) localsUp() int {
	n := 0
	for _, lp := range d.locals {
		if lp.up.Load() {
			n++
		}
	}
	return n
}

// directLocals lists the local paths DIRECT flows may use.
func (d *Daemon) directLocals() []direct.Relay {
	d.mu.Lock()
	lps := d.localPaths()
	d.mu.Unlock()
	var out []direct.Relay
	for _, lp := range lps {
		l, ok := d.upReg.Get(lp.id)
		pc := uplinkConfig(l)
		if !ok || (pc.DirUp == wire.DirOff && pc.DirDown == wire.DirOff) {
			continue
		}
		backup := pc.Priority == wire.PriorityBackup || pc.DirUp == wire.DirBackupOnly || pc.DirDown == wire.DirBackupOnly
		out = append(out, direct.Relay{ID: lp.id, Backup: backup, Dial: lp.dialDirect})
	}
	return out
}
