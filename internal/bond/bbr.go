package bond

import "time"

// BBR-lite constants (decisions.md phase 04).
const (
	startupGain   = 2.89 // pacing gain in STARTUP
	probeBWGain   = 2.0  // cwnd gain in every mode but PROBE_RTT: in-flight cap 2 × BDP (BBRv3)
	initialCwnd   = 10 * 1400
	minCwnd       = 4 * 1500 // PROBE_RTT floor
	fullBWGrowth  = 1.25
	fullBWRounds  = 3
	defaultSRTT   = 100 * time.Millisecond
	minRTTWindow  = 10 * time.Second
	bwRoundWindow = 10

	// Loss response (BBRv2/v3, decisions.md phase 07b).
	lossThresh        = 0.02            // a round losing more than this share of its data is "too much loss"
	startupLossEvents = 6               // ...and at least this many lost packets end STARTUP (BBRv3 full_loss_cnt)
	probeLossEvents   = 3               // ...or this many cut inflight_hi in the other modes
	inflightHiBeta    = 0.7             // inflight_hi cut on loss
	startupAppBound   = 2 * time.Second // after this long in STARTUP, app-limited rounds count for the full-pipe check
)

var cycleGains = [8]float64{1.25, 0.75, 1, 1, 1, 1, 1, 1}

type bbrMode uint8

const (
	bbrStartup bbrMode = iota
	bbrDrain
	bbrProbeBW
	bbrProbeRTT
)

func (m bbrMode) String() string {
	return [...]string{"startup", "drain", "probe_bw", "probe_rtt"}[m]
}

// probeRTTHold is how long PROBE_RTT keeps the path drained (plus one min RTT,
// so the PING sent when the queue is empty gets its PONG).
const probeRTTHold = 200 * time.Millisecond

// bbr is the per-path congestion controller: windowed max delivery rate
// (btlbw), windowed min RTT, pacing rate and in-flight cap.
type bbr struct {
	mode       bbrMode
	maxBW      roundMax
	round      uint64
	nextRound  uint64 // delivered count that ends the current round
	fullBW     uint64
	fullCnt    int
	cycle      int
	cycleStart time.Time

	startupPace float64 // STARTUP pacing never goes down (BBR: until the pipe is full)
	startupAt   time.Time

	roundAt         time.Time // when the current round started
	roundStart      uint64    // delivered count when the current round started
	roundLost       uint64    // bytes lost in the current round
	roundLossEvents int
	lossyRounds     int // consecutive lossy rounds
	inflightHi      int // in-flight bound learned from loss, bytes (0 = none)

	minRTT       time.Duration
	minRTTAt     time.Time
	probeRTTDone time.Time     // zero until the path drained in PROBE_RTT
	probeRTTDue  bool          // min RTT expired; the engine starts PROBE_RTT when no other path is in it
	probeMin     time.Duration // lowest RTT seen since the path drained in PROBE_RTT
	priorMode    bbrMode

	// Windowed min of the data RTT (send → ACK of first transmissions): the
	// RTT the BDP and the in-flight cap use. A PING is answered at once and
	// rides small grants, a data packet waits for the uplink grant and the
	// ACK interval: on LTE uploads the data RTT stayed ~2 × the ping min, so
	// a cap of 2 × btlbw × ping min held one data RTT of the current rate,
	// the rate could never grow and the cap sat at its floor (2026-11-26:
	// S23 and modem at ~2.5 Mb/s each). BBR takes min_rtt from data ACKs too.
	dataMin      time.Duration
	dataMinAt    time.Time
	dataProbeMin time.Duration // lowest data RTT since the path drained in PROBE_RTT

	// ACK aggregation (Linux BBR extra_acked): bytes acked beyond btlbw × the
	// time of an aggregation epoch, windowed max over 10 rounds, added to the
	// in-flight cap. The receiver ACKs every AckEvery packets or AckInterval
	// and an LTE uplink waits for grants only under load, so data RTT under
	// load exceeded every idle min the cap was sized by (2026-11-26: the Dell
	// modem crawled from 4 to 10 Mb/s in 7 s).
	extraAcked    roundMax
	ackEpochAt    time.Time
	ackEpochAcked uint64
}

