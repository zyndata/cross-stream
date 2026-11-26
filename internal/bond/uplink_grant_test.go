package bond

import (
	"testing"
	"time"
)

// TestSimUplinkGrant: LTE uploads (2026-11-26, S23 + modem, hub → server).
// Under load data datagrams wait for an uplink grant and the ACKs come in
// batches (AckEvery / AckInterval); PINGs and lone datagrams do not wait:
// the data RTT under load is ~2 × the ping min. An in-flight cap of
// 2 × btlbw × ping min then holds one data RTT of the current rate, the rate
// sample can never exceed the estimate, and both paths sat at the cap floor
// (~2.5 Mb/s each, 14 Mb/s bonded where the raw links gave 50 + 20; before
// the ACK aggregation term the load-only grant still gave 17 Mb/s here).
// The bond must reach most of the links' capacity.
func TestSimUplinkGrant(t *testing.T) {
	for _, tc := range []struct {
		name  string
		specs []linkSpec
		want  float64 // Mb/s
	}{
		{"one path", []linkSpec{
			{mbps: 40, rtt: 20 * time.Millisecond, jitter: 4 * time.Millisecond, grant: 25 * time.Millisecond},
		}, 28},
		{"phone and modem", []linkSpec{
			{mbps: 45, rtt: 20 * time.Millisecond, jitter: 4 * time.Millisecond, grant: 25 * time.Millisecond},
			{mbps: 20, rtt: 22 * time.Millisecond, jitter: 4 * time.Millisecond, grant: 25 * time.Millisecond},
		}, 42},
		{"grant also for a lone datagram", []linkSpec{
			{mbps: 40, rtt: 20 * time.Millisecond, jitter: 4 * time.Millisecond, grant: 25 * time.Millisecond, grantIdle: true},
		}, 28},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := newSim(t, tc.specs)
			s.run(time.Second)
			fs := s.tcpFlows(1)
			s.run(5 * time.Second)
			at, b := s.now, tcpBytes(fs)
			s.run(15 * time.Second)
			got := float64(tcpBytes(fs)-b) * 8 / s.now.Sub(at).Seconds() / 1e6
			t.Logf("TCP goodput %.2f Mb/s", got)
			logPaths(s, fs)
			if got < tc.want { // before the fix: ~3 Mb/s per path (cap floor)
				t.Errorf("TCP goodput %.2f Mb/s, want ≥ %.0f", got, tc.want)
			}
		})
	}
}
