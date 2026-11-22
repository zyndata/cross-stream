package bond

import "time"

// rttEstimator is the RFC 6298 smoothed RTT and variance.
type rttEstimator struct {
	srtt, rttvar time.Duration
	has          bool
}

func (r *rttEstimator) sample(rtt time.Duration) {
	if rtt <= 0 {
		rtt = time.Microsecond
	}
	if !r.has {
		r.srtt, r.rttvar, r.has = rtt, rtt/2, true
		return
	}
	d := r.srtt - rtt
	if d < 0 {
		d = -d
	}
	r.rttvar = (3*r.rttvar + d) / 4
	r.srtt = (7*r.srtt + rtt) / 8
}

// roundMax keeps the maximum sample of each of the last n delivery rounds
// that had a sample. Like Linux BBR's filter, it ages only when new samples
// arrive: a quiet (app-limited) path keeps its estimate.
type roundMax struct {
	vals  [10]uint64
	round [10]uint64
	last  uint64 // round of the newest sample
}

func (m *roundMax) add(round, v uint64) {
	i := round % uint64(len(m.vals))
	if m.round[i] != round {
		m.round[i], m.vals[i] = round, 0
	}
	m.vals[i] = max(m.vals[i], v)
	m.last = round
}

// advance moves the window to round without a sample: a maximum ages out
// after len(vals) rounds even when no sample qualifies (all app-limited or
// lost), so a path whose capacity fell does not keep a stale estimate.
func (m *roundMax) advance(round uint64) { m.last = max(m.last, round) }

func (m *roundMax) get() uint64 {
	var best uint64
	for i, v := range m.vals {
		if m.last-m.round[i] < uint64(len(m.vals)) {
			best = max(best, v)
		}
	}
	return best
}

func (m *roundMax) reset() { *m = roundMax{} }

// tokenBucket limits a send rate in bytes/s. Zero rate = unlimited.
type tokenBucket struct {
	rate   uint64
	tokens float64
	last   time.Time
}

func (b *tokenBucket) burst() float64 {
	return max(float64(b.rate)*0.02, 3000)
}

func (b *tokenBucket) setRate(rate uint64, now time.Time) {
	if b.rate != rate {
		b.rate, b.last = rate, now
		b.tokens = b.burst()
	}
}

func (b *tokenBucket) refill(now time.Time) {
	if b.rate == 0 {
		return
	}
	if b.last.IsZero() {
		b.last, b.tokens = now, b.burst()
	}
	dt := now.Sub(b.last).Seconds()
	if dt > 0 {
		b.tokens = min(b.tokens+dt*float64(b.rate), b.burst())
		b.last = now
	}
}

// allow reports whether n bytes may be sent now.
func (b *tokenBucket) allow(now time.Time, n int) bool {
	if b.rate == 0 {
		return true
	}
	b.refill(now)
	return b.tokens >= float64(n)
}

// take consumes n bytes (may go negative for control traffic).
func (b *tokenBucket) take(now time.Time, n int) {
	if b.rate == 0 {
		return
	}
	b.refill(now)
	b.tokens -= float64(n)
}

// wait returns how long until n bytes are allowed.
func (b *tokenBucket) wait(now time.Time, n int) time.Duration {
	if b.rate == 0 {
		return 0
	}
	b.refill(now)
	if b.tokens >= float64(n) {
		return 0
	}
	return time.Duration((float64(n) - b.tokens) / float64(b.rate) * float64(time.Second))
}

// rateMeter measures bytes/s over the last full second.
type rateMeter struct {
	start time.Time
	n     uint64
	rate  uint64
}

func (r *rateMeter) add(now time.Time, n int) {
	r.roll(now)
	r.n += uint64(n)
}

func (r *rateMeter) roll(now time.Time) {
	if r.start.IsZero() {
		r.start = now
		return
	}
	d := now.Sub(r.start)
	if d < time.Second {
		return
	}
	if d < 2*time.Second {
		r.rate = uint64(float64(r.n) / d.Seconds())
	} else {
		r.rate = 0
	}
	r.start, r.n = now, 0
}

func (r *rateMeter) get(now time.Time) uint64 {
	r.roll(now)
	return r.rate
}