const (
	ackEpochReset  = 1 << 20                // bytes: a long epoch starts over (Linux bbr_ack_epoch_acked_reset_thresh)
	extraAckedTime = 100 * time.Millisecond // extra_acked is at most this long at btlbw (Linux bbr_extra_acked_max_us)
)

// onAcked updates the ACK aggregation estimate for acked bytes; cwnd is
// the path's current in-flight cap.
func (b *bbr) onAcked(now time.Time, acked, cwnd int) {
	bw := b.bw()
	if bw == 0 || acked <= 0 {
		return
	}
	if b.ackEpochAt.IsZero() {
		b.ackEpochAt = now
	}
	expected := uint64(float64(bw) * now.Sub(b.ackEpochAt).Seconds())
	if b.ackEpochAcked <= expected || b.ackEpochAcked+uint64(acked) >= ackEpochReset {
		b.ackEpochAcked, b.ackEpochAt, expected = 0, now, 0
	}
	b.ackEpochAcked += uint64(acked)
	extra := min(b.ackEpochAcked-expected, uint64(cwnd), uint64(float64(bw)*extraAckedTime.Seconds()))
	b.extraAcked.add(b.round, extra)
}

// onRTT feeds a path RTT sample (from PING/PONG). The min RTT filter expires
// after minRTTWindow; then PROBE_RTT drains the path to measure it again
// (one path at a time, so the bond never loses several paths at once). Until
// that measurement the old minimum stays: a sample taken under load includes
// the queue and would inflate the in-flight cap.
func (b *bbr) onRTT(now time.Time, rtt time.Duration) {
	if b.mode == bbrProbeRTT && !b.probeRTTDone.IsZero() && (b.probeMin == 0 || rtt < b.probeMin) {
		b.probeMin = rtt
	}
	if b.minRTT == 0 || rtt <= b.minRTT {
		b.minRTT, b.minRTTAt = rtt, now
		return
	}
	if now.Sub(b.minRTTAt) > minRTTWindow && b.mode != bbrProbeRTT {
		b.probeRTTDue = true
	}
}

// onDataRTT feeds the RTT of an acked first transmission (dataMin). Like
// the ping filter it expires after minRTTWindow and then asks for PROBE_RTT,
// whose drained samples replace it: a sample taken under load includes the
// queue and would inflate the in-flight cap.
func (b *bbr) onDataRTT(now time.Time, rtt time.Duration) {
	if b.mode == bbrProbeRTT && !b.probeRTTDone.IsZero() && (b.dataProbeMin == 0 || rtt < b.dataProbeMin) {
		b.dataProbeMin = rtt
	}
	if b.dataMin == 0 || rtt <= b.dataMin {
		b.dataMin, b.dataMinAt = rtt, now
		return
	}
	if now.Sub(b.dataMinAt) > minRTTWindow && b.mode != bbrProbeRTT {
		b.probeRTTDue = true
	}
}

// bdpRTT is the RTT of the BDP: the data RTT min, never below the ping min
// (the path's own round trip), or the ping min before any data sample.
func (b *bbr) bdpRTT(minRTT time.Duration) time.Duration { return max(minRTT, b.dataMin) }

// enterProbeRTT starts draining the path to measure its min RTT.
func (b *bbr) enterProbeRTT() {
	b.priorMode, b.mode, b.probeRTTDone, b.probeRTTDue, b.probeMin = b.mode, bbrProbeRTT, time.Time{}, false, 0
	b.dataProbeMin = 0
}

// update advances PROBE_RTT. It returns true when the path just drained
// (the caller sends a PING at once).
func (b *bbr) update(now time.Time, inflight int) bool {
	if b.mode != bbrProbeRTT {
		return false
	}
	if b.probeRTTDone.IsZero() {
		if inflight <= b.cwnd(b.minRTT) {
			b.probeRTTDone = now.Add(probeRTTHold + b.minRTT)
			return true
		}
		return false
	}
	if !now.Before(b.probeRTTDone) {
		if b.probeMin > 0 {
			b.minRTT = b.probeMin
		}
		b.minRTTAt = now
		if b.dataProbeMin > 0 {
			b.dataMin = b.dataProbeMin
		}
		b.dataMinAt = now
		b.mode = b.priorMode
		if b.mode == bbrProbeBW {
			b.cycle, b.cycleStart = 2, now
		}
	}
	return false
}

