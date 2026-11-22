package bond

import (
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

// AggregateWait is the longest a small IP frame waits for others to share
// its datagram (architecture.md "Packet aggregation").
const AggregateWait = time.Millisecond

// SetAggregation turns packet aggregation on or off (default on).
func (e *Engine) SetAggregation(on bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.noAggregate = !on
	if !on {
		now := e.cfg.Now()
		for _, p := range e.order {
			e.flushAgg(now, p)
		}
	}
}

// aggregates reports whether pkt waits for aggregation: small packets, but
// not min_latency real-time ones.
func (e *Engine) aggregates(pkt []byte) bool {
	if e.noAggregate || len(pkt) > SmallPacket {
		return false
	}
	return e.mode != MinLatency || !classify(pkt).realtime
}

// aggLimit is the largest frames payload of one datagram on p.
func (p *path) aggLimit() int {
	mtu := p.mtu
	if mtu == 0 {
		mtu = MinPathMTU
	}
	return mtu - DatagramOverhead
}

// addAgg queues a small IP frame on p; a full batch is sent at once.
func (e *Engine) addAgg(now time.Time, p *path, f *wire.IP) {
	n := frameSize(f)
	if p.aggSize+n > p.aggLimit() {
		e.flushAgg(now, p)
	}
	if len(p.agg) == 0 {
		p.aggAt = now
	}
	p.agg = append(p.agg, f)
	p.aggSize += n
	e.kick()
}

// flushAgg sends p's waiting frames as one datagram.
func (e *Engine) flushAgg(now time.Time, p *path) {
	if len(p.agg) == 0 {
		return
	}
	frames := p.agg
	p.agg, p.aggSize = nil, 0
	e.transmit(now, p, frames...)
}

// flushAggDue sends batches that waited AggregateWait and returns when the
// next one is due (zero if none).
func (e *Engine) flushAggDue(now time.Time) time.Time {
	var next time.Time
	for _, p := range e.order {
		if len(p.agg) == 0 {
			continue
		}
		due := p.aggAt.Add(AggregateWait)
		if !now.Before(due) {
			e.flushAgg(now, p)
			continue
		}
		if next.IsZero() || due.Before(next) {
			next = due
		}
	}
	return next
}
