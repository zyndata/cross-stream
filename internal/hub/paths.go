package hub

import (
	"net/netip"

	"github.com/zyndata/cross-stream/internal/bond"
	"github.com/zyndata/cross-stream/internal/session"
	"github.com/zyndata/cross-stream/internal/wire"
)

// clientPath is one relay path the client keeps in every session.
type clientPath struct {
	via    session.Sender
	addr   netip.AddrPort
	cfg    wire.PathConfig
	sent   wire.PathConfig // last PATH_CONFIG sent
	sentOK bool
	repeat int // further sends of sent (PATH_CONFIG is one unacked datagram)
	// last PATH_NAME sent (named: one was sent)
	sentName string
	named    bool
}

// hubConfig converts a PATH_CONFIG to the hub's (upload direction) policy.
func hubConfig(f wire.PathConfig) bond.PathConfig {
	return bond.PathConfig{Priority: bond.Priority(f.Priority), RateBps: uint64(f.RateKbps) * 1000 / 8,
		CapBytes: uint64(f.CapMB) * 1_000_000, Dir: bond.DirMode(f.DirUp)}
}

// AddPath adds or updates a relay path: via delivers outer packets to the
// server through the relay, addr is the relay's data address. A new path is
// probed before it carries data; an existing one is re-probed at once.
func (c *Client) AddPath(id uint8, addr netip.AddrPort, via session.Sender, cfg wire.PathConfig) {
	c.init()
	cfg.PathID = id
	c.pmu.Lock()
	c.paths[id] = &clientPath{via: via, addr: addr, cfg: cfg}
	used := c.used[id]
	c.pmu.Unlock()
	if s := c.sess.Load(); s != nil {
		s.SetPathConfig(id, hubConfig(cfg))
		s.AddPath(id, addr, via, false)
		s.Bond().SetUsed(id, used)
		c.sendConfig(s, false)
	}
}

// RemovePath drops a relay path (the relay left) and tells the server with
// PATH_GONE.
func (c *Client) RemovePath(id uint8) {
	c.init()
	c.pmu.Lock()
	delete(c.paths, id)
	delete(c.obs, id)
	c.pmu.Unlock()
	if s := c.sess.Load(); s != nil {
		c.recordUsed(s)
		if err := s.Send(&wire.PathGone{PathID: id}); err != nil {
			c.Log.Debug("path gone", "path", id, "err", err)
		}
		s.RemovePath(id)
	}
}

// PathLost handles a relay whose control channel was lost: the path turns
// DEAD here (probed, it returns when PONGs come back) and the server gets
// PATH_GONE at once, so it re-sends that path's unacked packets elsewhere
// instead of waiting for its own detection; it learns the path again from
// the relay's next packet.
func (c *Client) PathLost(id uint8) {
	s := c.sess.Load()
	if s == nil {
		return
	}
	s.Bond().MarkDead(id)
	if err := s.Send(&wire.PathGone{PathID: id}); err != nil {
		c.Log.Debug("path gone", "path", id, "err", err)
	}
}

// SetPathStream marks a relay path as carried over TCP/WSS from the relay
// to the server (no fast retransmit, ETA penalty).
func (c *Client) SetPathStream(id uint8, stream bool) {
	if s := c.sess.Load(); s != nil {
		s.Bond().SetStream(id, stream)
	}
}

// SetPathConfig changes a relay path's policy and tells the server.
func (c *Client) SetPathConfig(id uint8, cfg wire.PathConfig) {
	c.init()
	cfg.PathID = id
	c.pmu.Lock()
	p := c.paths[id]
	if p != nil {
		p.cfg = cfg
	}
	c.pmu.Unlock()
	if s := c.sess.Load(); s != nil && p != nil {
		s.SetPathConfig(id, hubConfig(cfg))
		c.sendConfig(s, false)
	}
}

// loadPaths adds every known relay path to a new session.
func (c *Client) loadPaths(s *session.Session) {
	c.pmu.Lock()
	type entry struct {
		id   uint8
		p    clientPath
		used uint64
	}
	var all []entry
	for id, p := range c.paths {
		p.sentOK = false
		all = append(all, entry{id, *p, c.used[id]})
	}
	c.pmu.Unlock()
	for _, e := range all {
		s.SetPathConfig(e.id, hubConfig(e.p.cfg))
		s.AddPath(e.id, e.p.addr, e.p.via, false)
		s.Bond().SetUsed(e.id, e.used)
	}
}

// recordUsed keeps each path's cap count, so it survives the path being
// removed and added again.
func (c *Client) recordUsed(s *session.Session) map[uint8]bool {
	capped := make(map[uint8]bool)
	st := s.Bond().Stats()
	c.pmu.Lock()
	defer c.pmu.Unlock()
	for _, p := range st.Paths {
		c.used[p.ID] = p.Used
		capped[p.ID] = p.Capped
	}
	return capped
}