func (b *bbr) reset() { *b = bbr{} }

// bw is the bottleneck bandwidth estimate in bytes/s (0 = no sample yet).
func (b *bbr) bw() uint64 { return b.maxBW.get() }

func (b *bbr) gains() (pacing, cwnd float64) {
	switch b.mode {
	case bbrStartup:
		return startupGain, probeBWGain
	case bbrDrain:
		return 1 / startupGain, probeBWGain
	case bbrProbeRTT:
		return 1, 0
	}
	return cycleGains[b.cycle], probeBWGain
}

// rate returns the bottleneck rate estimate, or initialCwnd/srtt before the
// first sample.
func (b *bbr) rate(srtt time.Duration) float64 {
	if bw := b.bw(); bw > 0 {
		return float64(bw)
	}
	if srtt <= 0 {
		srtt = defaultSRTT
	}
	return initialCwnd / srtt.Seconds()
}

// pacingRate is gain × bottleneck rate, never below the initial window per
// RTT (a path carrying only TCP ACKs must not throttle them), and never
// lower than before during STARTUP.
func (b *bbr) pacingRate(srtt time.Duration) float64 {
	g, _ := b.gains()
	if srtt <= 0 {
		srtt = defaultSRTT
	}
	r := max(g*b.rate(srtt), initialCwnd/srtt.Seconds())
	if b.mode == bbrStartup {
		b.startupPace = max(b.startupPace, r)
		return b.startupPace
	}
	return r
}

func (b *bbr) bdp(minRTT time.Duration) float64 {
	rtt := b.bdpRTT(minRTT)
	if b.bw() == 0 || rtt <= 0 {
		return initialCwnd
	}
	return float64(b.bw()) * rtt.Seconds()
}

func (b *bbr) cwnd(minRTT time.Duration) int {
	_, g := b.gains()
	if b.mode == bbrProbeRTT {
		// Half a BDP (BBRv2/v3): the queue drains, the path keeps sending.
		return max(int(b.bdp(minRTT)/2), minCwnd)
	}
	if b.bw() == 0 {
		return initialCwnd
	}
	c := int(g * b.bdp(minRTT))
	if b.mode == bbrStartup || (b.mode == bbrProbeBW && b.cycle == 0) {
		c += int(b.extraAcked.get()) // only while probing up: a path at its rate keeps a short queue
	}
	if b.inflightHi > 0 {
		c = min(c, b.inflightHi)
	}
	return max(c, initialCwnd)
}

// endRound closes the current round and runs its loss response. A lossy
// round also moves the bandwidth window on without a sample: app-limited
// samples never lower the estimate, so a path whose capacity fell while it
// was app-limited (a bond hands each path only its share) kept its old rate
// for good. Loss-free app-limited rounds keep it, as in BBR.
func (b *bbr) endRound(now time.Time, delivered uint64, minRTT time.Duration) {
	b.nextRound = delivered
	b.round++
	b.roundAt = now
	if b.onRoundEnd(delivered, minRTT) {
		b.lossyRounds++
	} else {
		b.lossyRounds = 0
	}
	if b.lossyRounds >= lossyRoundsToAge {
		b.maxBW.advance(b.round)
	}
}

// lossyRoundsToAge consecutive lossy rounds age the bandwidth window; a
// single one is usually a cellular delivery stall.
const lossyRoundsToAge = 2

// lossRoundFloor is the shortest loss-only round: shorter delivery stalls of
// cellular links are not a capacity change.
const lossRoundFloor = time.Second

// onLoss counts one lost packet of size bytes in the current round. Rounds
// normally end on ACKs; when a path loses (nearly) everything no ACK ends
// one, so a round that has lasted 2 loss timeouts (at least lossRoundFloor) ends here:
// inflight_hi is cut and the stale bandwidth estimate ages out (phase 18:
// a path whose capacity collapsed kept pacing at its old rate).
func (b *bbr) onLoss(now time.Time, size int, delivered uint64, minRTT, lossTimeout time.Duration) {
	b.roundLost += uint64(size)
	b.roundLossEvents++
	if b.roundAt.IsZero() {
		b.roundAt = now
		return
	}
	if now.Sub(b.roundAt) > max(2*lossTimeout, lossRoundFloor) {
		b.endRound(now, delivered, minRTT)
	}
}

