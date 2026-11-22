package hub

import (
	"slices"
	"sync/atomic"
	"time"

	"github.com/zyndata/cross-stream/internal/bond"
	"github.com/zyndata/cross-stream/internal/session"
	"github.com/zyndata/cross-stream/internal/transport"
)

// PathConn is one hub path to the server (architecture.md "Local uplinks on
// the hub"): relayPath through a relay phone, localPath through one of the
// hub's own interfaces.
type PathConn interface {
	session.Sender
	PathID() uint8
	// Stream reports a TCP/WSS path (no fast retransmit, ETA penalty).
	Stream() bool
}

// fallbackPath is a path whose server endpoint moves on when it stays dead
// (endpointFallback).
type fallbackPath interface {
	PathConn
	fallbackKey() string // key of Daemon.lastEP
	track() *epTrack     // guarded by Daemon.mu
	endpointMoved()      // called without Daemon.mu after the endpoint changed
}

// epTrack is a path's server endpoint (ep and deadSince guarded by
// Daemon.mu).
type epTrack struct {
	ep        string
	deadSince time.Time    // path not working since (endpoint fallback)
	stream    atomic.Bool  // ep is a TCP/WSS endpoint
	conns     atomic.Int32 // TCP connections for a tcp:// ep (connPolicy)
	policy    *connPolicy  // owned by the path's stats loop (relayLoop / uplinkTick)
}

// initConns sets up the path's connection policy (fixed > 0: override).
func (t *epTrack) initConns(fixed int) {
	t.policy = newConnPolicy(fixed)
	t.conns.Store(int32(t.policy.conns()))
}

// tcpConns is the number of TCP connections a tcp:// endpoint gets.
func (t *epTrack) tcpConns() int { return max(int(t.conns.Load()), 1) }

// observeConns feeds a connection set's counters to the policy; it returns
// the new number when it changed.
func (t *epTrack) observeConns(now time.Time, c transport.TCPCounters) (int, bool) {
	if t.policy == nil || c.Conns == 0 {
		return 0, false
	}
	n, changed := t.policy.update(now, c.Retrans, c.Sent)
	if changed {
		t.conns.Store(int32(n))
	}
	return n, changed
}

// setEP records the endpoint (with Daemon.mu held).
func (t *epTrack) setEP(ep string) {
	t.ep = ep
	t.stream.Store(transportOf(ep) == "tcp" || transportOf(ep) == "wss")
}

// Stream reports whether the path reaches the server over TCP/WSS.
func (t *epTrack) Stream() bool { return t.stream.Load() }

// fallbackPaths lists the connected relays and running local paths. Caller
// holds mu.
func (d *Daemon) fallbackPaths() []fallbackPath {
	var out []fallbackPath
	for _, rc := range d.connected() {
		out = append(out, rc)
	}
	for _, lp := range d.localPaths() {
		out = append(out, lp)
	}
	return out
}

// endpointFallback moves a path whose path stays DEAD to the next server
// endpoint (UDP → TCP → WSS → UDP …; protocol.md §3) after
// EndpointFallbackAfter while another path is ALIVE. With no ALIVE path
// only a single path moves (after 3 × the time: several paths dead at once
// means the server is down, and moving them all to TCP/WSS would only slow
// the return). A path that works keeps its endpoint for its next start.
func (d *Daemon) endpointFallback(now time.Time, st bond.Stats) {
	working := make(map[uint8]bool, len(st.Paths))
	anyAlive := false
	for _, p := range st.Paths {
		switch p.State {
		case bond.Alive.String(), bond.Suspect.String(), bond.Sleeping.String():
			working[p.ID] = true
		}
		anyAlive = anyAlive || p.State == bond.Alive.String()
	}
	wait := EndpointFallbackAfter
	if !anyAlive {
		wait *= 3
	}
	d.mu.Lock()
	var eps []string
	if d.enr != nil {
		eps = orderedEndpoints(d.enr.Endpoints)
	}
	var moves []fallbackPath
	ps := d.fallbackPaths()
	for _, p := range ps {
		t := p.track()
		if working[p.PathID()] {
			t.deadSince = time.Time{}
			d.lastEP[p.fallbackKey()] = t.ep
			continue
		}
		if !anyAlive && len(ps) > 1 { // the server, not this carrier: no dead time counted
			t.deadSince = time.Time{}
			continue
		}
		if t.deadSince.IsZero() {
			t.deadSince = now
			continue
		}
		if len(eps) < 2 || now.Sub(t.deadSince) < wait {
			continue
		}
		i := slices.Index(eps, t.ep)
		t.setEP(eps[(i+1)%len(eps)])
		t.deadSince = now
		delete(d.lastEP, p.fallbackKey())
		moves = append(moves, p)
	}
	d.mu.Unlock()
	for _, p := range moves {
		p.endpointMoved()
	}
}
