package bond

import (
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

// pingRec is one PING waiting for its PONG.
type pingRec struct {
	ts   uint64
	sent time.Time
}

// sentPacket is one transmission of a sequenced IP packet.
type sentPacket struct {
	seq      uint32
	pkt      []byte
	size     int // outer datagram bytes
	path     *path
	num      uint64 // per-path packet number
	sent     time.Time
	first    time.Time   // first transmission of this seq
	attempts int         // re-sends so far
	done     bool        // acked, lost or superseded
	twin     *sentPacket // the other copy of a duplicated packet
	isTwin   bool        // second copy: no rate samples

	// delivery rate sample state at send time
	delivered     uint64
	deliveredTime time.Time
	firstSent     time.Time
	appLimited    bool
}

// path is the engine's view of one path.
type path struct {
	id     uint8
	cfg    PathConfig
	state  State
	stream bool // reliable transport (TCP/WSS): no fast retransmit, ETA penalty

	holdUntil   time.Time // speed test: not killed for missed PONGs until then
	guarded     bool      // RTT far above the best path: no new data
	pongsNeeded int       // anti-flap: PONGs to come back from DEAD (0 = PongsToAlive)
	aliveSince  time.Time // last recovery (DEAD → ALIVE after a death)
	died        bool      // was ALIVE and died at least once

	agg     []wire.Frame // small IP frames waiting to share a datagram
	aggSize int
	aggAt   time.Time
	txPkts  uint64 // datagrams sent
	twins   uint64 // duplicate copies sent

	fastProbeUntil time.Time
	rtt            rttEstimator // from PING/PONG: the path itself
	dataRTT        rttEstimator // send → ACK of data packets: queueing, ACK delay and ACK path included
	latestDataRTT  time.Duration
	cc             bbr

	nextPing   time.Time
	pings      []pingRec
	pongStreak int
	lastRecv   time.Time
	wokeAt     time.Time // last SLEEPING → ALIVE: silence counts from here
	lastData   time.Time // last data sent (sleeping decision)

	mtu        int
	probesLeft int
	nextProbe  time.Time
	probes     map[uint64]int // ts -> probe size

	nextSend time.Time // pacing
	inflight int
	bucket   tokenBucket

	sent            []*sentPacket
	nextNum         uint64
	largestAcked    uint64
	hasAcked        bool
	delivered       uint64
	appLimitedUntil uint64 // app-limited period: until delivered passes it (0 = none), as in Linux
	deliveredTime   time.Time
	firstSent       time.Time
	lastAckProgress time.Time
	unackedSince    time.Time // first data send after the last ACK progress

	txBytes, rxBytes, txData, rxData uint64
	txMeter, rxMeter, dataMeter      rateMeter
	retrans, lost                    uint64
	loss                             float64
	winSent, winLost                 uint64
	winStart                         time.Time
	used                             uint64
}

func newPath(id uint8, cfg PathConfig, now time.Time) *path {
	p := &path{id: id, cfg: cfg, state: Dead, lastRecv: now, probes: make(map[uint64]int)}
	p.bucket.setRate(cfg.RateBps, now)
	p.fastProbe(now)
	return p
}

// fastProbe makes a dead path ping at PingInterval for a while.
func (p *path) fastProbe(now time.Time) {
	p.fastProbeUntil = now.Add(FastProbeFor)
	if p.state == Dead {
		p.nextPing = now
	}
}

func (p *path) srtt() time.Duration {
	if !p.rtt.has {
		return defaultSRTT
	}
	return p.rtt.srtt
}

func (p *path) minRTTEst() time.Duration {
	if p.cc.minRTT > 0 {
		return p.cc.minRTT
	}
	return p.srtt()
}

// capped reports whether the data cap is used up.
func (p *path) capped() bool { return p.cfg.CapBytes > 0 && p.used >= p.cfg.CapBytes }

// dataClass returns the priority the scheduler uses for this path, and false
// when it may not carry data from this side at all.
func (p *path) dataClass() (Priority, bool) {
	if p.cfg.Dir == DirOff || p.capped() {
		return 0, false
	}
	if p.cfg.Dir == DirBackupOnly {
		return Backup, true
	}
	return p.cfg.Priority, true
}

// pingInterval depends on the state: fast while alive or newly probing.
func (p *path) pingInterval(now time.Time) time.Duration {
	switch p.state {
	case Sleeping:
		return SleepPingInterval
	case Dead:
		if now.Before(p.fastProbeUntil) {
			return PingInterval
		}
		return DeadProbeInterval
	}
	return PingInterval
}

func (p *path) pongTimeout() time.Duration {
	return max(2*p.srtt(), MinPongTimeout)
}

// missedPings counts outstanding pings older than the pong timeout and drops
// the ones too old to matter.
func (p *path) missedPings(now time.Time) int {
	to := p.pongTimeout()
	n := 0
	keep := p.pings[:0]
	for _, r := range p.pings {
		age := now.Sub(r.sent)
		if age >= to {
			n++
		}
		if age < 10*time.Second {
			keep = append(keep, r)
		}
	}
	p.pings = keep
	return n
}

// dropMissed removes pings past the pong timeout and returns how many.
func (p *path) dropMissed(now time.Time) int {
	to := p.pongTimeout()
	keep := p.pings[:0]
	for _, r := range p.pings {
		if now.Sub(r.sent) < to {
			keep = append(keep, r)
		}
	}
	n := len(p.pings) - len(keep)
	p.pings = keep
	return n
}

// onPong handles a PONG for ts; it returns the RTT and whether ts was known.
func (p *path) onPong(now time.Time, sent time.Time, ts uint64) {
	rtt := now.Sub(sent)
	p.rtt.sample(rtt)
	p.cc.onRTT(now, rtt)
	// Drop this ping and every older one (lost or reordered: either way not missed any more).
	keep := p.pings[:0]
	for _, r := range p.pings {
		if r.ts > ts {
			keep = append(keep, r)
		}
	}
	p.pings = keep
	p.pongStreak++
}

// lossTimeout is the age at which an unacked packet counts as lost: 9/8 of
// the largest RTT estimate (QUIC's time threshold) plus variance and ACK delay.
func (p *path) lossTimeout() time.Duration {
	base := max(p.srtt(), p.dataRTT.srtt, p.latestDataRTT)
	return base*9/8 + 4*max(p.rtt.rttvar, p.dataRTT.rttvar) + 2*AckInterval
}

// suspectAge is how long data may stay unacked before the path is SUSPECT.
func (p *path) suspectAge() time.Duration {
	return max(3*p.srtt(), MinSuspectAge)
}

// account adds a sent or received datagram to the counters.
func (p *path) account(now time.Time, n int, tx bool) {
	p.used += uint64(n)
	if tx {
		p.txBytes += uint64(n)
		p.txMeter.add(now, n)
		return
	}
	p.rxBytes += uint64(n)
	p.rxMeter.add(now, n)
}

// noteLoss updates the smoothed loss fraction once a second. A second
// without data counts as loss-free, so an idle path's figure decays instead
// of showing its last lossy value for good (phase 18: 76 % minutes after a
// download). Losses of packets sent in the previous second can outnumber
// this second's sends: the fraction is capped at 1.
func (p *path) noteLoss(now time.Time) {
	if p.winStart.IsZero() {
		p.winStart = now
	}
	if now.Sub(p.winStart) < time.Second {
		return
	}
	f := 0.0
	if p.winSent > 0 {
		f = min(float64(p.winLost)/float64(p.winSent), 1)
	}
	p.loss = 0.75*p.loss + 0.25*f
	p.winSent, p.winLost, p.winStart = 0, 0, now
}

// pruneSent drops finished packets from the front of the send list.
func (p *path) pruneSent() {
	i := 0
	for i < len(p.sent) && p.sent[i].done {
		p.sent[i] = nil
		i++
	}
	p.sent = p.sent[i:]
}

// becomeAlive resets congestion state after the path was dead.
func (p *path) becomeAlive(now time.Time) {
	p.state = Alive
	if p.died {
		p.aliveSince = now // a recovery: anti-flap watches it
	}
	p.cc.reset()
	p.nextSend = now
	p.lastAckProgress = now
	p.unackedSince = time.Time{}
	p.hasAcked = false
	p.lastData = now
	if p.mtu == 0 && p.probesLeft == 0 {
		p.probesLeft, p.nextProbe = 1+probeRetries, now
	}
}

func (p *path) stats(now time.Time) PathStats {
	return PathStats{
		ID: p.id, State: p.state.String(), Priority: p.cfg.Priority.String(), Dir: p.cfg.Dir.String(),
		SRTT: p.rtt.srtt, Jitter: p.rtt.rttvar, MinRTT: p.minRTTEst(), Loss: p.loss,
		BtlBw: p.cc.bw(), Inflight: p.inflight, Cwnd: p.cc.cwnd(p.minRTTEst()),
		CC: p.cc.mode.String(), Pacing: uint64(p.cc.pacingRate(p.srtt())), DataRTT: p.dataRTT.srtt,
		TxBytes: p.txBytes, RxBytes: p.rxBytes, TxData: p.txData, RxData: p.rxData,
		TxRate: p.txMeter.get(now), RxRate: p.rxMeter.get(now), MTU: p.mtu,
		Retransmit: p.retrans, Lost: p.lost, Capped: p.capped(), Used: p.used,
		Stream: p.stream, Guarded: p.guarded, TxPkts: p.txPkts, Twins: p.twins, PongsNeeded: p.pongsToAlive(),
	}
}