// sendConfig sends PATH_CONFIG for paths whose config changed (all paths
// and SESSION_CONFIG when full). A path over its cap is sent with both
// directions off, so the server stops using it too.
func (c *Client) sendConfig(s *session.Session, full bool) {
	out := c.configFrames(c.recordUsed(s), full)
	out = append(out, c.pathNames(full)...)
	if full {
		o := c.getBondOpts()
		out = append(out, &wire.SessionConfig{OverflowMbps: c.overflowMbps(), Redundancy: uint8(o.Redundancy), Mode: uint8(o.Mode)})
	}
	if len(out) == 0 {
		return
	}
	if err := s.Send(out...); err != nil {
		c.Log.Debug("send path config", "err", err)
	}
}

// ConfigRepeats is how many more times a changed PATH_CONFIG is sent, one
// per maintain tick (1 s): a lost one left a relay-capped backup path
// carrying data as "normal" until the 10 s full resend (e2e prio-cap,
// phase 18).
const ConfigRepeats = 3

// configFrames returns the PATH_CONFIG frames to send: changed or not yet
// sent ones, their repeats, and all of them when full. capped paths go out
// with both directions off.
func (c *Client) configFrames(capped map[uint8]bool, full bool) []wire.Frame {
	var out []wire.Frame
	c.pmu.Lock()
	defer c.pmu.Unlock()
	for id, p := range c.paths {
		w := p.cfg
		if capped[id] {
			w.DirUp, w.DirDown = wire.DirOff, wire.DirOff
		}
		switch {
		case !p.sentOK || p.sent != w:
			p.sent, p.sentOK, p.repeat = w, true, ConfigRepeats
		case p.repeat > 0:
			p.repeat--
		case !full:
			continue
		}
		out = append(out, &w)
	}
	return out
}

// pathNames returns PATH_NAME frames for paths whose name changed (all
// named paths when full). Names come from c.PathName outside c.pmu.
func (c *Client) pathNames(full bool) []wire.Frame {
	if c.PathName == nil {
		return nil
	}
	c.pmu.Lock()
	ids := make([]uint8, 0, len(c.paths))
	for id := range c.paths {
		ids = append(ids, id)
	}
	c.pmu.Unlock()
	names := make(map[uint8]string, len(ids))
	for _, id := range ids {
		names[id] = wire.TruncPathName(c.PathName(id))
	}
	var out []wire.Frame
	c.pmu.Lock()
	defer c.pmu.Unlock()
	for id, n := range names {
		p := c.paths[id]
		if p == nil || (!full && p.named && p.sentName == n) || (n == "" && !p.named) {
			continue
		}
		p.sentName, p.named = n, true
		out = append(out, &wire.PathName{PathID: id, Name: n})
	}
	return out
}

// PathReport is the client's view of its paths for status.
type PathReport struct {
	Up        bool
	Bond      bond.Stats
	Observed  map[uint8]netip.AddrPort
	TunnelMTU int
}

// Report returns the current session's bonding stats and observed addresses.
func (c *Client) Report() PathReport {
	c.init()
	r := PathReport{Observed: make(map[uint8]netip.AddrPort)}
	s := c.sess.Load()
	if s == nil {
		return r
	}
	r.Up, r.Bond, r.TunnelMTU = true, s.Bond().Stats(), s.Bond().TunnelMTU()
	c.pmu.Lock()
	for id, a := range c.obs {
		r.Observed[id] = a
	}
	c.pmu.Unlock()
	return r
}

// SetOverflow changes the overflow threshold of the running client: the
// local engine (upload) at once, the server (download) by SESSION_CONFIG.
func (c *Client) SetOverflow(mbps uint16) {
	c.overflow.Store(uint32(mbps))
	c.overflowSet.Store(true)
	if s := c.sess.Load(); s != nil {
		s.Bond().SetOverflow(uint64(mbps) * 1_000_000 / 8)
		c.sendConfig(s, true)
	}
}

// SetBondOpts changes redundancy, mode and aggregation of the running
// client: the local engine at once, the server by SESSION_CONFIG
// (aggregation is local only).
func (c *Client) SetBondOpts(o BondOpts) {
	c.bondOpts.Store(&o)
	if s := c.sess.Load(); s != nil {
		c.applyBondOpts(s)
		c.sendConfig(s, true)
	}
}

func (c *Client) getBondOpts() BondOpts {
	if o := c.bondOpts.Load(); o != nil {
		return *o
	}
	return c.Bond
}

func (c *Client) applyBondOpts(s *session.Session) {
	o := c.getBondOpts()
	s.Bond().SetRedundancy(o.Redundancy)
	s.Bond().SetMode(o.Mode)
	s.Bond().SetAggregation(!o.NoAggregate)
}

func (c *Client) overflowMbps() uint16 {
	if c.overflowSet.Load() {
		return uint16(c.overflow.Load())
	}
	return c.OverflowMbps
}
