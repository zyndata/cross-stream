package bond

import (
	"cmp"
	"slices"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

var t0 = time.Unix(1_700_000_000, 0)

func TestSeqDiff(t *testing.T) {
	tests := []struct {
		a, b uint32
		want int64
	}{
		{5, 3, 2},
		{3, 5, -2},
		{1, 0xFFFFFFFF, 2},
		{0xFFFFFFFF, 1, -2},
	}
	for _, tt := range tests {
		if got := seqDiff(tt.a, tt.b); got != tt.want {
			t.Errorf("seqDiff(%d, %d) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestSeqSet(t *testing.T) {
	var s seqSet
	for _, q := range []uint32{0xFFFFFFFE, 0xFFFFFFFF, 1, 3} {
		s.add(q)
	}
	tests := []struct {
		seq  uint32
		want bool
	}{
		{0xFFFFFFFE, true}, {0xFFFFFFFF, true}, {0, false}, {1, true}, {2, false}, {3, true}, {4, false},
		{0xFFFFFFFF - seqSetBits, true}, // below the window: reported as seen (duplicate)
	}
	for _, tt := range tests {
		if got := s.contains(tt.seq); got != tt.want {
			t.Errorf("contains(%d) = %v, want %v", tt.seq, got, tt.want)
		}
	}
	// bit i = largest−1−i: 2 missing, 1 present, 0 missing, 0xFFFFFFFF present, 0xFFFFFFFE present.
	if got, want := s.bitmap(3), uint64(0b11010); got != want {
		t.Errorf("bitmap(3) = %b, want %b", got, want)
	}
	s.add(3 + seqSetBits) // jump clears the old window
	if s.contains(1 + seqSetBits) {
		t.Error("old seq still set after a full-window jump")
	}
}

func TestReceiverReorder(t *testing.T) {
	type step struct {
		at   time.Duration
		seq  uint32 // 0 = tick only
		tick bool
	}
	tests := []struct {
		name    string
		hold    time.Duration
		steps   []step
		want    []uint32
		skipped uint64
		dups    uint64
		late    uint64
	}{
		{"in order", time.Second, []step{{0, 10, false}, {0, 11, false}, {0, 12, false}}, []uint32{10, 11, 12}, 0, 0, 0},
		{"reordered", time.Second, []step{{0, 10, false}, {0, 12, false}, {0, 11, false}}, []uint32{10, 11, 12}, 0, 0, 0},
		{"duplicate", time.Second, []step{{0, 10, false}, {0, 10, false}, {0, 11, false}}, []uint32{10, 11}, 0, 1, 0},
		{"gap skipped after hold", 50 * time.Millisecond,
			[]step{{0, 10, false}, {0, 12, false}, {40 * time.Millisecond, 0, true}, {60 * time.Millisecond, 0, true}},
			[]uint32{10, 12}, 1, 0, 0},
		{"late after skip dropped", 50 * time.Millisecond,
			[]step{{0, 10, false}, {0, 12, false}, {60 * time.Millisecond, 0, true}, {70 * time.Millisecond, 11, false}},
			[]uint32{10, 12}, 1, 0, 1},
		{"later gaps share one hold", 50 * time.Millisecond, // 11 and 13 lost: one hold, not two
			[]step{{0, 10, false}, {0, 12, false}, {5 * time.Millisecond, 14, false}, {60 * time.Millisecond, 0, true}},
			[]uint32{10, 12, 14}, 2, 0, 0},
		{"peer restart resets", time.Second, []step{{0, 100000, false}, {0, 5, false}, {0, 6, false}},
			[]uint32{100000, 5, 6}, 0, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r receiver
			var got []uint32
			deliver := func(p []byte) { got = append(got, uint32(p[0])<<24|uint32(p[1])<<16|uint32(p[2])<<8|uint32(p[3])) }
			for _, s := range tt.steps {
				now := t0.Add(s.at)
				if s.tick {
					r.tick(now, tt.hold, deliver)
					continue
				}
				r.onIP(now, s.seq, []byte{byte(s.seq >> 24), byte(s.seq >> 16), byte(s.seq >> 8), byte(s.seq)}, deliver)
			}
			if !slices.Equal(got, tt.want) || r.skipped != tt.skipped || r.dups != tt.dups || r.late != tt.late {
				t.Errorf("delivered %v skipped %d dups %d late %d, want %v %d %d %d", got, r.skipped, r.dups, r.late, tt.want, tt.skipped, tt.dups, tt.late)
			}
		})
	}
}

func TestReceiverAcks(t *testing.T) {
	var r receiver
	for _, s := range []uint32{100, 101, 103, 300, 30} { // 30: late packet far below the newest
		r.onIP(t0, s, []byte{0}, func([]byte) {})
	}
	due, _ := r.ackDue(t0)
	if !due {
		t.Fatal("ACK not due at once after the first packets")
	}
	acks := r.acks(t0)
	want := []wire.Frame{
		&wire.Ack{Largest: 300},
		&wire.Ack{Largest: 103, Bitmap: 0b110}, // 101, 100
		&wire.Ack{Largest: 30},
	}
	if len(acks) != len(want) {
		t.Fatalf("acks %v, want %v", acks, want)
	}
	for i := range want {
		if *acks[i].(*wire.Ack) != *want[i].(*wire.Ack) {
			t.Errorf("ack %d = %+v, want %+v", i, acks[i], want[i])
		}
	}
	if due, next := r.ackDue(t0); due || !next.IsZero() {
		t.Error("ACK still due after sending")
	}
	r.onIP(t0.Add(time.Millisecond), 104, []byte{0}, func([]byte) {})
	if due, next := r.ackDue(t0.Add(time.Millisecond)); due || !next.Equal(t0.Add(AckInterval)) {
		t.Errorf("second ACK due=%v at %v, want after AckInterval", due, next)
	}
}

func TestHoldTime(t *testing.T) {
	ms := time.Millisecond
	tests := []struct {
		lo, hi, want time.Duration
	}{
		{60 * ms, 60 * ms, 120 * ms},  // 1.5 × 60 + 30
		{40 * ms, 120 * ms, 210 * ms}, // 1.5 × 120 + 30 > 2 × 80 + 20
		{20 * ms, 150 * ms, 280 * ms}, // 2 × 130 + 20
		{40 * ms, 400 * ms, MaxHold},  // capped
	}
	for _, tt := range tests {
		if got := holdTime(tt.lo, tt.hi); got != tt.want {
			t.Errorf("holdTime(%v, %v) = %v, want %v", tt.lo, tt.hi, got, tt.want)
		}
	}
}

func TestTokenBucket(t *testing.T) {
	var b tokenBucket
	b.setRate(100_000, t0) // burst 3000
	if !b.allow(t0, 3000) || b.allow(t0, 3001) {
		t.Fatal("burst not 3000 bytes")
	}
	b.take(t0, 3000)
	if b.allow(t0, 1) {
		t.Fatal("empty bucket allows")
	}
	if w := b.wait(t0, 1000); w != 10*time.Millisecond {
		t.Errorf("wait for 1000 B at 100 kB/s = %v, want 10ms", w)
	}
	if !b.allow(t0.Add(10*time.Millisecond), 1000) {
		t.Error("refill after 10 ms")
	}
	var off tokenBucket
	if !off.allow(t0, 1<<20) || off.wait(t0, 1<<20) != 0 {
		t.Error("zero rate must be unlimited")
	}
}

func TestRTTAndRoundMax(t *testing.T) {
	var r rttEstimator
	r.sample(100 * time.Millisecond)
	r.sample(200 * time.Millisecond)
	if r.srtt != 112500*time.Microsecond || r.rttvar != 62500*time.Microsecond {
		t.Errorf("srtt %v rttvar %v", r.srtt, r.rttvar)
	}
	var m roundMax
	m.add(1, 10)
	m.add(1, 30)
	m.add(5, 20)
	if got := m.get(); got != 30 {
		t.Errorf("max over rounds 1..5 = %d, want 30", got)
	}
	m.add(11, 5)
	if got := m.get(); got != 20 {
		t.Errorf("round 1 must expire 10 rounds after it: got %d, want 20", got)
	}
}

func TestEngineControlPaths(t *testing.T) {
	var sent []uint8
	now := t0
	e := New(Config{Now: func() time.Time { return now }, Transmit: func(id uint8, _ []wire.Frame) error {
		sent = append(sent, id)
		return nil
	}})
	if _, ok := e.BestPath(); ok {
		t.Fatal("BestPath without paths")
	}
	e.AddPath(2, PathConfig{}, true)
	e.AddPath(1, PathConfig{}, false)
	if id, ok := e.BestPath(); !ok || id != 2 {
		t.Errorf("BestPath = %d %v, want the alive path 2", id, ok)
	}
	if st, _ := e.PathState(1); st != Dead {
		t.Errorf("new path state %v, want dead (probing)", st)
	}
	e.Tick()
	if !slices.Contains(sent, 1) || !slices.Contains(sent, 2) {
		t.Errorf("pings sent on %v, want both paths", sent)
	}
	if !e.SetPathConfig(1, PathConfig{Priority: Backup}) || e.SetPathConfig(9, PathConfig{}) {
		t.Error("SetPathConfig result")
	}
	e.MarkDead(2)
	if st, _ := e.PathState(2); st != Dead {
		t.Errorf("MarkDead: state %v", st)
	}
	e.RemovePath(2)
	if _, ok := e.PathState(2); ok {
		t.Error("removed path still known")
	}
	for range 5 {
		e.Send(make([]byte, 100)) // no usable path: queued, later dropped
	}
	now = now.Add(QueueMaxDelay + time.Millisecond)
	e.Tick()
	if st := e.Stats(); st.Dropped != 1 || st.Queue != 4 {
		t.Errorf("queue %d dropped %d, want one soft drop past the limit", st.Queue, st.Dropped)
	}
	now = now.Add(QueueMaxDelay)
	e.Tick()
	if st := e.Stats(); st.Dropped != 5 || st.Queue != 0 {
		t.Errorf("queue %d dropped %d, want every packet past twice the limit dropped", st.Queue, st.Dropped)
	}
}

// TestStartupPacingNeverDrops: a tiny app-limited first sample (a path that
// carries only TCP ACKs) must not throttle the path below the initial rate.
func TestStartupPacingNeverDrops(t *testing.T) {
	var b bbr
	srtt := 100 * time.Millisecond
	initial := b.pacingRate(srtt)
	b.onAck(t0, 800, true, 0, 80, 0, srtt) // 800 B/s, app-limited
	if got := b.pacingRate(srtt); got < initial {
		t.Errorf("pacing %.0f B/s after a tiny sample, want >= initial %.0f", got, initial)
	}
	if got := b.cwnd(srtt); got < initialCwnd {
		t.Errorf("startup cwnd %d, want >= %d", got, initialCwnd)
	}
	b.mode = bbrProbeBW
	if got, floor := b.pacingRate(srtt), initialCwnd/srtt.Seconds(); got != floor {
		t.Errorf("PROBE_BW pacing %.0f with a tiny estimate, want the floor %.0f", got, floor)
	}
}

func tcpPacket(payload int, flags byte) []byte {
	p := make([]byte, 20+20+payload)
	p[0], p[9] = 0x45, 6
	p[2], p[3] = byte(len(p)>>8), byte(len(p))
	p[20+12] = 5 << 4
	p[20+13] = flags
	return p
}

func TestPureTCPAck(t *testing.T) {
	udp := tcpPacket(0, 0x10)
	udp[9] = 17
	frag := tcpPacket(0, 0x10)
	frag[6] = 0x20 // more fragments
	tests := []struct {
		name string
		pkt  []byte
		want bool
	}{
		{"ack", tcpPacket(0, 0x10), true},
		{"ack with data", tcpPacket(100, 0x10), false},
		{"syn ack", tcpPacket(0, 0x12), false},
		{"fin ack", tcpPacket(0, 0x11), false},
		{"udp", udp, false},
		{"fragment", frag, false},
		{"short", []byte{0x45, 0, 0}, false},
	}
	for _, tt := range tests {
		if got := pureTCPAck(tt.pkt); got != tt.want {
			t.Errorf("%s: %v, want %v", tt.name, got, tt.want)
		}
	}
}

func TestReceiverDeliversTCPAcksAcrossGaps(t *testing.T) {
	var r receiver
	var got []int
	deliver := func(p []byte) { got = append(got, len(p)) }
	ack, data := tcpPacket(0, 0x10), tcpPacket(10, 0x10)
	r.onIP(t0, 1, data, deliver)
	r.onIP(t0, 3, ack, deliver)  // gap at 2: the ACK goes out at once
	r.onIP(t0, 4, data, deliver) // held
	r.onIP(t0, 2, data, deliver) // fills the gap: 2 and 4, the ACK is not repeated
	if want := []int{50, 40, 50, 50}; !slices.Equal(got, want) {
		t.Errorf("delivered sizes %v, want %v", got, want)
	}
}

func TestProbeRTTOnePathAtATime(t *testing.T) {
	now := t0
	e := New(Config{Now: func() time.Time { return now }, Transmit: func(uint8, []wire.Frame) error { return nil }})
	e.AddPath(1, PathConfig{}, true)
	e.AddPath(2, PathConfig{}, true)
	for _, p := range e.order {
		p.cc.onRTT(now, 50*time.Millisecond)
	}
	now = now.Add(minRTTWindow + time.Second)
	for _, p := range e.order {
		p.cc.onRTT(now, 80*time.Millisecond)
		p.lastRecv = now // heard from: not dead for silence
		p.pings = nil
	}
	e.Tick()
	n := 0
	for _, p := range e.order {
		if p.cc.mode == bbrProbeRTT {
			n++
		}
	}
	if n != 1 {
		t.Errorf("%d paths in PROBE_RTT, want 1", n)
	}
}

// TestStartupExit: STARTUP ends on heavy loss and, after startupAppBound,
// on a flat estimate even when every round is app-limited (phase 07b).
func TestStartupExit(t *testing.T) {
	rtt := 50 * time.Millisecond
	tests := []struct {
		name     string
		losses   int           // packets lost in each round after the first
		app      bool          // every sample app-limited
		grow     bool          // the rate grows 50 % per round (pipe not full)
		step     time.Duration // time between rounds
		wantDone bool          // left STARTUP
		wantHi   bool          // inflight_hi set
		perRound uint64        // bytes delivered per round (0: 20 kB)
	}{
		{"heavy loss", startupLossEvents, false, true, rtt, true, true, 0},
		{"heavy loss, almost nothing delivered", startupLossEvents, false, true, rtt, false, false, initialCwnd / 2},
		{"few losses", startupLossEvents - 1, false, true, rtt, false, false, 0},
		{"app-limited, early", 0, true, false, rtt, false, false, 0},
		{"app-limited, past the bound", 0, true, false, startupAppBound / 4, true, false, 0},
		{"full pipe", 0, false, false, rtt, true, false, 0},
	}
	for _, tt := range tests {
		var b bbr
		now, delivered, rate := t0, uint64(0), 200_000.0
		for range 8 {
			prev := delivered
			delivered += cmp.Or(tt.perRound, 20_000)
			for range tt.losses {
				b.onLoss(now, 1500, prev, rtt, time.Hour)
			}
			b.onAck(now, rate, tt.app, prev, delivered, 0, rtt)
			now = now.Add(tt.step)
			if tt.grow {
				rate *= 1.5
			}
		}
		if done := b.mode != bbrStartup; done != tt.wantDone {
			t.Errorf("%s: mode %v, want left STARTUP %v", tt.name, b.mode, tt.wantDone)
		}
		if hi := b.inflightHi > 0; hi != tt.wantHi {
			t.Errorf("%s: inflight_hi %d, want set %v", tt.name, b.inflightHi, tt.wantHi)
		}
	}
}

// TestInflightHi: too much loss in PROBE_BW cuts the in-flight bound, which
// grows back in loss-free probing rounds and stops binding at 2 × BDP.
func TestInflightHi(t *testing.T) {
	rtt := 50 * time.Millisecond
	b := bbr{mode: bbrProbeBW}
	b.maxBW.add(0, 1_000_000) // BDP 50 kB, cwnd 100 kB
	for range probeLossEvents {
		b.onLoss(t0, 1500, 0, rtt, time.Hour)
	}
	b.onRoundEnd(40_000, rtt)
	if want := int(inflightHiBeta * 100_000); b.inflightHi != want || b.cwnd(rtt) != want {
		t.Fatalf("after loss: inflight_hi %d cwnd %d, want %d", b.inflightHi, b.cwnd(rtt), want)
	}
	b.cycle = 1 // not probing: no growth
	b.onRoundEnd(80_000, rtt)
	if b.inflightHi != int(inflightHiBeta*100_000) {
		t.Errorf("grew outside the probe phase: %d", b.inflightHi)
	}
	b.cycle = 0
	for i := range 5 {
		b.onRoundEnd(uint64(120_000+i*40_000), rtt)
	}
	if b.inflightHi != 0 || b.cwnd(rtt) != 100_000 {
		t.Errorf("after loss-free probing: inflight_hi %d cwnd %d, want unbound 100000", b.inflightHi, b.cwnd(rtt))
	}
}

// TestBandwidthWindowAges: a maximum leaves the window after bwRoundWindow
// rounds even when no later sample qualified.
func TestBandwidthWindowAges(t *testing.T) {
	tests := []struct {
		name    string
		rounds  uint64 // rounds advanced without a sample
		wantMax uint64
	}{
		{"fresh", 0, 5_000_000},
		{"inside the window", bwRoundWindow - 1, 5_000_000},
		{"aged out", bwRoundWindow, 0},
	}
	for _, tt := range tests {
		var m roundMax
		m.add(3, 5_000_000)
		m.advance(3 + tt.rounds)
		if got := m.get(); got != tt.wantMax {
			t.Errorf("%s: max %d, want %d", tt.name, got, tt.wantMax)
		}
	}
}

// TestLossOnlyRounds: a path that loses everything (no ACK ends a round)
// still ends rounds after the loss timeout, cuts inflight_hi and forgets its
// stale bandwidth estimate.
func TestLossOnlyRounds(t *testing.T) {
	rtt, to := 50*time.Millisecond, 300*time.Millisecond
	b := bbr{mode: bbrProbeBW}
	b.maxBW.add(0, 5_000_000) // 40 Mb/s
	now := t0
	for i := range 160 {
		b.onLoss(now, 1500, 1_000_000, rtt, to)
		now = now.Add(25 * time.Millisecond)
		if i == 0 && b.round != 0 {
			t.Fatalf("first loss ended a round")
		}
	}
	// 4 s of loss, rounds of lossRoundFloor: ~4 loss-only rounds.
	if b.round < 3 || b.round > 4 {
		t.Errorf("rounds %d after 4 s of total loss, want 3–4", b.round)
	}
	if b.inflightHi == 0 || b.inflightHi >= int(probeBWGain*5_000_000*rtt.Seconds()) {
		t.Errorf("inflight_hi %d, want cut below the 2×BDP cwnd", b.inflightHi)
	}
	for range 400 {
		b.onLoss(now, 1500, 1_000_000, rtt, to)
		now = now.Add(25 * time.Millisecond)
	}
	if bw := b.bw(); bw != 0 {
		t.Errorf("btlbw %d after 14 s of total loss, want aged out", bw)
	}
}

// TestLossFigure: the smoothed loss fraction is capped at 1 and decays on
// an idle path.
func TestLossFigure(t *testing.T) {
	tests := []struct {
		name       string
		sent, lost uint64
		idle       int // idle seconds afterwards
		wantMax    float64
		wantMin    float64
	}{
		{"half lost", 100, 50, 0, 0.13, 0.12},
		{"more lost than sent", 10, 30, 0, 0.25, 0.25},
		{"idle decays", 100, 100, 10, 0.02, 0},
	}
	for _, tt := range tests {
		p := &path{}
		now := t0
		p.noteLoss(now)
		p.winSent, p.winLost = tt.sent, tt.lost
		now = now.Add(time.Second)
		p.noteLoss(now)
		for range tt.idle {
			now = now.Add(time.Second)
			p.noteLoss(now)
		}
		if p.loss > tt.wantMax || p.loss < tt.wantMin {
			t.Errorf("%s: loss %.3f, want %.3f..%.3f", tt.name, p.loss, tt.wantMin, tt.wantMax)
		}
	}
}
