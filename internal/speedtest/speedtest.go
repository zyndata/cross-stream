// Package speedtest measures raw path capacity with TEST_* frames
// (architecture.md "Speed test"). The sender pushes filler on each path on a
// ramp (rampStart, doubling every rampStep) until the test's byte budget or
// its duration is used up; the receiver reports the path's best one-second
// window. Not inner TCP: once the ramp is above a path's capacity the path
// drops the excess, so that window is its capacity. When the budget runs out
// before the ramp passes the capacity the result is a lower bound (Capped).
// The schedule is fixed, so the requester knows the sender's final rate
// without asking (phase 18: a fixed budget/duration rate reported every fast
// path at exactly 16 Mb/s).
package speedtest

import (
	"context"
	"sync"
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

// Limits and defaults.
const (
	// FillerSize keeps a TEST_DATA datagram under the smallest path MTU.
	FillerSize    = 1150
	DefaultMaxMB  = 20
	MaxMaxMB      = 200
	DefaultSecs   = 10
	MaxDuration   = 30 * time.Second
	MinDuration   = time.Second
	tickEvery     = 2 * time.Millisecond
	rampStart     = 500_000.0 // bytes/s (4 Mb/s)
	rampStep      = 400 * time.Millisecond
	rampCeil      = 125_000_000.0          // bytes/s (1 Gb/s)
	window        = 500 * time.Millisecond // receiver: best window
	bucket        = 100 * time.Millisecond
	countersKept  = 2 * time.Minute
	DatagramBytes = wire.HeaderLen + 16 + wire.FrameHeaderLen + 2 + FillerSize // outer bytes per filler datagram
)

// Grace is how long a receiver keeps counting after the test duration (late
// packets, the request's own travel time).
const Grace = time.Second

// HoldExtra is how much longer than the test both ends keep the tested paths
// alive, while the filler drains from the path's queue.
const HoldExtra = 3 * time.Second

// Clamp bounds a requested duration and byte cap.
func Clamp(d time.Duration, maxMB uint16) (time.Duration, uint16) {
	d = min(max(d, MinDuration), MaxDuration)
	if maxMB == 0 {
		maxMB = DefaultMaxMB
	}
	return d, min(maxMB, MaxMaxMB)
}

// SendFunc sends one filler frame on a path.
type SendFunc func(pathID uint8, f *wire.TestData) error

// rampRate is the sender's rate (bytes/s) at el into the test.
func rampRate(el time.Duration) float64 {
	n := int(el / rampStep)
	if n > 30 {
		return rampCeil
	}
	return min(rampStart*float64(uint64(1)<<n), rampCeil)
}

// rampBytes is how many bytes the ramp has sent by el (no budget).
func rampBytes(el time.Duration) float64 {
	total := 0.0
	for t := time.Duration(0); t < el; t += rampStep {
		total += rampRate(t) * min(rampStep, el-t).Seconds()
	}
	return total
}

// PeakRate is the sender's rate in Mb/s when it stops: at the moment the
// budget of maxBytes per path runs out, or at d.
func PeakRate(d time.Duration, maxBytes uint64) float64 {
	t := time.Duration(0)
	for t < d && rampBytes(t+rampStep) < float64(maxBytes) {
		t += rampStep
	}
	return rampRate(min(t, d)) * 8 / 1e6
}

// stopAt is when the sender stops: the budget of maxBytes runs out, or d.
func stopAt(d time.Duration, maxBytes uint64) time.Duration {
	left := float64(maxBytes)
	for t := time.Duration(0); t < d; t += rampStep {
		step := min(rampStep, d-t)
		b := rampRate(t) * step.Seconds()
		if b >= left {
			return t + time.Duration(left/rampRate(t)*float64(time.Second))
		}
		left -= b
	}
	return d
}

// Capped reports whether a path's measured rate is only a lower bound: the
// sender ran faster than mbps for less than one receiver window before it
// stopped, so no window can have seen the path full; it may be faster.
func Capped(mbps float64, d time.Duration, maxBytes uint64) bool {
	stop := stopAt(d, maxBytes)
	for t := time.Duration(0); t < stop; t += rampStep {
		if rampRate(t)*8/1e6 > mbps {
			return stop-t < window
		}
	}
	return true
}

// Send pushes filler on every path on the ramp until each has sent maxBytes
// or d passed (or ctx ends) and returns the outer bytes sent per path.
func Send(ctx context.Context, id uint16, paths []uint8, d time.Duration, maxBytes uint64, send SendFunc) map[uint8]uint64 {
	sent := make(map[uint8]uint64, len(paths))
	if len(paths) == 0 || d <= 0 {
		return sent
	}
	f := &wire.TestData{TestID: id, Filler: make([]byte, FillerSize)}
	start := time.Now()
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		el := time.Since(start)
		if el >= d {
			el = d
		}
		due := min(uint64(rampBytes(el)), maxBytes)
		for _, p := range paths {
			for sent[p]+DatagramBytes <= due {
				if err := send(p, f); err != nil {
					sent[p] += DatagramBytes // a full queue is part of the overload; keep the schedule
					continue
				}
				sent[p] += DatagramBytes
			}
		}
		if el >= d || due >= maxBytes {
			return sent
		}
		select {
		case <-ctx.Done():
			return sent
		case <-t.C:
		}
	}
}