// undoLoss takes back one loss of size bytes counted in the current round
// (its late ACK came: spuriousLoss).
func (b *bbr) undoLoss(size int) {
	b.roundLost -= min(uint64(size), b.roundLost)
	if b.roundLossEvents > 0 {
		b.roundLossEvents--
	}
}

// onRoundEnd applies the loss response of the round that just ended:
// too much loss ends STARTUP and bounds the in-flight data (inflight_hi);
// a loss-free round in the probing phase raises the bound again. It
// returns whether the round was congested: lossy with at least
// probeLossEvents losses (0.5 % random loss made every small round lossy).
func (b *bbr) onRoundEnd(delivered uint64, minRTT time.Duration) bool {
	got := delivered - b.roundStart
	lossy := b.roundLost > 0 && float64(b.roundLost) > lossThresh*float64(got+b.roundLost)
	switch {
	case lossy && b.mode == bbrStartup && b.roundLossEvents >= startupLossEvents:
		b.inflightHi = max(int(b.bdp(minRTT)), int(got), minCwnd)
		b.mode = bbrDrain
	case lossy && b.mode != bbrStartup && b.roundLossEvents >= probeLossEvents:
		hi := b.inflightHi
		if hi == 0 {
			hi = b.cwnd(minRTT)
		}
		b.inflightHi = max(int(got), int(inflightHiBeta*float64(hi)), minCwnd)
	case !lossy && b.inflightHi > 0 && b.mode == bbrProbeBW && b.cycle == 0:
		b.inflightHi += b.inflightHi / 4
		if float64(b.inflightHi) >= probeBWGain*b.bdp(minRTT) {
			b.inflightHi = 0 // no longer binding
		}
	}
	congested := lossy && b.roundLossEvents >= probeLossEvents // not one random loss in a small round
	b.roundStart, b.roundLost, b.roundLossEvents = delivered, 0, 0
	return congested
}

// onAck feeds one delivery rate sample. pktDelivered is the path's delivered
// count when the acked packet was sent, delivered the count now.
func (b *bbr) onAck(now time.Time, rate float64, appLimited bool, pktDelivered, delivered uint64, inflight int, minRTT time.Duration) {
	if b.startupAt.IsZero() {
		b.startupAt = now
	}
	roundEnd := false
	if pktDelivered >= b.nextRound {
		b.endRound(now, delivered, minRTT)
		roundEnd = true
	}
	if rate > 0 && (!appLimited || uint64(rate) >= b.bw()) {
		b.maxBW.add(b.round, uint64(rate))
	} else if bw := b.bw(); appLimited && bw > 0 && 2*rate >= float64(bw) && b.roundLost == 0 && b.lossyRounds == 0 {
		// A loss-free app-limited sample of a busy path (at least half the
		// estimate) keeps the estimate fresh. Without it the window stood
		// still while the path's traffic stayed just below its rate (20–30
		// rounds), and then one low sample after a cellular stall, or two
		// lossy rounds, expired it at once: 53 → 11 Mb/s, once to 0 (cap at
		// its floor). Lossy rounds and paths whose rate fell still age it.
		b.maxBW.add(b.round, bw)
	}
	switch b.mode {
	case bbrStartup:
		if roundEnd && (!appLimited || now.Sub(b.startupAt) >= startupAppBound) {
			if bw := b.bw(); float64(bw) >= float64(b.fullBW)*fullBWGrowth {
				b.fullBW, b.fullCnt = bw, 0
			} else if b.fullCnt++; b.fullCnt >= fullBWRounds {
				b.mode = bbrDrain
			}
		}
		if b.mode != bbrDrain {
			break
		}
		fallthrough
	case bbrDrain:
		if float64(inflight) <= b.bdp(minRTT) {
			b.mode, b.cycle, b.cycleStart = bbrProbeBW, 2, now
		}
	case bbrProbeBW:
		if minRTT <= 0 {
			minRTT = defaultSRTT
		}
		if now.Sub(b.cycleStart) >= minRTT {
			b.cycle = (b.cycle + 1) % len(cycleGains)
			b.cycleStart = now
		}
	}
}
