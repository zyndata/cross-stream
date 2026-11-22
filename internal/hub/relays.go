package hub

import (
	"errors"
	"net"
	"net/netip"
	"slices"
	"sync/atomic"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/pairing"
	"github.com/zyndata/cross-stream/internal/session"
	"github.com/zyndata/cross-stream/internal/transport"
	"github.com/zyndata/cross-stream/internal/wire"
)

var errNoRelayAddr = errors.New("hub: relay data address not known yet")

// relayPath is one connected relay. It is also the session.Sender of that
// relay's path: outer packets go to the relay as [B1][path_id][packet].
type relayPath struct {
	d     *Daemon
	rec   pairing.Relay
	ctl   *control.Conn
	ip    netip.Addr // control peer address; data must come from it too
	since time.Time
	data  atomic.Pointer[netip.AddrPort] // relay Wi-Fi data socket, from B3/B2
	stats atomic.Pointer[control.Stats]
	peak  atomic.Uint64 // float64 bits: peak cellular rate, bytes/s (DIRECT weight)

	// relayLoop only
	prevStats *control.Stats
	prevAt    time.Time

	epTrack // server endpoint this relay forwards to
}

// PathID implements PathConn.
func (rc *relayPath) PathID() uint8 { return rc.rec.PathID }

func (rc *relayPath) fallbackKey() string { return rc.rec.ID }
func (rc *relayPath) track() *epTrack     { return &rc.epTrack }
func (rc *relayPath) endpointMoved()      { rc.d.sendEndpoint(rc) }

// WriteToUDPAddrPort implements session.Sender. The address is the server's
// and is ignored: the relay forwards to the endpoint it was given.
func (rc *relayPath) WriteToUDPAddrPort(b []byte, _ netip.AddrPort) (int, error) {
	to := rc.data.Load()
	if to == nil {
		return 0, errNoRelayAddr
	}
	buf := make([]byte, 0, wire.RelayPathHeaderLen+len(b))
	buf = append(buf, byte(wire.RelayToServer), rc.rec.PathID)
	n, err := rc.d.data.WriteToUDPAddrPort(append(buf, b...), *to)
	return max(n-wire.RelayPathHeaderLen, 0), err
}

// connected returns the connected relays sorted by path id. Caller holds mu.
func (d *Daemon) connected() []*relayPath {
	out := make([]*relayPath, 0, len(d.relays))
	for _, rc := range d.relays {
		out = append(out, rc)
	}
	slices.SortFunc(out, func(a, b *relayPath) int { return int(a.rec.PathID) - int(b.rec.PathID) })
	return out
}

// uplink picks the path for a handshake attempt among the connected relays
// with a known data address and the local paths that are up. Each call
// takes the next one, so one relay without working mobile data cannot keep
// the tunnel down while another path would work.
func (d *Daemon) uplink() (uint8, session.Sender, bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	type cand struct {
		id  uint8
		via session.Sender
	}
	var cs []cand
	for _, rc := range d.connected() {
		if rc.data.Load() != nil {
			cs = append(cs, cand{rc.rec.PathID, rc})
		}
	}
	for _, lp := range d.localPaths() {
		if lp.up.Load() {
			cs = append(cs, cand{lp.pid, lp})
		}
	}
	if len(cs) == 0 {
		return 0, nil, false
	}
	c := cs[d.hsTry%len(cs)]
	d.hsTry++
	return c.id, c.via, true
}

func (d *Daemon) acceptLoop() {
	for {
		c, err := d.ctlLn.Accept()
		if err != nil {
			return
		}
		go d.serveRelay(c)
	}
}

