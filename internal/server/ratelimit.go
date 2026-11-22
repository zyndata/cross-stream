package server

import (
	"net/netip"
	"sync"
	"time"
)

// Handshake rate limit per source IP (phase 14 hardening): a hub needs one
// INIT per rekey plus retries, so a sustained 2/s with bursts of 10 never
// slows a real hub but caps the DH work an attacker can trigger.
const (
	HandshakeRate  = 2.0  // INIT per second per source IP
	HandshakeBurst = 10.0 // bucket size
	MaxRateSources = 8192 // tracked source IPs before idle ones are dropped
	// MaxStreamConnsPerIP caps open TCP/WSS transport connections per source
	// IP: up to 4 per TCP path (phase 20), several paths and hubs may share a
	// carrier NAT address, and a connection that died without a FIN stays
	// open here until its 2-min idle timeout.
	MaxStreamConnsPerIP = 64
)

// rateLimiter is a token bucket per source address.
type rateLimiter struct {
	rate, burst float64
	max         int

	mu      sync.Mutex
	buckets map[netip.Addr]*bucket
}

type bucket struct {
	tokens float64
	last   time.Time
}

func newRateLimiter(rate, burst float64, maxSources int) *rateLimiter {
	return &rateLimiter{rate: rate, burst: burst, max: maxSources, buckets: map[netip.Addr]*bucket{}}
}

// Allow takes one token from addr's bucket; false when it is empty.
func (r *rateLimiter) Allow(addr netip.Addr, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	b := r.buckets[addr]
	if b == nil {
		if len(r.buckets) >= r.max {
			r.pruneLocked(now)
		}
		b = &bucket{tokens: r.burst, last: now}
		r.buckets[addr] = b
	}
	if el := now.Sub(b.last).Seconds(); el > 0 {
		b.tokens = min(r.burst, b.tokens+el*r.rate)
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

// pruneLocked drops buckets that refilled completely (they behave like new
// ones); if the table is still full every source starts over.
func (r *rateLimiter) pruneLocked(now time.Time) {
	full := time.Duration(r.burst / r.rate * float64(time.Second))
	for a, b := range r.buckets {
		if now.Sub(b.last) >= full {
			delete(r.buckets, a)
		}
	}
	if len(r.buckets) >= r.max {
		clear(r.buckets)
	}
}
