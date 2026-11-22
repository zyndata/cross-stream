package hub

import "time"

// WakeGap is how late a 1-second tick may come before the hub counts it as
// a wake from sleep (laptop suspended, VM paused). The server may have
// dropped the session meanwhile (idle timeout 3 min) and drops what an
// unknown session sends, so the hub handshakes again at once instead of
// waiting session.DeadAfter of silence.
const WakeGap = 10 * time.Second

// tickGap is how long passed between two ticks. Both clocks count: Linux's
// monotonic clock stops during suspend (only the wall clock jumps), a frozen
// process sees the monotonic clock jump too. A wall clock stepped by NTP can
// look like a wake; a needless handshake costs one round trip.
func tickGap(prev, now time.Time) time.Duration {
	return elapsed(now.Sub(prev), now.Round(0).Sub(prev.Round(0)))
}

// elapsed is the larger of the monotonic and the wall-clock difference.
func elapsed(mono, wall time.Duration) time.Duration { return max(mono, wall) }
