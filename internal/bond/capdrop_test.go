package bond

import (
	"testing"
	"time"
)

// TestSimCapacityDrop: a path's capacity falls after STARTUP (the phone's
// cell or its Wi-Fi hop to the hub; phase 18: server-side btlbw frozen at
// 36.8 Mb/s while the path delivered almost nothing). With TCP traffic every
// sample looked app-limited, so the estimate never aged, the scheduler kept
// sending into the slow path, and the bond fell below the good path alone.
// The estimate must follow and the bond must keep the good path's throughput.
func TestSimCapacityDrop(t *testing.T) {
	for _, tc := range []struct {
		name  string
		div   float64
		queue time.Duration
	}{
		{"10× tail drop", 10, 0},
		{"10× deep buffer", 10, 600 * time.Millisecond},
		{"30× deep buffer", 30, 600 * time.Millisecond},
	} {
		t.Run(tc.name, func(t *testing.T) {
			specs := []linkSpec{
				{mbps: 36, rtt: 50 * time.Millisecond, jitter: 5 * time.Millisecond, queue: tc.queue},
				{mbps: 18, rtt: 60 * time.Millisecond, jitter: 5 * time.Millisecond},
			}
			s := newSim(t, specs)
			s.run(time.Second)
			fs := s.tcpFlows(4)
			s.run(10 * time.Second)
			s.a.out[1].rate /= tc.div
			s.run(10 * time.Second)
			at, b := s.now, tcpBytes(fs)
			s.run(15 * time.Second)
			got := float64(tcpBytes(fs)-b) * 8 / s.now.Sub(at).Seconds() / 1e6
			bw := float64(s.pathStats(s.a, 1).BtlBw) * 8 / 1e6
			t.Logf("after the drop to %.1f Mb/s: btlbw %.2f Mb/s, TCP goodput %.2f Mb/s", 36/tc.div, bw, got)
			logPaths(s, fs)
			if bw > 3*36/tc.div {
				t.Errorf("btlbw %.2f Mb/s 10 s after the drop, want ≤ 3 × %.1f", bw, 36/tc.div)
			}
			if got < 16 { // before the fix: 10–16 Mb/s
				t.Errorf("TCP goodput %.2f Mb/s, want ≥ 16 (about the 18 Mb/s path alone)", got)
			}
		})
	}
}
