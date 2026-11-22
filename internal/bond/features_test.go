package bond

import (
	"slices"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

func udpPacket(src, dst uint16, payload int) []byte {
	p := make([]byte, 20+8+payload)
	p[0], p[9] = 0x45, 17
	p[2], p[3] = byte(len(p)>>8), byte(len(p))
	p[20], p[21], p[22], p[23] = byte(src>>8), byte(src), byte(dst>>8), byte(dst)
	return p
}

func TestClassify(t *testing.T) {
	icmp := udpPacket(0, 0, 36)
	icmp[9] = 1
	frag := udpPacket(53, 4000, 10)
	frag[7] = 8 // not the first fragment: no ports to look at
	tests := []struct {
		name string
		pkt  []byte
		want pktClass
	}{
		{"dns query", udpPacket(40000, 53, 30), pktClass{critical: true}},
		{"dns answer", udpPacket(53, 40000, 100), pktClass{critical: true}},
		{"udp game", udpPacket(40000, 7777, 100), pktClass{realtime: true}},
		{"icmp", icmp, pktClass{realtime: true}},
		{"udp fragment", frag, pktClass{realtime: true}},
		{"tcp syn", tcpPacket(0, 0x02), pktClass{critical: true}},
		{"tcp fin", tcpPacket(0, 0x11), pktClass{critical: true}},
		{"tcp rst", tcpPacket(0, 0x04), pktClass{critical: true}},
		{"tcp data", tcpPacket(1000, 0x10), pktClass{}},
		{"not ipv4", []byte{0x60, 0, 0, 0}, pktClass{}},
		{"short", []byte{0x45}, pktClass{}},
	}
	for _, tt := range tests {
		if got := classify(tt.pkt); got != tt.want {
			t.Errorf("%s: %+v, want %+v", tt.name, got, tt.want)
		}
	}
}

// testEngine has two alive paths with a known RTT and records datagrams per path.
type testEngine struct {
	e    *Engine
	now  time.Time
	sent map[uint8][][]wire.Frame
}

func newTestEngine(t *testing.T, rtts ...time.Duration) *testEngine {
	t.Helper()
	te := &testEngine{now: t0, sent: map[uint8][][]wire.Frame{}}
	te.e = New(Config{Now: func() time.Time { return te.now }, Transmit: func(id uint8, fs []wire.Frame) error {
		te.sent[id] = append(te.sent[id], fs)
		return nil
	}})
	for i, rtt := range rtts {
		id := uint8(i + 1)
		te.e.AddPath(id, PathConfig{}, true)
		p := te.e.paths[id]
		for range 8 {
			p.rtt.sample(rtt)
		}
		p.mtu = 1452
	}
	return te
}

// send queues pkt after enough time for any pacing gap.
func (te *testEngine) send(pkts ...[]byte) {
	for _, pkt := range pkts {
		te.now = te.now.Add(5 * time.Millisecond)
		te.e.Send(pkt)
	}
}

// ipFrames counts IP frames (and datagrams carrying them) sent on a path.
func (te *testEngine) ipFrames(id uint8) (frames, datagrams int) {
	for _, d := range te.sent[id] {
		n := 0
		for _, f := range d {
			if _, ok := f.(*wire.IP); ok {
				n++
			}
		}
		frames += n
		if n > 0 {
			datagrams++
		}
	}
	return frames, datagrams
}

func TestCriticalPacketsAlwaysDuplicated(t *testing.T) {
	te := newTestEngine(t, 50*time.Millisecond, 60*time.Millisecond)
	te.e.SetRedundancy(RedundancyOff)
	te.e.SetAggregation(false)
	te.send(tcpPacket(1000, 0x10)) // bulk: one copy
	a1, _ := te.ipFrames(1)
	a2, _ := te.ipFrames(2)
	if a1+a2 != 1 {
		t.Fatalf("bulk packet sent %d times", a1+a2)
	}
	te.send(udpPacket(40000, 53, 40)) // DNS: both paths
	te.send(tcpPacket(0, 0x02))       // SYN: both paths
	b1, _ := te.ipFrames(1)
	b2, _ := te.ipFrames(2)
	if b1-a1 != 2 || b2-a2 != 2 {
		t.Fatalf("critical packets: %d on path 1, %d on path 2, want 2 each", b1-a1, b2-a2)
	}
	if st := te.e.Stats(); st.Paths[0].Twins+st.Paths[1].Twins != 2 {
		t.Errorf("twins %d+%d, want 2", st.Paths[0].Twins, st.Paths[1].Twins)
	}
}

func TestTwinNeverOnBackup(t *testing.T) {
	te := newTestEngine(t, 50*time.Millisecond, 60*time.Millisecond)
	te.e.SetPathConfig(2, PathConfig{Priority: Backup})
	te.e.SetRedundancy(RedundancyOn)
	te.send(udpPacket(1, 53, 20))
	if n, _ := te.ipFrames(2); n != 0 {
		t.Fatalf("backup path got %d copies", n)
	}
}

func TestTwinAckReleasesBothCopies(t *testing.T) {
	te := newTestEngine(t, 50*time.Millisecond, 60*time.Millisecond)
	te.e.SetRedundancy(RedundancyOn)
	te.e.SetAggregation(false)
	te.send(tcpPacket(1000, 0x10))
	p1, p2 := te.e.paths[1], te.e.paths[2]
	if p1.inflight == 0 || p2.inflight == 0 {
		t.Fatalf("in flight %d / %d, want both copies", p1.inflight, p2.inflight)
	}
	seq := te.e.nextSeq - 1
	te.now = te.now.Add(55 * time.Millisecond)
	te.e.Receive(1, 50, []wire.Frame{&wire.Ack{Largest: seq}})
	if p1.inflight != 0 || p2.inflight != 0 || len(te.e.unacked) != 0 {
		t.Fatalf("after ACK in flight %d / %d, unacked %d", p1.inflight, p2.inflight, len(te.e.unacked))
	}
}

func TestLostPrimaryHandsOverToTwin(t *testing.T) {
	te := newTestEngine(t, 50*time.Millisecond, 60*time.Millisecond)
	te.e.SetRedundancy(RedundancyOn)
	te.e.SetAggregation(false)
	te.send(tcpPacket(1000, 0x10))
	sp := te.e.unacked[te.e.nextSeq-1]
	te.e.lose(te.now, sp)
	if len(te.e.rtx) != 0 {
		t.Fatal("re-send queued while the twin is still in flight")
	}
	if got := te.e.unacked[sp.seq]; got != sp.twin {
		t.Fatal("twin did not take over the seq")
	}
}

func TestAutoRedundancy(t *testing.T) {
	te := newTestEngine(t, 50*time.Millisecond, 60*time.Millisecond)
	te.e.SetAggregation(false)
	game := udpPacket(40000, 7777, 100)
	te.send(game)
	if st := te.e.Stats(); st.Redundant || st.Paths[0].Twins+st.Paths[1].Twins != 0 {
		t.Fatal("auto redundant without a lossy path")
	}
	te.e.paths[1].loss = 0.05
	te.e.Tick()
	te.send(game)
	te.send(tcpPacket(1200, 0x10))       // TCP: not duplicated by auto
	te.send(udpPacket(40000, 443, 1200)) // bulk UDP (QUIC): not duplicated by auto
	st := te.e.Stats()
	if !st.Redundant || st.Paths[0].Twins+st.Paths[1].Twins != 1 {
		t.Fatalf("auto with 5%% loss: redundant %v, twins %d+%d, want the game packet duplicated",
			st.Redundant, st.Paths[0].Twins, st.Paths[1].Twins)
	}
	te.e.paths[1].loss = 0
	te.now = te.now.Add(AutoCalm - time.Second)
	te.e.Tick()
	if !te.e.Stats().Redundant {
		t.Fatal("auto redundancy off before the calm period")
	}
	te.now = te.now.Add(2 * time.Second)
	te.e.Tick()
	if te.e.Stats().Redundant {
		t.Fatal("auto redundancy still on after the calm period")
	}
}

func TestMinLatencyMode(t *testing.T) {
	te := newTestEngine(t, 30*time.Millisecond, 80*time.Millisecond)
	te.e.SetRedundancy(RedundancyOff)
	te.e.SetMode(MinLatency)
	te.e.paths[1].inflight = 10000 // busy low-RTT path
	te.send(udpPacket(40000, 7777, 1000))
	n1, _ := te.ipFrames(1)
	n2, _ := te.ipFrames(2)
	if n1 != 1 || n2 != 1 {
		t.Fatalf("real-time packet: %d on the low-RTT path, %d copies on the other; want 1 and 1", n1, n2)
	}
	if te.e.unacked[te.e.nextSeq-1].path != te.e.paths[1] {
		t.Fatal("primary copy not on the lowest-RTT path")
	}
	if st := te.e.Stats(); st.Mode != "min_latency" {
		t.Errorf("mode %q", st.Mode)
	}
}

func TestPriorityQueue(t *testing.T) {
	te := newTestEngine(t, 50*time.Millisecond)
	te.e.SetAggregation(false)
	te.e.SetMode(MinLatency)
	te.e.paths[1].nextSend = te.now.Add(time.Hour) // pacing holds everything
	te.e.Send(tcpPacket(1000, 0x10))
	te.e.Send(udpPacket(1, 7777, 50))
	te.e.paths[1].nextSend = te.now
	te.e.flush(te.now)
	d := te.sent[1]
	var first []byte
	for _, fs := range d {
		if ip, ok := fs[0].(*wire.IP); ok {
			first = ip.Packet
			break
		}
	}
	if first == nil || first[9] != 17 {
		t.Fatal("real-time packet did not jump the bulk queue")
	}
}

func TestReceiverPassesRealtimeInMinLatency(t *testing.T) {
	var r receiver
	var got []int
	deliver := func(p []byte) { got = append(got, len(p)) }
	game, data := udpPacket(1, 7777, 72), tcpPacket(10, 0x10)
	r.onIP(t0, 1, data, deliver)
	r.onIP(t0, 3, game, deliver) // held: max_throughput keeps order
	if len(got) != 1 {
		t.Fatalf("delivered %v before the gap was filled", got)
	}
	r.passRealtime = true
	r.onIP(t0, 5, game, deliver) // gap at 2 and 4: goes out at once
	if want := []int{50, 100}; !slices.Equal(got, want) {
		t.Fatalf("delivered %v, want %v", got, want)
	}
}

func TestAggregation(t *testing.T) {
	te := newTestEngine(t, 50*time.Millisecond)
	for range 10 {
		te.send(udpPacket(1, 7777, 100))
	}
	if n, _ := te.ipFrames(1); n != 0 {
		t.Fatalf("%d small frames sent before the aggregation wait", n)
	}
	te.send(tcpPacket(1200, 0x10)) // a big packet takes the waiting ones along
	frames, datagrams := te.ipFrames(1)
	if frames != 11 || datagrams != 2 {
		t.Fatalf("%d frames in %d datagrams, want 11 in 2 (batch of 10 + the big one, or batch sharing)", frames, datagrams)
	}
	te.send(udpPacket(1, 7777, 100))
	te.now = te.now.Add(AggregateWait)
	te.e.Tick()
	if frames, _ := te.ipFrames(1); frames != 12 {
		t.Fatalf("small frame not sent after the wait: %d frames", frames)
	}
	te.e.SetAggregation(false)
	te.send(udpPacket(1, 7777, 100))
	if frames, _ := te.ipFrames(1); frames != 13 {
		t.Fatal("aggregation off still holds small packets")
	}
}

// A burst of small packets (same instant) shares one path's batch instead of
// spreading over paths whose pacing slots are free (e2e on Linux: 17 %).
func TestAggregationBurstSticksToBatch(t *testing.T) {
	te := newTestEngine(t, 50*time.Millisecond, 50*time.Millisecond)
	te.e.SetRedundancy(RedundancyOff)
	for range 6 {
		te.e.Send(udpPacket(1, 7777, 100))
	}
	te.now = te.now.Add(AggregateWait)
	te.e.Tick()
	f1, d1 := te.ipFrames(1)
	f2, d2 := te.ipFrames(2)
	if f1+f2 != 6 || d1+d2 != 1 {
		t.Fatalf("6 small packets: %d+%d frames in %d+%d datagrams, want one datagram", f1, f2, d1, d2)
	}
}

func TestAggregationRespectsMTU(t *testing.T) {
	te := newTestEngine(t, 50*time.Millisecond)
	te.e.paths[1].mtu = 600
	for range 10 {
		te.send(udpPacket(1, 7777, 150)) // 178 B packets, 3 per 600-byte datagram
	}
	te.now = te.now.Add(AggregateWait)
	te.e.Tick()
	for _, d := range te.sent[1] {
		if _, probe := d[0].(*wire.Ping); probe {
			continue // MTU probes are padded on purpose
		}
		size := DatagramOverhead
		for _, f := range d {
			size += frameSize(f)
		}
		if size > 600 {
			t.Fatalf("datagram of %d bytes on a 600-byte path", size)
		}
	}
	if frames, datagrams := te.ipFrames(1); frames != 10 || datagrams != 4 {
		t.Fatalf("%d frames in %d datagrams, want 10 in 4", frames, datagrams)
	}
}

func TestQualityGuards(t *testing.T) {
	te := newTestEngine(t, 50*time.Millisecond, 200*time.Millisecond)
	te.e.Tick()
	if st := te.e.Stats(); !st.Paths[1].Guarded || st.Paths[0].Guarded {
		t.Fatalf("guarded %v/%v, want path 2 (4× the best RTT)", st.Paths[0].Guarded, st.Paths[1].Guarded)
	}
	te.e.SetAggregation(false)
	for range 50 {
		te.send(tcpPacket(1200, 0x10))
	}
	if n, _ := te.ipFrames(2); n != 0 {
		t.Fatalf("guarded path got %d data packets", n)
	}
	if hold := te.e.holdTime(); hold != holdTime(50*time.Millisecond, 50*time.Millisecond) {
		t.Errorf("hold %v counts the guarded path", hold)
	}
	te.e.SetRedundancy(RedundancyOn)
	te.e.paths[1].inflight = 0
	te.send(udpPacket(1, 7777, 100))
	if n, _ := te.ipFrames(2); n == 0 {
		t.Fatal("guarded path got no redundant copy")
	}
	// hysteresis: released only below 0.8 × the limit (150 ms → 120 ms)
	p := te.e.paths[2]
	p.rtt.srtt = 140 * time.Millisecond
	te.e.Tick()
	if !p.guarded {
		t.Fatal("released inside the hysteresis band")
	}
	p.rtt.srtt = 110 * time.Millisecond
	te.e.Tick()
	if p.guarded {
		t.Fatal("still guarded below the release threshold")
	}
}

func TestGuardAbsoluteRTT(t *testing.T) {
	te := newTestEngine(t, 300*time.Millisecond, 600*time.Millisecond)
	te.e.Tick()
	if !te.e.paths[2].guarded {
		t.Fatal("600 ms path not guarded while a 300 ms path exists")
	}
	slow := newTestEngine(t, 700*time.Millisecond, 900*time.Millisecond)
	slow.e.Tick()
	if slow.e.paths[1].guarded || slow.e.paths[2].guarded {
		t.Fatal("all paths slow: within 3× of the best nothing is guarded")
	}
}

func TestAntiFlap(t *testing.T) {
	te := newTestEngine(t, 50*time.Millisecond)
	p := te.e.paths[1]
	if p.pongsToAlive() != PongsToAlive {
		t.Fatal("first life")
	}
	te.e.MarkDead(1) // first death: no penalty
	if p.pongsToAlive() != PongsToAlive {
		t.Fatalf("pongs %d after the first death", p.pongsToAlive())
	}
	for _, want := range []int{6, 12, 12} {
		te.e.setState(p, Alive, te.now) // recovered
		te.now = te.now.Add(10 * time.Second)
		te.e.MarkDead(1) // died 10 s later: flapping
		if got := p.pongsToAlive(); got != want {
			t.Fatalf("pongs %d, want %d", got, want)
		}
	}
	te.e.setState(p, Alive, te.now)
	te.now = te.now.Add(FlapReset)
	p.lastRecv = te.now
	te.e.Tick()
	if got := p.pongsToAlive(); got != PongsToAlive {
		t.Fatalf("pongs %d after 5 min stable, want %d", got, PongsToAlive)
	}
}

func TestStreamPath(t *testing.T) {
	te := newTestEngine(t, 50*time.Millisecond, 50*time.Millisecond)
	te.e.SetAggregation(false)
	te.e.SetRedundancy(RedundancyOff)
	te.e.SetStream(1, true)
	te.send(tcpPacket(1000, 0x10))
	if n, _ := te.ipFrames(2); n != 1 {
		t.Fatal("equal paths: the UDP path should win over the stream path")
	}
	te.e.paths[2].inflight = 1 << 20 // UDP path full
	for range 5 {
		te.send(tcpPacket(1000, 0x10))
	}
	p := te.e.paths[1]
	if len(p.sent) < 4 {
		t.Fatalf("stream path carries %d packets", len(p.sent))
	}
	// later packets acked, first one not: no fast retransmit on a stream path
	te.now = te.now.Add(300 * time.Millisecond)
	var acks []wire.Frame
	for _, sp := range p.sent[1:] {
		acks = append(acks, &wire.Ack{Largest: sp.seq})
	}
	te.e.Receive(1, 50, acks)
	if p.lost != 0 || len(te.e.rtx) != 0 {
		t.Fatalf("stream path: lost %d, re-sends %d", p.lost, len(te.e.rtx))
	}
	te.now = te.now.Add(StreamLossAge + time.Second)
	te.e.detectLoss(te.now)
	if p.lost == 0 {
		t.Fatal("stream path: nothing lost after StreamLossAge")
	}
	if st := te.e.Stats(); !st.Paths[0].Stream {
		t.Error("stats: stream flag")
	}
}

func TestHoldAlive(t *testing.T) {
	te := newTestEngine(t, 50*time.Millisecond)
	te.e.HoldAlive(1, te.now.Add(3*time.Second))
	te.now = te.now.Add(2 * time.Second) // silent: would be DEAD after SilenceDead
	te.e.Tick()
	if st, _ := te.e.PathState(1); st == Dead {
		t.Fatal("held path declared dead")
	}
	te.now = te.now.Add(2 * time.Second)
	te.e.Tick()
	if st, _ := te.e.PathState(1); st != Dead {
		t.Fatalf("state %v after the hold, want dead", st)
	}
}

// A sleeping backup path that wakes for data is not killed for the silence
// of its slow-ping period (e2e backup: 2.4 s without an upload path).
func TestWokenPathNotKilled(t *testing.T) {
	te := newTestEngine(t, 50*time.Millisecond)
	te.e.SetAggregation(false)
	te.e.SetPathConfig(1, PathConfig{Priority: Backup})
	p := te.e.paths[1]
	te.e.setState(p, Sleeping, te.now)
	p.lastRecv = te.now
	p.pings = nil
	te.now = te.now.Add(1500 * time.Millisecond) // between slow pings
	te.e.Send(tcpPacket(1000, 0x10))             // wakes it
	if p.state != Alive {
		t.Fatalf("state %v after data, want alive", p.state)
	}
	te.e.Tick()
	if p.state == Dead {
		t.Fatal("woken path killed for its sleeping-period silence")
	}
}

func TestNoteSent(t *testing.T) {
	te := newTestEngine(t, 50*time.Millisecond)
	te.e.NoteSent(1, 5000)
	te.e.NoteSent(9, 5000) // unknown path: ignored
	if st := te.e.Stats(); st.Paths[0].Used != 5000 || st.Paths[0].TxBytes != 5000 {
		t.Fatalf("used %d tx %d", st.Paths[0].Used, st.Paths[0].TxBytes)
	}
}

// TestSimRedundancyOnLossyLinks: with redundancy on, 5 % random loss on both
// links is covered by the second copy instead of re-sends.
func TestSimRedundancyOnLossyLinks(t *testing.T) {
	lossy := []linkSpec{
		{mbps: 8, rtt: 60 * time.Millisecond, loss: 0.05},
		{mbps: 8, rtt: 60 * time.Millisecond, loss: 0.05},
	}
	rtx := func(r Redundancy) (uint64, uint64) {
		s := newSim(t, lossy)
		s.a.e.SetRedundancy(r)
		s.run(2 * time.Second)
		s.startSource(s.a, 2, pktSize, 10*time.Second)
		s.run(12 * time.Second)
		var n uint64
		for _, p := range s.a.e.Stats().Paths {
			n += p.Retransmit
			t.Logf("%v path %d: retrans %d lost %d twins %d tx_pkts %d", r, p.ID, p.Retransmit, p.Lost, p.Twins, p.TxPkts)
		}
		return n, s.b.e.Stats().Dups
	}
	off, _ := rtx(RedundancyOff)
	on, dups := rtx(RedundancyOn)
	t.Logf("re-sends: off %d, on %d (receiver dups %d)", off, on, dups)
	if on*5 > off || dups == 0 {
		t.Errorf("redundancy on: %d re-sends vs %d off, %d dups; want ≥ 5× fewer", on, off, dups)
	}
}

func TestGuardIgnoresPathsThatCarryNoData(t *testing.T) {
	te := newTestEngine(t, 70*time.Millisecond, 20*time.Millisecond)
	fast := te.e.paths[2]
	for _, tt := range []struct {
		name  string
		setup func()
	}{
		{"sleeping backup", func() { fast.cfg.Priority, fast.state = Backup, Sleeping }},
		{"alive backup next to a normal path", func() { fast.cfg.Priority, fast.state = Backup, Alive }},
		{"suspect", func() { fast.cfg.Priority, fast.state = Normal, Suspect }},
	} {
		tt.setup()
		te.e.updateGuards()
		if te.e.paths[1].guarded {
			t.Errorf("%s: the working normal path is guarded against a path that carries no data", tt.name)
		}
	}
}
