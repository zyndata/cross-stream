package bond

import (
	"testing"
	"time"
)

// TestSimIdlePathRamp: a path that carried only PINGs since it came up
// (a phone that just joined, a hub that just restarted) leaves STARTUP after
// 2 s of app-limited rounds with an estimate of its keepalive traffic
// (0.2–1 Mb/s). The first bulk transfer then crawled up in PROBE_BW: on the
// S23 and the Dell's modem it took ~6 s to reach the link rate (2026-11-26).
// A path whose rate was never measured must ramp like STARTUP.
// (Background: 0.1 Mb/s of small packets, as the hub's own chatter.)
func TestSimIdlePathRamp(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec linkSpec
		want float64 // Mb/s in the first 3 s
	}{
		{"LTE uplink", linkSpec{mbps: 45, rtt: 22 * time.Millisecond, jitter: 4 * time.Millisecond, grant: 20 * time.Millisecond}, 25},
		{"smooth link", linkSpec{mbps: 30, rtt: 40 * time.Millisecond, jitter: 4 * time.Millisecond}, 18},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSim(t, []linkSpec{tc.spec})
			s.startSource(s.a, 0.1, 200, 10*time.Second) // idle: keepalives, DNS, stray ACKs
			s.run(10 * time.Second)
			before := s.pathStats(s.a, 1)
			fs := s.tcpFlows(1)
			s.run(3 * time.Second)
			got := float64(tcpBytes(fs)) * 8 / 3 / 1e6
			after := s.pathStats(s.a, 1)
			t.Logf("idle estimate %.2f Mb/s (%s); first 3 s %.1f Mb/s, estimate then %.1f Mb/s (%s)",
				float64(before.BtlBw)*8/1e6, before.CC, got, float64(after.BtlBw)*8/1e6, after.CC)
			if got < tc.want {
				t.Errorf("first 3 s after idle: %.1f Mb/s on a %.0f Mb/s link, want ≥ %.0f", got, tc.spec.mbps, tc.want)
			}
		})
	}
}

// TestSimColdStartLoss: the first STARTUP burst of a fresh path is lost
// before anything was delivered (a cold LTE uplink: 2026-11-27, the S23 lost
// ~110 packets in its first round after a hub restart). The loss ended
// STARTUP with a 0.2 Mb/s estimate and a 6 KB in-flight bound, and the path
// crawled to its rate for 10 s. A round that delivered almost nothing is no
// proof of the path's rate.
func TestSimColdStartLoss(t *testing.T) {
	spec := linkSpec{mbps: 45, rtt: 25 * time.Millisecond, jitter: 4 * time.Millisecond, queue: 10 * time.Millisecond}
	s := newSim(t, []linkSpec{spec})
	s.run(time.Second)
	s.a.out[1].rate = 0.3 * 1e6 / 8 // cold uplink: hardly any grant yet
	s.startSource(s.a, 60, pktSize, time.Hour)
	s.run(400 * time.Millisecond)
	s.a.out[1].rate = spec.mbps * 1e6 / 8
	s.run(time.Second)
	m := s.mark()
	s.run(2 * time.Second)
	got := s.goodput(m)
	ps := s.pathStats(s.a, 1)
	t.Logf("1–3 s after the cold start: %.1f Mb/s, estimate %.1f Mb/s (%s), lost %d", got, float64(ps.BtlBw)*8/1e6, ps.CC, ps.Lost)
	if got < 30 {
		t.Errorf("1–3 s after a lossy cold start: %.1f Mb/s on a %.0f Mb/s link, want ≥ 30", got, spec.mbps)
	}
}
