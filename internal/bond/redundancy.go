package bond

import (
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

// Redundancy settings (architecture.md "Redundancy").
const (
	// AutoLoss and AutoJitter turn auto redundancy on (any normal path).
	AutoLoss   = 0.02
	AutoJitter = 50 * time.Millisecond
	// AutoCalm is how long auto redundancy stays on after the last trigger.
	AutoCalm = 30 * time.Second
	// SmallPacket is the largest packet min_latency mode always duplicates
	// and the largest packet the sender aggregates.
	SmallPacket = 256
	// AutoMaxPacket is the largest real-time packet auto redundancy
	// duplicates: calls and game state, not bulk UDP (QUIC video).
	AutoMaxPacket = 512
)

// Mode is the hub mode (values match wire.ModeMaxThroughput...).
type Mode uint8

// Hub modes.
const (
	MaxThroughput = Mode(wire.ModeMaxThroughput)
	MinLatency    = Mode(wire.ModeMinLatency)
)

func (m Mode) String() string {
	if m == MinLatency {
		return "min_latency"
	}
	return "max_throughput"
}

// Redundancy is the duplication setting (values match wire.RedundancyOff...).
type Redundancy uint8

// Redundancy settings.
const (
	RedundancyOff  = Redundancy(wire.RedundancyOff)
	RedundancyAuto = Redundancy(wire.RedundancyAuto)
	RedundancyOn   = Redundancy(wire.RedundancyOn)
)

func (r Redundancy) String() string {
	switch r {
	case RedundancyOff:
		return "off"
	case RedundancyOn:
		return "on"
	}
	return "auto"
}

// pktClass describes an IP packet for duplication and scheduling.
type pktClass struct {
	critical bool // DNS, TCP SYN/FIN/RST: always duplicated
	realtime bool // UDP or ICMP (not DNS): min_latency treats it as real-time
}

// classify inspects an IPv4 header (anything else is plain bulk data).
func classify(pkt []byte) pktClass {
	if len(pkt) < 20 || pkt[0]>>4 != 4 {
		return pktClass{}
	}
	ihl := int(pkt[0]&0x0F) * 4
	if ihl < 20 || len(pkt) < ihl {
		return pktClass{}
	}
	firstFrag := pkt[6]&0x1F == 0 && pkt[7] == 0
	l4 := pkt[ihl:]
	switch pkt[9] {
	case 1: // ICMP
		return pktClass{realtime: true}
	case 17: // UDP
		if firstFrag && len(l4) >= 4 {
			src, dst := int(l4[0])<<8|int(l4[1]), int(l4[2])<<8|int(l4[3])
			if src == 53 || dst == 53 {
				return pktClass{critical: true}
			}
		}
		return pktClass{realtime: true}
	case 6: // TCP
		if firstFrag && len(l4) >= 14 && l4[13]&0x07 != 0 { // FIN, SYN or RST
			return pktClass{critical: true}
		}
	}
	return pktClass{}
}

// SetRedundancy sets the duplication setting.
func (e *Engine) SetRedundancy(r Redundancy) {
	e.mu.Lock()
	e.redundancy = r
	e.mu.Unlock()
}

// SetMode switches between max_throughput and min_latency: min_latency
// duplicates small and real-time packets, sends real-time packets on the
// lowest-RTT path and delivers received real-time packets without waiting
// for gaps before them.
func (e *Engine) SetMode(m Mode) {
	e.mu.Lock()
	e.mode = m
	e.recv.passRealtime = m == MinLatency
	e.mu.Unlock()
}

// redundant reports whether every packet is duplicated now.
func (e *Engine) redundant(now time.Time) bool {
	switch e.redundancy {
	case RedundancyOn:
		return true
	case RedundancyAuto:
		return now.Before(e.redundantUntil)
	}
	return false
}

// updateRedundancy keeps auto redundancy on while a normal path is lossy or
// jittery, and for AutoCalm after.
func (e *Engine) updateRedundancy(now time.Time) {
	for _, p := range e.order {
		if p.state != Alive && p.state != Suspect {
			continue
		}
		if cls, ok := p.dataClass(); !ok || cls != Normal {
			continue
		}
		if p.loss > AutoLoss || (p.rtt.has && p.rtt.rttvar > AutoJitter) {
			e.redundantUntil = now.Add(AutoCalm)
			return
		}
	}
}

// wantTwin reports whether a new packet gets a second copy: critical
// packets always, everything with redundancy on, small and real-time packets
// in min_latency mode, real-time (UDP/ICMP) packets up to AutoMaxPacket while
// auto redundancy is triggered. Auto leaves TCP and bulk UDP alone: their
// loss is mostly the bond's own congestion (which also triggers auto),
// duplicating TCP ACKs cost ~15 % of a bonded download in the simulation,
// and bulk UDP copies would double the relays' data use.
func (e *Engine) wantTwin(now time.Time, pkt []byte, c pktClass) bool {
	switch {
	case c.critical, e.redundancy == RedundancyOn:
		return true
	case e.mode == MinLatency:
		return c.realtime || len(pkt) <= SmallPacket
	case e.redundant(now):
		return c.realtime && len(pkt) <= AutoMaxPacket
	}
	return false
}

// pickTwin chooses the path for a second copy: alive, normal or secondary
// (never backup), not primary, lowest ETA. Copies need room in the rate
// limit and up to twice the path's in-flight cap, not a pacing slot: a path
// that mostly carries copies gets few rate samples (copies give none), so
// its own cap underestimates what it can take. Critical copies (tiny) only
// need the path alive.
func (e *Engine) pickTwin(now time.Time, primary *path, size int, critical bool) *path {
	var best *path
	var bestETA float64
	for _, p := range e.order {
		if p == primary || p.state != Alive || (p.mtu > 0 && size > p.mtu) {
			continue
		}
		if cls, ok := p.dataClass(); !ok || cls == Backup {
			continue
		}
		if !critical && (p.inflight+size > 2*p.cc.cwnd(p.minRTTEst()) || !p.bucket.allow(now, size)) {
			continue
		}
		if eta := e.eta(p, size, false); best == nil || eta < bestETA {
			best, bestETA = p, eta
		}
	}
	return best
}

// sendTwin sends a copy of the new packet sp on q. The copy is tracked like
// any transmission (in-flight bytes, loss) but gives no rate samples: an ACK
// cannot tell which copy arrived.
func (e *Engine) sendTwin(now time.Time, q *path, sp *sentPacket) {
	t := &sentPacket{seq: sp.seq, pkt: sp.pkt, first: sp.first, attempts: sp.attempts, twin: sp, isTwin: true}
	sp.twin = t
	e.sendPacket(now, q, t, false)
	q.twins++
}