// serveRelay runs one relay control connection: handshake, hello, admit or
// reject, then stats until the relay leaves or goes silent for 5 s.
func (d *Daemon) serveRelay(c net.Conn) {
	ctl, err := control.Server(c, d.relayKey)
	if err != nil {
		d.log.Debug("relay handshake failed", "from", c.RemoteAddr(), "err", err)
		_ = c.Close()
		return
	}
	defer func() { _ = ctl.Close() }()
	_ = ctl.SetReadDeadline(time.Now().Add(control.HandshakeTimeout))
	m, err := ctl.Recv()
	hello, ok := m.(*control.Hello)
	if err != nil || !ok {
		d.log.Debug("relay sent no hello", "from", c.RemoteAddr(), "err", err)
		return
	}
	rec, reason := d.admit(ctl.Peer(), hello)
	if reason != "" {
		d.log.Info("relay rejected", "name", hello.RelayName, "from", c.RemoteAddr(), "reason", reason)
		_ = ctl.Send(&control.Reject{Reason: reason})
		return
	}
	ta, _ := c.RemoteAddr().(*net.TCPAddr)
	rc := &relayPath{d: d, rec: rec, ctl: ctl, ip: ta.AddrPort().Addr().Unmap(), since: time.Now()}
	rc.initConns(d.cfg.TCPConns)
	d.mu.Lock()
	rc.setEP(d.serverEP)
	if ep, ok := d.lastEP[rec.ID]; ok && d.enr != nil && slices.Contains(orderedEndpoints(d.enr.Endpoints), ep) {
		rc.setEP(ep) // the endpoint that worked last time (e.g. TCP on a UDP-blocking carrier)
	}
	d.mu.Unlock()

	// Welcome and registration under one lock, so an endpoint change cannot
	// slip in between (the relay ignores messages before welcome).
	d.mu.Lock()
	old := d.relays[rec.ID]
	err = ctl.Send(&control.Welcome{RelayID: rec.ID, PathID: rec.PathID, ServerEP: rc.ep, DataPort: d.cfg.DataPort,
		TCPConns: rc.tcpConns()})
	if err == nil {
		d.relays[rec.ID] = rc
	}
	d.mu.Unlock()
	if old != nil {
		_ = old.ctl.Close()
	}
	if err != nil {
		return
	}
	if m := d.modeMsg(); m.Proxy {
		d.sendMode([]*relayPath{rc})
	}
	d.emit(Event{Type: EvRelayJoined, Relay: rec.ID, Name: rec.Name})
	d.log.Info("relay connected", "relay", rec.ID, "name", rec.Name, "carrier", rec.Carrier, "path", rec.PathID, "from", rc.ip)
	reasonGone, leaving := d.relayLoop(rc)
	d.mu.Lock()
	current := d.relays[rec.ID] == rc
	if current {
		delete(d.relays, rec.ID)
	}
	d.mu.Unlock()
	if current {
		d.relayGone(rec.ID, reasonGone)
		d.noteFailover(time.Now())
		d.emit(Event{Type: EvRelayLeft, Relay: rec.ID, Name: rec.Name, Reason: reasonGone})
	}
	if ds := d.directStack(); ds != nil && current {
		if n := ds.ResetRelay(rec.ID); n > 0 {
			d.log.Info("direct flows of a lost relay reset", "relay", rec.ID, "flows", n)
		}
	}
	if c := d.client.Load(); c != nil && current && rc.data.Load() != nil {
		if leaving {
			c.RemovePath(rec.PathID) // PATH_GONE to the server
		} else {
			c.PathLost(rec.PathID) // control lost: DEAD here, PATH_GONE to the server
		}
	}
	d.log.Info("relay disconnected", "relay", rec.ID, "name", rec.Name, "reason", reasonGone)
}

// relayLoop reads control messages until the relay leaves (leaving = true)
// or the channel fails.
func (d *Daemon) relayLoop(rc *relayPath) (reason string, leaving bool) {
	for {
		_ = rc.ctl.SetReadDeadline(time.Now().Add(control.DeadAfter))
		m, err := rc.ctl.Recv()
		if err != nil {
			return err.Error(), false
		}
		switch m := m.(type) {
		case *control.Stats:
			now := time.Now()
			if d.cfg.StatsNow != nil {
				now = d.cfg.StatsNow()
			}
			rc.stats.Store(m)
			d.countUsage(rc, m, now)
			rc.updatePeak(now, m)
			if n, changed := rc.observeConns(now, transport.TCPCounters{Conns: m.TCPConns, Retrans: m.TCPRetrans, Sent: m.TCPSent}); changed {
				d.sendConns(rc, n, m)
			}
		case *control.Leaving:
			return "leaving: " + m.Reason, true
		case *control.LimitsUpdate:
			if err := d.reg.Update(rc.rec.ID, rc.rec.Name, rc.rec.Carrier, m.Limits); err != nil {
				d.log.Warn("save relay limits", "relay", rc.rec.ID, "err", err)
			}
			rc.rec.Limits = m.Limits
			d.applyPathConfig(rc.rec.ID)
		}
	}
}

