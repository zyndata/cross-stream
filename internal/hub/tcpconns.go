package hub

import (
	"time"

	"github.com/zyndata/cross-stream/internal/transport"
)

// TCP connections per tcp:// path (phase 20): TCPConnsDefault, and
// TCPConnsLossy while the path's TCP retransmission ratio stays high. A
// path moves up when the ratio over the last lossWindow reaches lossyAt and
// back when it stays below calmBelow over calmWindow; a window counts only
// with at least minLossSample sent segments in it (light traffic keeps N).
const (
	TCPConnsDefault = 2
	TCPConnsLossy   = 4
	lossyAt         = 0.03
	calmBelow       = 0.01
	lossWindow      = 10 * time.Second
	calmWindow      = 30 * time.Second
	minLossSample   = 100
)

type connSample struct {
	at            time.Time
	retrans, sent uint64
}

// connPolicy picks one path's number of TCP connections from the
// cumulative retransmitted/sent counters of its connection set. Not safe
// for concurrent use.
type connPolicy struct {
	fixed int // > 0: hidden override (xs-hubd -tcp-conns), policy off
	n     int
	hist  []connSample // oldest first, about calmWindow long
}

func newConnPolicy(fixed int) *connPolicy {
	if fixed > transport.MaxStreamConns {
		fixed = transport.MaxStreamConns
	}
	return &connPolicy{fixed: fixed, n: TCPConnsDefault}
}

// conns is the current choice.
func (p *connPolicy) conns() int {
	if p.fixed > 0 {
		return p.fixed
	}
	return p.n
}

// update adds a sample and returns the (new) choice and whether it changed.
// Counters that went back (a new connection set) restart the history.
func (p *connPolicy) update(now time.Time, retrans, sent uint64) (int, bool) {
	if p.fixed > 0 {
		return p.fixed, false
	}
	if k := len(p.hist); k > 0 && (sent < p.hist[k-1].sent || retrans < p.hist[k-1].retrans) {
		p.hist = p.hist[:0]
	}
	p.hist = append(p.hist, connSample{now, retrans, sent})
	// keep one sample at least calmWindow old as the base of the long window
	for len(p.hist) > 2 && !p.hist[1].at.After(now.Add(-calmWindow)) {
		p.hist = p.hist[1:]
	}
	old := p.n
	switch p.n {
	case TCPConnsDefault:
		if r, ok := p.ratio(now, lossWindow); ok && r >= lossyAt {
			p.n = TCPConnsLossy
		}
	default:
		if r, ok := p.ratio(now, calmWindow); ok && r < calmBelow {
			p.n = TCPConnsDefault
		}
	}
	if p.n != old {
		p.hist = p.hist[len(p.hist)-1:] // the next decision needs a fresh window
	}
	return p.n, p.n != old
}

// ratio is retransmitted/sent over the last w (false when the history is
// shorter than w or the window carried too little).
func (p *connPolicy) ratio(now time.Time, w time.Duration) (float64, bool) {
	last := p.hist[len(p.hist)-1]
	for i := len(p.hist) - 1; i >= 0; i-- {
		base := p.hist[i]
		if base.at.After(now.Add(-w)) {
			continue
		}
		ds := last.sent - base.sent
		if ds < minLossSample {
			return 0, false
		}
		return float64(last.retrans-base.retrans) / float64(ds), true
	}
	return 0, false
}
