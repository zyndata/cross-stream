package bond

import "time"

// Path quality guards (docs/openmptcprouter-notes.md, phase 10).
const (
	// GuardRTT and GuardFactor: a path with srtt above GuardRTT (while
	// another path is below it) or above GuardFactor × the best path's
	// srtt gets no new data, so it cannot stretch the reorder hold. It is
	// released below GuardRelease of the limit.
	GuardRTT     = 500 * time.Millisecond
	GuardFactor  = 3
	GuardRelease = 0.8
	// FlapWindow: a path that dies this soon after it came back needs twice
	// the PONGs next time (PongsToAlive → … → MaxPongsToAlive); FlapReset of
	// stable life resets it.
	FlapWindow      = 30 * time.Second
	FlapReset       = 5 * time.Minute
	MaxPongsToAlive = 12
)

// updateGuards recomputes which paths are guarded.
func (e *Engine) updateGuards() {
	// The reference is the fastest path that carries data now: an ALIVE
	// path of the best class in use. A sleeping backup or a SUSPECT path
	// with a low RTT must not get the working paths guarded.
	var best time.Duration
	found := false
	top := 3
	for _, p := range e.order {
		if cls, ok := p.dataClass(); ok && p.state == Alive && p.rtt.has {
			top = min(top, classRank(cls))
		}
	}
	for _, p := range e.order {
		cls, ok := p.dataClass()
		if !ok || p.state != Alive || !p.rtt.has || classRank(cls) != top {
			continue
		}
		if !found || p.rtt.srtt < best {
			best, found = p.rtt.srtt, true
		}
	}
	for _, p := range e.order {
		if !found || p.state == Dead || !p.rtt.has {
			p.guarded = false
			continue
		}
		limit := GuardFactor * best
		if best <= GuardRTT {
			limit = min(limit, GuardRTT)
		}
		if p.guarded {
			limit = time.Duration(float64(limit) * GuardRelease)
		}
		p.guarded = p.rtt.srtt > limit && p.rtt.srtt > best
	}
}

// classRank orders the priorities the way the scheduler tries them.
func classRank(c Priority) int {
	switch c {
	case Normal:
		return 0
	case Secondary:
		return 1
	}
	return 2
}

// pongsToAlive is how many PONGs in a row bring p back from DEAD.
func (p *path) pongsToAlive() int {
	if p.pongsNeeded == 0 {
		return PongsToAlive
	}
	return p.pongsNeeded
}

// noteDeath doubles the PONGs p needs next time if it died soon after it
// came back (anti-flap).
func (p *path) noteDeath(now time.Time) {
	if !p.aliveSince.IsZero() && now.Sub(p.aliveSince) < FlapWindow {
		p.pongsNeeded = min(2*p.pongsToAlive(), MaxPongsToAlive)
	}
	p.aliveSince, p.died = time.Time{}, true
}

// noteStable resets the anti-flap penalty after FlapReset alive.
func (p *path) noteStable(now time.Time) {
	if p.pongsNeeded != 0 && !p.aliveSince.IsZero() && now.Sub(p.aliveSince) >= FlapReset {
		p.pongsNeeded = 0
	}
}