// admit decides about a relay from its key and hello: known relays are
// accepted, new ones need a valid pairing token. It returns a reject reason
// or "".
func (d *Daemon) admit(peer [crypto.KeyLen]byte, h *control.Hello) (pairing.Relay, string) {
	if rec, ok := d.reg.Lookup(peer); ok {
		if err := d.reg.Update(rec.ID, h.RelayName, h.Carrier, h.Limits); err != nil {
			d.log.Warn("save relay", "relay", rec.ID, "err", err)
		}
		rec.Name, rec.Carrier, rec.Limits = h.RelayName, h.Carrier, h.Limits
		return rec, ""
	}
	if h.PairToken == "" || d.issuer.Consume(h.PairToken) != nil {
		if d.reg.IsForgotten(peer) {
			return pairing.Relay{}, control.RejectForgotten
		}
		return pairing.Relay{}, control.RejectBadToken
	}
	rec, err := d.reg.Add(peer, *h)
	if err != nil {
		d.log.Warn("register relay", "name", h.RelayName, "err", err)
		return pairing.Relay{}, control.RejectHubFull
	}
	d.log.Info("relay paired", "relay", rec.ID, "name", rec.Name, "path", rec.PathID)
	return rec, ""
}

// Forget removes a relay (by id or name), tells it to delete this hub if it
// is connected and rejects its key from now on.
func (d *Daemon) Forget(idOrName string) (pairing.Relay, error) {
	rec, err := d.reg.Forget(idOrName)
	if err != nil {
		return pairing.Relay{}, err
	}
	d.mu.Lock()
	rc := d.relays[rec.ID]
	delete(d.relays, rec.ID)
	d.mu.Unlock()
	if rc != nil {
		_ = rc.ctl.Send(&control.Forget{})
		_ = rc.ctl.Close()
		if c := d.client.Load(); c != nil {
			c.RemovePath(rec.PathID)
		}
		// serveRelay no longer finds the relay in the table and skips its
		// leave handling: do it here.
		d.mu.Lock()
		delete(d.lastEP, rc.fallbackKey())
		d.mu.Unlock()
		d.relayGone(rec.ID, "forgotten")
		d.emit(Event{Type: EvRelayLeft, Relay: rec.ID, Name: rec.Name, Reason: "forgotten"})
		if ds := d.directStack(); ds != nil {
			ds.ResetRelay(rec.ID)
		}
	}
	d.log.Info("relay forgotten", "relay", rec.ID, "name", rec.Name, "was_connected", rc != nil)
	return rec, nil
}

// dataLoop reads relay datagrams: B3 keepalives and B2 server packets. A
// datagram counts only if it comes from a connected relay's control address
// with that relay's path id; it also updates the relay's data address.
func (d *Daemon) dataLoop() {
	buf := make([]byte, 65535)
	for {
		n, from, err := d.data.ReadFromUDPAddrPort(buf)
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return
			}
			continue
		}
		from = netip.AddrPortFrom(from.Addr().Unmap(), from.Port())
		m, err := wire.ParseRelay(buf[:n])
		if err == nil && m.Kind == wire.RelayUDPIn {
			if rc := d.relayByData(from); rc != nil {
				if ds := d.directStack(); ds != nil {
					ds.HandleUDP(rc.rec.ID, m.FlowID, m.Payload)
				}
			}
			continue
		}
		if err != nil || (m.Kind != wire.RelayFromSrv && m.Kind != wire.RelayKeepalive) {
			continue
		}
		rc := d.relayFor(from.Addr(), m.PathID)
		if rc == nil {
			continue
		}
		if cur := rc.data.Load(); cur == nil || *cur != from {
			rc.data.Store(&from)
			d.addPath(rc)
		}
		if m.Kind == wire.RelayFromSrv {
			if c := d.client.Load(); c != nil {
				c.HandlePacket(m.Payload, from, rc)
			}
		}
	}
}

// relayByData returns the connected relay whose data socket is from.
func (d *Daemon) relayByData(from netip.AddrPort) *relayPath {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, rc := range d.relays {
		if a := rc.data.Load(); a != nil && *a == from {
			return rc
		}
	}
	return nil
}

func (d *Daemon) relayFor(ip netip.Addr, pathID uint8) *relayPath {
	d.mu.Lock()
	defer d.mu.Unlock()
	for _, rc := range d.relays {
		if rc.ip == ip && rc.rec.PathID == pathID {
			return rc
		}
	}
	return nil
}
