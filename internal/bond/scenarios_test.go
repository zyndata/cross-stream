package bond

import (
	"slices"
	"testing"
	"time"
)

const pktSize = 1200 // inner IP packet bytes in the simulations

var equal3 = []linkSpec{
	{mbps: 8, rtt: 60 * time.Millisecond},
	{mbps: 8, rtt: 60 * time.Millisecond},
	{mbps: 8, rtt: 60 * time.Millisecond},
}

// warm brings all paths up and lets the source ramp up.
func warm(t *testing.T, specs []linkSpec, offeredMbps float64, d time.Duration) (*sim, *source) {
	t.Helper()
	s := newSim(t, specs)
	s.run(time.Second)
	for i := range specs {
		if st, _ := s.a.e.PathState(uint8(i + 1)); st != Alive {
			t.Fatalf("path %d not alive after 1 s: %v", i+1, st)
		}
	}
	src := s.startSource(s.a, offeredMbps, pktSize, time.Hour)
	s.run(d)
	return s, src
}

func TestSimAggregatesEqualPaths(t *testing.T) {
	s, _ := warm(t, equal3, 30, 5*time.Second)
	m := s.mark()
	s.run(10 * time.Second)
	got := s.goodput(m)
	t.Logf("3×8 Mb/s, 60 ms: goodput %.2f Mb/s, per path %v", got, s.pathMbps(m))
	if got < 0.85*24*float64(pktSize)/float64(pktSize+IPOverhead) {
		t.Errorf("goodput %.2f Mb/s, want ≥ 85%% of 24 Mb/s", got)
	}
	for id, r := range s.pathMbps(m) {
		if r < 6 {
			t.Errorf("path %d carried %.2f Mb/s, want an even share", id, r)
		}
	}
	for _, p := range s.a.e.Stats().Paths {
		if p.SRTT >= 2*60*time.Millisecond {
			t.Errorf("path %d srtt %v under load, want < 2 × 60 ms (queue build-up)", p.ID, p.SRTT)
		}
	}
	s.checkOrder()
}

func TestSimMixedPaths(t *testing.T) {
	specs := []linkSpec{
		{mbps: 8, rtt: 40 * time.Millisecond, jitter: 5 * time.Millisecond},
		{mbps: 8, rtt: 60 * time.Millisecond, jitter: 5 * time.Millisecond},
		{mbps: 2, rtt: 120 * time.Millisecond, jitter: 5 * time.Millisecond},
	}
	s, _ := warm(t, specs, 25, 5*time.Second)
	m := s.mark()
	s.run(10 * time.Second)
	got := s.goodput(m)
	share := s.pathMbps(m)
	t.Logf("8/8/2 Mb/s, 40/60/120 ms: goodput %.2f Mb/s, per path %v", got, share)
	for _, p := range s.a.e.Stats().Paths {
		t.Logf("path %d: btlbw %.2f Mb/s srtt %v min %v cwnd %d inflight %d", p.ID, float64(p.BtlBw)*8/1e6, p.SRTT, p.MinRTT, p.Cwnd, p.Inflight)
	}
	if got < 15 {
		t.Errorf("goodput %.2f Mb/s, want ≥ 15", got)
	}
	if share[3] < 1.2 || share[3] > 2.2 {
		t.Errorf("slow path carried %.2f Mb/s, want about its 2 Mb/s", share[3])
	}
	for i, p := range s.a.e.Stats().Paths {
		if p.SRTT >= 2*specs[i].rtt+20*time.Millisecond {
			t.Errorf("path %d srtt %v, base %v: queue build-up", p.ID, p.SRTT, specs[i].rtt)
		}
	}
	s.checkOrder()
}