// PathCount is what one path received in a test.
type PathCount struct {
	Bytes       uint64
	First, Last time.Time
	buckets     []uint64 // bytes per bucket since First
}

// best returns the most bytes in any window-long run of buckets and that
// window's length (half a second evens out cellular bursts); shorter tests
// use their whole span.
func (p *PathCount) best() (uint64, time.Duration) {
	n := int(window / bucket)
	if len(p.buckets) <= n {
		return 0, 0
	}
	var sum, top uint64
	for i, b := range p.buckets {
		sum += b
		if i >= n {
			sum -= p.buckets[i-n]
		}
		if i >= n-1 {
			top = max(top, sum)
		}
	}
	return top, window
}

type count struct {
	paths   map[uint8]*PathCount
	touched time.Time
}

// Counter counts received filler per test and path. Safe for concurrent use.
type Counter struct {
	mu    sync.Mutex
	tests map[uint16]*count
}

// Expect starts counting test id; filler of other ids is ignored. It also
// forgets abandoned tests.
func (c *Counter) Expect(id uint16, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.tests == nil {
		c.tests = make(map[uint16]*count)
	}
	for k, o := range c.tests {
		if now.Sub(o.touched) > countersKept {
			delete(c.tests, k)
		}
	}
	if c.tests[id] == nil {
		c.tests[id] = &count{paths: make(map[uint8]*PathCount), touched: now}
	}
}

// Add counts n outer bytes of test id received on a path; a test nobody
// expects is not counted (the peer cannot grow the table).
func (c *Counter) Add(id uint16, pathID uint8, n int, now time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	t := c.tests[id]
	if t == nil {
		return
	}
	t.touched = now
	p := t.paths[pathID]
	if p == nil {
		p = &PathCount{First: now}
		t.paths[pathID] = p
	}
	p.Bytes += uint64(n)
	p.Last = now
	i := int(now.Sub(p.First) / bucket)
	if i >= 0 && i < int(MaxDuration/bucket)+20 {
		for len(p.buckets) <= i {
			p.buckets = append(p.buckets, 0)
		}
		p.buckets[i] += uint64(n)
	}
}

// Take returns and forgets test id's counts as TEST_RESULT frames.
func (c *Counter) Take(id uint16) []wire.TestResult {
	c.mu.Lock()
	t := c.tests[id]
	delete(c.tests, id)
	c.mu.Unlock()
	if t == nil {
		return nil
	}
	out := make([]wire.TestResult, 0, len(t.paths))
	for pid, p := range t.paths {
		if b, w := p.best(); w > 0 {
			// The best window; Mbps leaves one datagram out (the first starts the clock).
			out = append(out, wire.TestResult{TestID: id, PathID: pid, Bytes: b + DatagramBytes, DurationUS: uint32(w.Microseconds())})
			continue
		}
		us := p.Last.Sub(p.First).Microseconds()
		out = append(out, wire.TestResult{TestID: id, PathID: pid, Bytes: p.Bytes, DurationUS: uint32(min(max(us, 1), 1<<32-1))})
	}
	return out
}

// Mbps is a result's rate in Mb/s (outer bytes). The first packet only
// starts the clock, so its bytes are not counted.
func Mbps(r wire.TestResult) float64 {
	if r.DurationUS <= 1 || r.Bytes <= DatagramBytes {
		return 0
	}
	return float64(r.Bytes-DatagramBytes) * 8 / float64(r.DurationUS)
}
