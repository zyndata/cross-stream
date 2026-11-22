// Package datacap counts bytes against a data cap with a period (session,
// day, month) and throttles over the cap (relay phones, hub local uplinks).
package datacap

import (
	"sync"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
)

// BytesPerMB is the unit of cap_mb and used_mb (decimal, like carrier billing).
const BytesPerMB = 1_000_000

// PeriodStart returns when the cap period containing now began.
// session periods start at sessionStart; day at local midnight; month at
// 00:00 of the most recent reset day (1-28, default 1).
func PeriodStart(l control.Limits, now, sessionStart time.Time) time.Time {
	switch l.CapPeriod {
	case control.PeriodDay:
		y, m, d := now.Date()
		return time.Date(y, m, d, 0, 0, 0, 0, now.Location())
	case control.PeriodMonth:
		rd := l.ResetDay
		if rd < 1 {
			rd = 1
		}
		y, m, d := now.Date()
		if d < rd {
			m--
		}
		return time.Date(y, m, rd, 0, 0, 0, 0, now.Location())
	default:
		return sessionStart
	}
}

// Meter counts cellular bytes against the data cap and applies the throttle.
// Safe for concurrent use.
type Meter struct {
	mu     sync.Mutex
	now    func() time.Time
	birth  time.Time // start of the "session" period
	limits control.Limits
	start  time.Time // current period start
	used   uint64    // bytes in the current period

	// throttle token bucket (bytes), active only when over cap with over_cap=throttle
	tokens float64
	last   time.Time
}

// New returns a meter for limits l; now is the clock (time.Now).
func New(l control.Limits, now func() time.Time) *Meter {
	m := &Meter{now: now, birth: now()}
	m.SetLimits(l)
	return m
}

// SetLimits installs new limits; used_mb from the owner replaces our count.
func (m *Meter) SetLimits(l control.Limits) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.limits = l
	m.start = PeriodStart(l, now, m.birth)
	m.used = uint64(l.UsedMB) * BytesPerMB
	m.tokens, m.last = 0, now
}

// rollLocked starts a new period if the current one ended.
func (m *Meter) rollLocked(now time.Time) {
	if ps := PeriodStart(m.limits, now, m.birth); ps.After(m.start) {
		m.start, m.used = ps, 0
	}
}

func (m *Meter) overCapLocked() bool {
	return m.limits.CapMB > 0 && m.used >= uint64(m.limits.CapMB)*BytesPerMB
}

// Allow reports whether a packet of n bytes may go over cellular now and, if
// so, counts it. Over the cap: stop mode drops everything (the relay leaves
// on the next tick); throttle mode lets throttle_kbps through.
func (m *Meter) Allow(n int) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.rollLocked(now)
	if m.overCapLocked() {
		if m.limits.OverCap != control.OverCapThrottle {
			return false
		}
		rate := float64(m.limits.ThrottleKbps) * 1000 / 8 // bytes per second
		burst := max(rate/4, 16*1024)
		m.tokens = min(burst, m.tokens+now.Sub(m.last).Seconds()*rate)
		m.last = now
		if m.tokens < float64(n) {
			return false
		}
		m.tokens -= float64(n)
	}
	m.used += uint64(n)
	return true
}

// State returns the bytes used in the current period and whether the relay
// must stop (cap reached in stop mode).
func (m *Meter) State() (used uint64, stop bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rollLocked(m.now())
	return m.used, m.overCapLocked() && m.limits.OverCap != control.OverCapThrottle
}

// Limits returns the installed limits.
func (m *Meter) Limits() control.Limits {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.limits
}

// Update installs new limits and keeps the count of the current period
// (hub local uplinks: the hub itself is the owner of the count).
func (m *Meter) Update(l control.Limits) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.limits = l
	m.start = PeriodStart(l, now, m.birth)
	m.tokens, m.last = 0, now
}

// Add counts n bytes that were sent whatever the cap (streams that cannot
// drop: the owner stops the link once State reports stop).
func (m *Meter) Add(n int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rollLocked(m.now())
	m.used += uint64(n)
}

// Reset sets the count to zero; a session period starts again now.
func (m *Meter) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := m.now()
	m.birth = now
	m.start = PeriodStart(m.limits, now, m.birth)
	m.used = 0
}

// Restore puts back a count saved with Snapshot; a count from an earlier
// period is ignored.
func (m *Meter) Restore(since time.Time, used uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rollLocked(m.now())
	if !since.Before(m.start) {
		m.used = used
	}
}

// Snapshot returns the current period start and its byte count.
func (m *Meter) Snapshot() (since time.Time, used uint64) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rollLocked(m.now())
	return m.start, m.used
}
