package bond

import (
	"testing"
	"time"
)

// TestSimStallKeepsEstimate: a cellular delivery stall (150–250 ms, then
// everything at once) makes the whole flight look lost. The late ACKs must
// count as delivered and the busy path's estimate must survive: on the S23
// it fell 49.9 → 3.1 Mb/s in one sample (2026-11-26) and the path crawled
// back for seconds. In the sim the estimate fell to 6–11 Mb/s, once to 0.
func TestSimStallKeepsEstimate(t *testing.T) {
	const mbps = 50
	for _, stall := range []time.Duration{150 * time.Millisecond, 250 * time.Millisecond} {
		for seed := uint64(1); seed <= 4; seed++ {
			s := newSimSeed(t, []linkSpec{{mbps: mbps, rtt: 20 * time.Millisecond, jitter: 4 * time.Millisecond,
				grant: 20 * time.Millisecond, stall: stall}}, seed)
			s.run(time.Second)
			s.tcpFlows(4)
			s.run(4 * time.Second) // STARTUP done, stalls every 1–3 s from here
			low := float64(mbps)
			for range 300 {
				s.run(50 * time.Millisecond)
				low = min(low, float64(s.pathStats(s.a, 1).BtlBw)*8/1e6)
			}
			if low < mbps/2 {
				t.Errorf("stall %v seed %d: btlbw fell to %.1f Mb/s on a %d Mb/s path, want ≥ %d", stall, seed, low, mbps, mbps/2)
			}
		}
	}
}
