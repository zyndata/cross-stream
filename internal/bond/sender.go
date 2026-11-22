package bond

import (
	"slices"
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

// saturationWait is the shortest queue wait after which normal paths count
// as saturated even if only pacing holds them back.
const saturationWait = 100 * time.Millisecond

// pacingSlack lets a path catch up after a late timer.
const pacingSlack = 2 * time.Millisecond

// frameSize is the encoded size of a frame including its TLV header.
func frameSize(f wire.Frame) int {
	switch f := f.(type) {
	case *wire.IP:
		return wire.FrameHeaderLen + 4 + len(f.Packet)
	case *wire.Ack:
		return wire.FrameHeaderLen + 12
	case *wire.Ping:
		return wire.FrameHeaderLen + 8 + f.Padding
	case *wire.Pong:
		return wire.FrameHeaderLen + 10
	}
	b, _ := wire.AppendFrame(nil, f)
	return len(b)
}

// transmit sends frames as one datagram on p, together with the small IP
// frames waiting for aggregation on p when they fit.
func (e *Engine) transmit(now time.Time, p *path, frames ...wire.Frame) {
	size := DatagramOverhead
	for _, f := range frames {
		size += frameSize(f)
	}
	if len(p.agg) > 0 {
		if p.aggSize+size-DatagramOverhead <= p.aggLimit() {
			frames = append(p.agg, frames...)
			size += p.aggSize - DatagramOverhead
			p.agg, p.aggSize = nil, 0
		} else {
			e.flushAgg(now, p)
		}
	}
	p.account(now, size, true)
	p.bucket.take(now, size)
	p.txPkts++
	_ = e.cfg.Transmit(p.id, frames)
}

// flush sends queued re-sends and packets while paths allow it.
func (e *Engine) flush(now time.Time) {
	for len(e.rtx) > 0 {
		sp := e.rtx[0]
		if sp.done || e.unacked[sp.seq] != sp || (!IsReliable(sp.pkt) && now.Sub(sp.first) >= RetransmitMaxAge) {
			if e.unacked[sp.seq] == sp {
				delete(e.unacked, sp.seq)
			}
			e.rtx = e.rtx[1:]
			continue
		}
		p := e.pick(now, IPOverhead+len(sp.pkt), sp.path, 0, false)
		if p == nil {
			break
		}
		e.rtx = e.rtx[1:]
		e.sendPacket(now, p, sp, false)
	}
	maxWait := e.queueMaxDelay()
	for len(e.rtq) > 0 { // priority packets first (critical; real-time in min_latency)
		q := e.rtq[0]
		if now.Sub(q.at) >= 2*maxWait {
			e.rtq[0] = queued{}
			e.rtq = e.rtq[1:]
			e.dropped++
			continue
		}
		c := classify(q.pkt)
		p := e.pick(now, IPOverhead+len(q.pkt), nil, now.Sub(q.at), c.realtime && e.mode == MinLatency)
		if p == nil {
			break
		}
		e.rtq[0] = queued{}
		e.rtq = e.rtq[1:]
		e.sendNew(now, p, q.pkt, c, false)
	}
	if len(e.rtq) == 0 {
		e.rtq = nil
	}
	for len(e.queue) > 0 || len(e.relq) > 0 {
		// The IP queue and the reliable queue take turns (one packet each),
		// so neither PEP streams nor packet-mode flows starve the other.
		if len(e.relq) > 0 && (len(e.queue) == 0 || e.relTurn) {
			q := e.relq[0]
			p := e.pick(now, IPOverhead+len(q.pkt), nil, now.Sub(q.at), false)
			if p == nil {
				break
			}
			e.popReliable()
			e.relTurn = false
			e.sendNew(now, p, q.pkt, pktClass{}, e.appLimited(now, p, IPOverhead+len(q.pkt)))
			continue
		}
		q := e.queue[0]
		wait := now.Sub(q.at)
		if e.dropHead(now, wait, maxWait) {
			e.queue[0] = queued{}
			e.queue = e.queue[1:]
			e.dropped++
			continue
		}
		c := classify(q.pkt)
		p := e.pick(now, IPOverhead+len(q.pkt), nil, wait, false)
		if p == nil {
			break
		}
		e.queue[0] = queued{}
		e.queue = e.queue[1:]
		e.relTurn = len(e.relq) > 0
		e.sendNew(now, p, q.pkt, c, e.appLimited(now, p, IPOverhead+len(q.pkt)))
	}
	if len(e.queue) == 0 {
		e.queue = nil
	}
	if len(e.queue) == 0 && len(e.relq) == 0 && len(e.rtx) == 0 {
		for _, p := range e.order {
			if p.inflight+max(p.mtu, MinPathMTU) <= p.cc.cwnd(p.minRTTEst()) {
				p.markAppLimited()
			}
		}
	}
	e.noteRoom()
}

// sendNew numbers a new packet, sends it on p and its copy, if it gets one.
func (e *Engine) sendNew(now time.Time, p *path, pkt []byte, c pktClass, appLimited bool) {
	sp := &sentPacket{seq: e.nextSeq, pkt: pkt, first: now}
	e.nextSeq++
	e.unacked[sp.seq] = sp
	e.sendPacket(now, p, sp, appLimited)
	if e.wantTwin(now, sp.pkt, c) {
		if t := e.pickTwin(now, p, IPOverhead+len(sp.pkt), c.critical); t != nil {
			e.sendTwin(now, t, sp)
		}
	}
}

// markAppLimited starts an app-limited period: rate samples of packets sent
// until the data now in flight is delivered do not lower the bandwidth
// estimate (they span time the path had nothing to send).
func (p *path) markAppLimited() {
	p.appLimitedUntil = max(p.delivered+uint64(p.inflight), 1)
}

// dropHead decides whether the queue head that waited wait is dropped
// (CoDel-like): past maxWait one packet per RTT (maxWait/1.5), so inner TCP
// gets one loss signal per round trip instead of losing a whole burst with
// its retransmissions; past 2 × maxWait every stale packet.
func (e *Engine) dropHead(now time.Time, wait, maxWait time.Duration) bool {
	switch {
	case wait >= 2*maxWait:
		return true
	case wait >= maxWait && !now.Before(e.nextDrop):
		e.nextDrop = now.Add(maxWait * 2 / 3)
		return true
	}
	return false
}

// queueMaxDelay is how long a packet may wait for a path: 1.5 × the largest
// path RTT (a path at its in-flight cap frees room once per RTT), within
// QueueMaxDelay..QueueMaxDelayCap.
func (e *Engine) queueMaxDelay() time.Duration {
	var hi time.Duration
	for _, p := range e.order {
		if p.state != Dead && p.rtt.has {
			hi = max(hi, p.rtt.srtt)
		}
	}
	return min(max(hi*3/2, QueueMaxDelay), QueueMaxDelayCap)
}

// appLimited reports, right after the packet about to go on p left its
// queue, whether p is limited by the traffic it gets rather than by its own
// pacing and in-flight cap: at most one packet still waits (IP queue, then
// reliable queue), or p could take the next two too. Samples of such packets
// cannot lower the bandwidth estimate. A path that this packet of sent
// bytes fills to its in-flight cap is never app-limited, however short the
// queue (see cwndFull).
func (e *Engine) appLimited(now time.Time, p *path, sent int) bool {
	if p.cwndFull(sent) {
		return false
	}
	size, next := e.queuedSizes()
	if next == 0 {
		return true
	}
	gap := time.Duration(float64(size) / p.cc.pacingRate(p.srtt()) * float64(time.Second))
	return p.inflight+size+next <= p.cc.cwnd(p.minRTTEst()) && !later(p.nextSend, now.Add(-pacingSlack)).Add(gap).After(now)
}

// cwndFull reports whether p, after sent more bytes, has no room for another
// full packet while a queue builds on it (smoothed data or ping RTT above
// 3 × min RTT: with 60 ± 20 ms jitter the min RTT is ~40 ms and the average
// already 2 ×; a path whose capacity fell runs at 4 × and more).
// Samples of packets sent then measure the path, not its traffic: in a bond
// the other paths drain the queue, so with TCP traffic every sample looked
// app-limited and the STARTUP estimate stayed for the whole session, also
// after the path's capacity fell (phase 18). Without the queue condition a
// lightly loaded path that touched its cap in a jitter burst sampled its
// traffic rate, the estimate sank and the bond spilled onto secondary paths.
func (p *path) cwndFull(sent int) bool {
	minRTT := p.minRTTEst()
	return p.inflight+sent+MinPathMTU > p.cc.cwnd(minRTT) && max(p.dataRTT.srtt, p.srtt()) > 3*minRTT
}

// sendPacket transmits sp on p and records it for ACKs and loss detection.
// For a re-send, sp.path is the path it was lost on and is replaced.
func (e *Engine) sendPacket(now time.Time, p *path, sp *sentPacket, appLimited bool) {
	size := IPOverhead + len(sp.pkt)
	if p.inflight == 0 {
		p.firstSent, p.deliveredTime = now, now
	}
	sp.path, sp.num, sp.sent, sp.size = p, p.nextNum, now, size
	p.nextNum++
	full := !appLimited && p.cwndFull(size)
	p.inflight += size // before markAppLimited: the period covers this packet too
	if appLimited {
		p.markAppLimited()
	} else if full {
		p.appLimitedUntil = 0 // the pipe is as full as allowed: the period is over
	}
	sp.delivered, sp.deliveredTime, sp.firstSent, sp.appLimited = p.delivered, p.deliveredTime, p.firstSent, p.appLimitedUntil != 0
	p.sent = append(p.sent, sp)
	if p.unackedSince.IsZero() {
		p.unackedSince = now
	}
	p.nextSend = later(p.nextSend, now.Add(-pacingSlack)).Add(
		time.Duration(float64(size) / p.cc.pacingRate(p.srtt()) * float64(time.Second)))
	p.txData += uint64(len(sp.pkt))
	p.lastData = now
	p.winSent++
	if p.state == Sleeping {
		e.setState(p, Alive, now)
	}
	f := &wire.IP{Seq: sp.seq, Packet: sp.pkt}
	if e.aggregates(sp.pkt) {
		e.addAgg(now, p, f)
		return
	}
	e.transmit(now, p, f)
}

// pick chooses the path for a packet of size outer bytes, or nil if none can
// send now. avoid (the path a re-send was lost on) is used only when no
// other path of the same tier can send. headWait is how long the packet has
// waited in the queue. rt (min_latency real-time packet) picks the lowest RTT.
// Guarded paths (RTT far above the best) get no new data.
func (e *Engine) pick(now time.Time, size int, avoid *path, headWait time.Duration, rt bool) *path {
	var tiers [3][]*path // normal, secondary, backup
	for _, p := range e.order {
		cls, ok := p.dataClass()
		if !ok || p.guarded || (p.mtu > 0 && size > p.mtu) {
			continue
		}
		switch {
		case p.state == Alive, p.state == Sleeping && cls == Backup:
		default:
			continue
		}
		switch cls {
		case Normal:
			tiers[0] = append(tiers[0], p)
		case Secondary:
			tiers[1] = append(tiers[1], p)
		default:
			tiers[2] = append(tiers[2], p)
		}
	}
	first, overflow := tiers[0], tiers[1]
	switch {
	case len(first) == 0 && len(tiers[1]) > 0:
		first, overflow = tiers[1], nil
	case len(first) == 0:
		first, overflow = tiers[2], nil
	}
	small := !rt && !e.noAggregate && size-IPOverhead <= SmallPacket
	if best := e.bestSendable(now, first, size, avoid, rt, small); best != nil {
		return best
	}
	if len(overflow) > 0 && e.overflowAllowed(now, first) && saturated(now, first, size, headWait) {
		return e.bestSendable(now, overflow, size, avoid, rt, small)
	}
	return nil
}

func (p *path) sendable(now time.Time, size int) bool {
	return p.inflight+size <= p.cc.cwnd(p.minRTTEst()) && !now.Before(p.nextSend) && p.bucket.allow(now, size)
}

// bestSendable returns the path with the earliest expected delivery among
// those that may send now; avoid only if it is the only one.
func (e *Engine) bestSendable(now time.Time, ps []*path, size int, avoid *path, rt, small bool) *path {
	var best *path
	var bestETA float64
	for _, p := range ps {
		// A small packet may join a path's open batch without a pacing slot
		// (the batch leaves as one datagram); in-flight cap and rate limit
		// still apply. Otherwise each packet of a burst lands on another
		// path and starts its own batch.
		joins := small && len(p.agg) > 0 && p.aggSize+size-DatagramOverhead <= p.aggLimit() &&
			p.inflight+size <= p.cc.cwnd(p.minRTTEst()) && p.bucket.allow(now, size)
		if p == avoid || (!joins && !p.sendable(now, size)) {
			continue
		}
		eta := e.eta(p, size, rt)
		if joins {
			eta-- // an open batch wins: one datagram less, it leaves within AggregateWait
		}
		if best == nil || eta < bestETA {
			best, bestETA = p, eta
		}
	}
	if best == nil && avoid != nil && slices.Contains(ps, avoid) && avoid.sendable(now, size) {
		return avoid
	}
	return best
}

// StreamETAPenalty makes the scheduler prefer UDP paths over TCP/WSS ones.
const StreamETAPenalty = 20 * time.Millisecond

// eta is the expected delivery time of a packet of size bytes sent on p now:
// half the RTT plus the time to drain what is in flight (max_throughput),
// the full RTT plus that (min_latency: low-RTT paths win more often) or just
// the RTT (min_latency real-time packets).
func (e *Engine) eta(p *path, size int, rt bool) float64 {
	rtt := p.srtt().Seconds()
	drain := float64(p.inflight+size) / p.cc.rate(p.srtt())
	var eta float64
	switch {
	case rt:
		eta = rtt
	case e.mode == MinLatency:
		eta = rtt + drain
	default:
		eta = rtt/2 + drain
	}
	if p.stream {
		eta += StreamETAPenalty.Seconds()
	}
	return eta
}

// saturated reports whether the normal paths cannot take more: all at their
// in-flight cap (not in STARTUP or PROBE_RTT) or rate limit, or
// the queue head waited one RTT of the slowest normal path (at least
// saturationWait): TCP window bursts do not spill onto secondary paths,
// sustained overload does.
func saturated(now time.Time, ps []*path, size int, headWait time.Duration) bool {
	var rtt time.Duration
	for _, p := range ps {
		rtt = max(rtt, p.srtt())
	}
	if headWait >= max(saturationWait, rtt) {
		return true
	}
	for _, p := range ps {
		ramping := p.cc.mode == bbrStartup || p.cc.mode == bbrProbeRTT // temporary, not full
		capped := p.inflight+size > p.cc.cwnd(p.minRTTEst()) && !ramping
		if !capped && p.bucket.allow(now, size) {
			return false
		}
	}
	return true
}

func (e *Engine) overflowAllowed(now time.Time, normal []*path) bool {
	if e.overflow == 0 {
		return true
	}
	var sum uint64
	for _, p := range normal {
		sum += p.txMeter.get(now)
	}
	return sum < e.overflow
}
