package bond

import (
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

// probeTSBit marks MTU probe timestamps.
const probeTSBit = 1 << 63

// tickGranularity is the longest Tick may sleep while nothing else is due.
const tickGranularity = 10 * time.Millisecond

// ackStale: an ACK path not heard from this long gets a second ACK copy on
// the next best path.
const ackStale = 100 * time.Millisecond

// Tick runs the timers (liveness, pings, loss detection, ACKs, reorder hold,
// pacing) and returns when it wants to run next.
func (e *Engine) Tick() time.Time {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.cfg.Now()
	e.updateStates(now)
	e.detectLoss(now)
	e.sendPings(now)
	if due, _ := e.recv.ackDue(now); due {
		e.sendAcks(now)
	}
	gapEnd := e.recv.tick(now, e.holdTime(), e.cfg.Deliver)
	e.flush(now)
	aggDue := e.flushAggDue(now)

	next := now.Add(tickGranularity)
	if len(e.queue) > 0 || len(e.rtq) > 0 || len(e.rtx) > 0 || len(e.relq) > 0 {
		next = now.Add(time.Millisecond)
	}
	if _, due := e.recv.ackDue(now); !due.IsZero() {
		next = earlier(next, due)
	}
	next = earlier(next, gapEnd)
	next = earlier(next, aggDue)
	for _, p := range e.order {
		next = earlier(next, p.nextPing)
	}
	return later(next, now.Add(100*time.Microsecond))
}

func later(a, b time.Time) time.Time {
	if b.After(a) {
		return b
	}
	return a
}

// earlier returns the earlier of a and b; a zero b is ignored.
func earlier(a, b time.Time) time.Time {
	if !b.IsZero() && b.Before(a) {
		return b
	}
	return a
}

// updateStates applies the liveness rules to every path, then the quality
// guards and auto redundancy.
func (e *Engine) updateStates(now time.Time) {
	defer e.updateRedundancy(now)
	defer e.updateGuards()
	for _, p := range e.order {
		switch p.state {
		case Dead:
			if p.dropMissed(now) > 0 {
				p.pongStreak = 0
			}
		default:
			silence := SilenceDead
			if p.state == Sleeping {
				silence = SleepSilenceDead
			}
			missed := p.missedPings(now) >= MissedPingsDead || now.Sub(later(p.lastRecv, p.wokeAt)) > silence
			if missed && now.Before(p.holdUntil) {
				missed = false
			}
			if missed {
				e.killPath(now, p)
				continue
			}
			p.noteStable(now)
			cls, ok := p.dataClass()
			switch {
			case p.state == Alive && !p.unackedSince.IsZero() && now.Sub(p.unackedSince) > p.suspectAge():
				e.setState(p, Suspect, now)
				if !p.stream { // a stream path delivers late, not never: re-send only when it dies
					e.resendOutstanding(now, p)
				}
			case p.state == Alive && (!ok || cls == Backup) && now.Sub(p.lastData) > SleepAfterIdle:
				e.setState(p, Sleeping, now)
			}
		}
		if p.cc.probeRTTDue && !e.anyProbeRTT() {
			p.cc.enterProbeRTT()
		}
		if p.cc.update(now, p.inflight) {
			p.nextPing = now
		}
		e.probeMTU(now, p)
	}
}

func (e *Engine) anyProbeRTT() bool {
	for _, p := range e.order {
		if p.cc.mode == bbrProbeRTT {
			return true
		}
	}
	return false
}

// resendOutstanding re-sends a SUSPECT path's in-flight packets elsewhere.
func (e *Engine) resendOutstanding(now time.Time, p *path) {
	for _, sp := range p.sent {
		if !sp.done {
			e.lose(now, sp)
		}
	}
	p.pruneSent()
}

// sendPings sends due PINGs on every path.
func (e *Engine) sendPings(now time.Time) {
	for _, p := range e.order {
		if now.Before(p.nextPing) {
			continue
		}
		ts := e.ts(now)
		p.pings = append(p.pings, pingRec{ts: ts, sent: now})
		e.transmit(now, p, &wire.Ping{TS: ts})
		p.nextPing = now.Add(p.pingInterval(now))
	}
}

// probeMTU sends the padded MTU probes of an alive path.
func (e *Engine) probeMTU(now time.Time, p *path) {
	if p.state == Dead || now.Before(p.nextProbe) {
		return
	}
	if p.probesLeft == 0 {
		if p.mtu == 0 && !p.nextProbe.IsZero() {
			p.mtu = MinPathMTU
		}
		return
	}
	if p.mtu >= probeSizes[0] {
		p.probesLeft = 0
		return
	}
	p.probesLeft--
	p.nextProbe = now.Add(probeWait)
	for i, size := range probeSizes {
		ts := probeTSBit | e.ts(now)<<2 | uint64(i) // never equal to a plain ping's ts
		p.probes[ts] = size
		pad := size - DatagramOverhead - wire.FrameHeaderLen - 8
		e.transmit(now, p, &wire.Ping{TS: ts, Padding: pad})
	}
}

// onPong handles a PONG on p: RTT sample, MTU probe result, path recovery.
func (e *Engine) onPong(now time.Time, p *path, f *wire.Pong) {
	if size, ok := p.probes[f.TS]; ok {
		delete(p.probes, f.TS)
		if int(f.ProbeLen) >= size {
			p.mtu = max(p.mtu, size)
		}
		return
	}
	known := false
	for _, r := range p.pings {
		if r.ts == f.TS {
			known = true
			break
		}
	}
	if !known {
		return
	}
	p.onPong(now, e.tsTime(f.TS), f.TS)
	switch p.state {
	case Dead:
		if p.pongStreak >= p.pongsToAlive() {
			e.setState(p, Alive, now)
		}
	case Suspect:
		if p.inflight == 0 {
			p.unackedSince = time.Time{}
			e.setState(p, Alive, now)
		}
	}
}

// ackPaths returns the best and second best path for ACKs and control
// frames: not dead, recently heard, not capped, lowest RTT.
func (e *Engine) ackPaths(now time.Time) (best, second *path) {
	rank := func(p *path) int {
		r := 0
		if now.Sub(p.lastRecv) > max(250*time.Millisecond, p.srtt()) {
			r += 2
		}
		if p.capped() || p.state != Alive {
			r++
		}
		return r
	}
	better := func(a, b *path) bool {
		ra, rb := rank(a), rank(b)
		if ra != rb {
			return ra < rb
		}
		return a.srtt() < b.srtt()
	}
	for _, p := range e.order {
		if p.state == Dead {
			continue
		}
		switch {
		case best == nil || better(p, best):
			best, second = p, best
		case second == nil || better(p, second):
			second = p
		}
	}
	return best, second
}

// sendAcks sends the pending ACK frames on the best path (and a copy on the
// second best when the best was not heard from lately).
func (e *Engine) sendAcks(now time.Time) {
	best, second := e.ackPaths(now)
	if best == nil {
		if len(e.order) == 0 {
			return
		}
		best = e.order[0]
	}
	frames := e.recv.acks(now)
	e.transmit(now, best, frames...)
	if second != nil && now.Sub(best.lastRecv) > ackStale {
		e.transmit(now, second, frames...)
	}
}

// holdTime is the reorder hold for the current path RTTs.
func (e *Engine) holdTime() time.Duration {
	var lo, hi time.Duration
	found := false
	for _, p := range e.order {
		if p.state == Dead || !p.rtt.has || p.guarded {
			continue
		}
		s := p.rtt.srtt
		if !found {
			lo, hi, found = s, s, true
			continue
		}
		lo, hi = min(lo, s), max(hi, s)
	}
	if !found {
		return MaxHold
	}
	return holdTime(lo, hi)
}
