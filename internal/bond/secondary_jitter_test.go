package bond

import (
	"testing"
	"time"
)

// TestSimSecondaryUnderJitter: three 8 Mb/s paths, 60 ± 20 ms, 0.5 % loss,
// path 3 secondary, 6 Mb/s offered (testenv "secondary"). The normal paths'
// estimates must not sink to their traffic rate, or they look full and spill
// onto the secondary (phase 18: an app-limited period ending one packet early
// let low samples in).
func TestSimSecondaryUnderJitter(t *testing.T) {
	spec := linkSpec{mbps: 8, rtt: 60 * time.Millisecond, jitter: 20 * time.Millisecond, loss: 0.005}
	for seed := uint64(1); seed <= 4; seed++ {
		s := newSimSeed(t, []linkSpec{spec, spec, spec}, seed)
		s.a.e.SetPathConfig(3, PathConfig{Priority: Secondary})
		s.run(time.Second)
		s.startSource(s.a, 6, pktSize, time.Hour)
		s.run(3 * time.Second)
		m := s.mark()
		s.run(12 * time.Second)
		if r := s.pathMbps(m)[3]; r > 0.05 {
			t.Errorf("seed %d: secondary carried %.3f Mb/s at 6 Mb/s offered, want ~0", seed, r)
		}
		for _, id := range []uint8{1, 2} {
			if bw := float64(s.pathStats(s.a, id).BtlBw) * 8 / 1e6; bw < 3 {
				t.Errorf("seed %d: path %d btlbw %.2f Mb/s sank to its traffic rate (want ≥ 3)", seed, id, bw)
			}
		}
	}
}