func TestSimFailover(t *testing.T) {
	s, _ := warm(t, equal3, 20, 5*time.Second)
	s.b.maxGap = 0
	skipped := s.b.e.Stats().Skipped
	s.setLink(2, false)
	cut := s.now
	s.run(5 * time.Second)
	t.Logf("failover: max delivery gap %v at %v after the cut, states %v, skipped %d",
		s.b.maxGap, s.b.maxGapAt.Sub(cut), s.a.states[2], s.b.e.Stats().Skipped-skipped)
	if s.b.maxGap >= time.Second {
		t.Errorf("max delivery gap %v, want < 1 s", s.b.maxGap)
	}
	st := s.a.states[2]
	si, di := slices.Index(st, Suspect), slices.Index(st, Dead)
	if si < 0 || di < 0 || si > di {
		t.Errorf("path 2 states %v, want SUSPECT before DEAD", st)
	}
	if got := s.pathStats(s.a, 2).State; got != "dead" {
		t.Errorf("path 2 state %s, want dead", got)
	}
	m := s.mark()
	s.run(3 * time.Second)
	if r := s.pathMbps(m)[2]; r != 0 {
		t.Errorf("dead path carried %.2f Mb/s", r)
	}
	if got := s.goodput(m); got < 14 {
		t.Errorf("goodput on 2 paths %.2f Mb/s, want ≥ 14", got)
	}
	// rejoin: probed, ALIVE after 3 PONGs, carries traffic again within 5 s.
	s.setLink(2, true)
	s.run(5 * time.Second)
	if got := s.pathStats(s.a, 2).State; got != "alive" {
		t.Errorf("path 2 after rejoin: %s, want alive", got)
	}
	m = s.mark()
	s.run(2 * time.Second)
	if r := s.pathMbps(m)[2]; r < 3 {
		t.Errorf("rejoined path carries %.2f Mb/s, want traffic", r)
	}
	s.checkOrder()
}

func TestSimFastRetransmitUnderLoss(t *testing.T) {
	specs := slices.Clone(equal3)
	for i := range specs {
		specs[i].loss = 0.02
	}
	s, _ := warm(t, specs, 15, 5*time.Second)
	st0 := s.b.e.Stats()
	sent0 := s.a.e.Stats()
	m := s.mark()
	s.run(10 * time.Second)
	st := s.b.e.Stats()
	var rtx, lost uint64
	for i, p := range s.a.e.Stats().Paths {
		rtx += p.Retransmit - sent0.Paths[i].Retransmit
		lost += p.Lost - sent0.Paths[i].Lost
	}
	delivered := st.Delivered - st0.Delivered
	skipped := st.Skipped - st0.Skipped
	t.Logf("2%% loss: goodput %.2f Mb/s, delivered %d, skipped %d, re-sent %d, lost %d",
		s.goodput(m), delivered, skipped, rtx, lost)
	if rtx == 0 {
		t.Fatal("no fast retransmits")
	}
	if float64(skipped) > 0.002*float64(delivered) {
		t.Errorf("skipped %d of %d (> 0.2%%): fast retransmit does not repair 2%% loss", skipped, delivered)
	}
	s.checkOrder()
}

func TestSimBackupPath(t *testing.T) {
	s := newSim(t, equal3)
	s.a.e.SetPathConfig(3, PathConfig{Priority: Backup})
	s.b.e.SetPathConfig(3, PathConfig{Priority: Backup})
	s.run(time.Second)
	s.startSource(s.a, 10, pktSize, time.Hour)
	s.run(12 * time.Second)
	m := s.mark()
	s.run(5 * time.Second)
	if p := s.pathStats(s.a, 3); p.TxData != 0 || p.State != "sleeping" {
		t.Errorf("backup path: %d data bytes, state %s; want 0 bytes, sleeping", p.TxData, p.State)
	}
	s.setLink(1, false)
	if r := s.pathMbps(m)[3]; r != 0 {
		t.Errorf("backup carried %.2f Mb/s while 2 normal paths were alive", r)
	}
	s.run(3 * time.Second)
	if p := s.pathStats(s.a, 3); p.TxData != 0 {
		t.Errorf("backup used with one normal path alive: %d bytes", p.TxData)
	}
	s.setLink(2, false)
	s.b.maxGap = 0
	s.run(4 * time.Second)
	m = s.mark()
	s.run(3 * time.Second)
	if r := s.pathMbps(m)[3]; r < 6 {
		t.Errorf("backup carries %.2f Mb/s after both normal paths died, want the load", r)
	}
	t.Logf("backup took over; max delivery gap %v", s.b.maxGap)
	if s.b.maxGap > 2*time.Second {
		t.Errorf("takeover gap %v, want ≤ 2 s", s.b.maxGap)
	}
	s.checkOrder()
}

