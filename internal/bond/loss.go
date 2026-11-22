package bond

import (
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

func (e *Engine) onAckFrame(now time.Time, f *wire.Ack) {
	e.onAck(now, f.Largest)
	for i := range uint32(64) {
		if f.Bitmap&(1<<i) != 0 {
			e.onAck(now, f.Largest-1-i)
		}
	}
}

func (e *Engine) onAck(now time.Time, seq uint32) {
	sp := e.unacked[seq]
	if sp == nil {
		return
	}
	delete(e.unacked, seq)
	if sp.done || sp.sent.IsZero() { // waiting for a re-send: nothing in flight
		sp.done = true
		return
	}
	sp.done = true
	p := sp.path
	p.inflight -= sp.size
	if t := sp.twin; t != nil && !t.done && !t.sent.IsZero() {
		t.done = true
		t.path.inflight -= t.size
		t.path.noteAckProgress(now)
	}
	minRTT := p.minRTTEst()
	// An ACK for a re-sent seq may be for the original copy on another
	// path (a delivery stall made it look lost): crediting it to this path
	// inflated the rate samples. Only first transmissions are sampled, and
	// no second copies (either copy may have arrived).
	if sp.attempts == 0 && !sp.isTwin {
		e.sample(now, p, sp, minRTT)
	}
	p.noteAckProgress(now)
	if p.state == Suspect {
		e.setState(p, Alive, now)
	}
	if t := sp.twin; t != nil && t.path.state == Suspect && t.path.inflight == 0 {
		e.setState(t.path, Alive, now)
	}
}

// noteAckProgress restarts p's unacked-data clock after an ACK.
func (p *path) noteAckProgress(now time.Time) {
	p.lastAckProgress = now
	if p.inflight > 0 {
		p.unackedSince = now
	} else {
		p.unackedSince = time.Time{}
	}
}

// sample feeds an acked packet to the path's delivery rate, RTT and
// congestion state.
func (e *Engine) sample(now time.Time, p *path, sp *sentPacket, minRTT time.Duration) {
	p.delivered += uint64(sp.size)
	if p.appLimitedUntil != 0 && p.delivered > p.appLimitedUntil {
		p.appLimitedUntil = 0
	}
	p.deliveredTime = now
	p.firstSent = sp.sent
	rtt := now.Sub(sp.sent)
	p.dataRTT.sample(rtt)
	p.latestDataRTT = rtt
	interval := max(sp.sent.Sub(sp.firstSent), now.Sub(sp.deliveredTime))
	var rate float64
	if interval >= minRTT/2 && interval > 0 {
		rate = float64(p.delivered-sp.delivered) / interval.Seconds()
	}
	p.cc.onAck(now, rate, sp.appLimited, sp.delivered, p.delivered, p.inflight, minRTT)
	if !p.hasAcked || sp.num > p.largestAcked {
		p.largestAcked, p.hasAcked = sp.num, true
	}
}

// StreamLossAge is the shortest age at which a packet on a TCP/WSS path
// counts as lost (dropped from a full send queue): the transport is
// reliable, so there is no fast retransmit, only this accounting.
const StreamLossAge = 2 * time.Second

// detectLoss declares packets lost when 3 later packets on the same path
// were acked and the packet is older than the latest RTT plus a reordering
// window (RACK-like: links with jitter reorder a little), or when it is older
// than the path's loss timeout.
func (e *Engine) detectLoss(now time.Time) {
	for _, p := range e.order {
		if p.stream {
			e.detectStreamLoss(now, p)
			continue
		}
		to := p.lossTimeout()
		reo := p.latestDataRTT + max(p.minRTTEst()/4, p.rtt.rttvar)
		for _, sp := range p.sent {
			if sp.done {
				continue
			}
			age := now.Sub(sp.sent)
			byNum := p.hasAcked && sp.num+3 <= p.largestAcked && age >= reo
			if !byNum && age <= to {
				if p.hasAcked && sp.num+3 <= p.largestAcked {
					continue // reordered maybe: later packets may still be lost by age
				}
				break
			}
			e.lose(now, sp)
		}
		p.pruneSent()
		p.noteLoss(now)
	}
}

// detectStreamLoss forgets packets on a reliable path that stayed unacked
// for StreamLossAge (or 4 loss timeouts).
func (e *Engine) detectStreamLoss(now time.Time, p *path) {
	to := max(StreamLossAge, 4*p.lossTimeout())
	for _, sp := range p.sent {
		if sp.done {
			continue
		}
		if now.Sub(sp.sent) <= to {
			break
		}
		e.lose(now, sp)
	}
	p.pruneSent()
	p.noteLoss(now)
}

// lose handles one lost transmission: re-send it if allowed, else forget it.
// While the other copy of a duplicated packet is still on its way, that copy
// takes over (no re-send).
func (e *Engine) lose(now time.Time, sp *sentPacket) {
	p := sp.path
	sp.done = true
	p.inflight -= sp.size
	p.lost++
	p.winLost++
	p.cc.onLoss(now, sp.size, p.delivered, p.minRTTEst(), p.lossTimeout())
	if e.unacked[sp.seq] != sp {
		return
	}
	if t := sp.twin; t != nil && !t.done && !t.sent.IsZero() {
		e.unacked[sp.seq] = t
		return
	}
	if !retryable(now, sp) {
		delete(e.unacked, sp.seq)
		return
	}
	r := &sentPacket{seq: sp.seq, pkt: sp.pkt, first: sp.first, attempts: sp.attempts + 1, path: p}
	e.unacked[sp.seq] = r
	e.rtx = append(e.rtx, r)
	p.retrans++
}

// killPath makes p DEAD and re-sends its unacked packets elsewhere.
func (e *Engine) killPath(now time.Time, p *path) {
	for _, sp := range p.sent {
		if !sp.done {
			e.lose(now, sp)
		}
	}
	p.sent, p.inflight, p.unackedSince, p.pongStreak = nil, 0, time.Time{}, 0
	p.pings = p.pings[:0]
	p.agg, p.aggSize = nil, 0
	p.noteDeath(now)
	e.setState(p, Dead, now)
	e.flush(now)
}