func TestSimSecondaryPath(t *testing.T) {
	s := newSim(t, equal3)
	s.a.e.SetPathConfig(3, PathConfig{Priority: Secondary})
	s.run(time.Second)
	src := s.startSource(s.a, 10, pktSize, time.Hour)
	s.run(5 * time.Second)
	m := s.mark()
	s.run(5 * time.Second)
	low := s.pathMbps(m)
	src.stop = true
	s.startSource(s.a, 22, pktSize, time.Hour)
	s.run(5 * time.Second)
	m = s.mark()
	s.run(5 * time.Second)
	high := s.pathMbps(m)
	t.Logf("secondary: below caps %v, saturated %v (goodput %.2f)", low, high, s.goodput(m))
	if low[3] > 0.1 {
		t.Errorf("secondary carried %.2f Mb/s while normal paths had room", low[3])
	}
	if high[3] < 3 {
		t.Errorf("secondary carried %.2f Mb/s with saturated normal paths, want traffic", high[3])
	}
	s.checkOrder()
}

func TestSimOverflowThreshold(t *testing.T) {
	s := newSim(t, equal3)
	s.a.e.SetPathConfig(3, PathConfig{Priority: Secondary})
	s.a.e.SetOverflow(1_000_000) // 8 Mb/s
	s.run(time.Second)
	s.startSource(s.a, 22, pktSize, time.Hour)
	s.run(5 * time.Second)
	m := s.mark()
	s.run(5 * time.Second)
	if r := s.pathMbps(m)[3]; r > 0.1 {
		t.Errorf("secondary carried %.2f Mb/s although normal paths deliver above overflow", r)
	}
}

func TestSimRateLimitCapAndDirection(t *testing.T) {
	s := newSim(t, equal3)
	s.a.e.SetPathConfig(1, PathConfig{RateBps: 250_000}) // 2 Mb/s
	s.a.e.SetPathConfig(2, PathConfig{CapBytes: 2_000_000})
	s.a.e.SetPathConfig(3, PathConfig{Dir: DirOff})
	s.run(time.Second)
	s.startSource(s.a, 20, pktSize, time.Hour)
	s.run(3 * time.Second)
	m := s.mark()
	tx0 := s.pathStats(s.a, 1).TxBytes
	s.run(10 * time.Second)
	rate := float64(s.pathStats(s.a, 1).TxBytes-tx0) / s.now.Sub(m.at).Seconds()
	p2 := s.pathStats(s.a, 2)
	t.Logf("limits: path 1 %.0f B/s (limit 250000), path 2 used %d (cap 2000000), path 3 data %d",
		rate, p2.Used, s.pathStats(s.a, 3).TxData)
	if rate < 0.95*250_000 || rate > 1.05*250_000 {
		t.Errorf("rate-limited path sends %.0f B/s, want 250000 ±5%%", rate)
	}
	if !p2.Capped || p2.Used > 2_100_000 {
		t.Errorf("capped path: capped=%v used=%d, want capped at 2000000 ±5%%", p2.Capped, p2.Used)
	}
	if d := s.pathStats(s.a, 3).TxData; d != 0 {
		t.Errorf("direction off path sent %d data bytes", d)
	}
}

func TestSimPathMTU(t *testing.T) {
	tests := []struct {
		name    string
		linkMTU int
		wantMTU int
		tunnel  int
	}{
		{"no limit", 0, 1452, DefaultTunnelMTU},
		{"1400", 1400, 1400, DefaultTunnelMTU},
		{"1340", 1340, 1340, 1340 - IPOverhead},
		{"1300", 1300, 1280, 1280 - IPOverhead},
		{"464XLAT", 1260, MinPathMTU, MinTunnelMTU},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			specs := slices.Clone(equal3)
			specs[1].mtu = tt.linkMTU
			s := newSim(t, specs)
			s.run(12 * time.Second)
			if got := s.pathStats(s.a, 2).MTU; got != tt.wantMTU {
				t.Errorf("path MTU %d, want %d", got, tt.wantMTU)
			}
			if got := s.a.e.TunnelMTU(); got != tt.tunnel {
				t.Errorf("tunnel MTU %d, want %d", got, tt.tunnel)
			}
		})
	}
}
