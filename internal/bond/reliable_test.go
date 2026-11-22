package bond

import (
	"encoding/binary"
	"slices"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

// relPayload encodes one STREAM_DATA frame carrying offset off.
func relPayload(t *testing.T, off uint64, size int) []byte {
	t.Helper()
	b, err := wire.AppendFrame(nil, &wire.StreamData{StreamID: 1, Offset: off, Data: make([]byte, size)})
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestIsReliable(t *testing.T) {
	tests := []struct {
		pkt  []byte
		want bool
	}{
		{nil, false},
		{[]byte{0x45, 0}, false},
		{[]byte{0x20}, true},
		{[]byte{0x25, 0, 0}, true},
		{[]byte{0x10}, false},
	}
	for _, tt := range tests {
		if got := IsReliable(tt.pkt); got != tt.want {
			t.Errorf("IsReliable(%x) = %v", tt.pkt, got)
		}
	}
	e := New(Config{Transmit: func(uint8, []wire.Frame) error { return nil }})
	if _, err := e.SendReliable([]byte{0x45}); err == nil {
		t.Error("SendReliable accepted an IPv4 packet")
	}
}

func TestReceiverReliable(t *testing.T) {
	rel := []byte{0x22, 0, 0}
	ip := func(seq uint32) []byte { return []byte{0x45, byte(seq)} }
	tests := []struct {
		name  string
		steps []struct {
			seq uint32
			rel bool
		}
		tickAfter int // skip the head gap after this many steps (-1 = never)
		wantRel   []uint32
		wantIP    []byte
	}{
		{"past a gap, keeps its place", []struct {
			seq uint32
			rel bool
		}{{10, false}, {12, true}, {13, false}, {11, false}}, -1, []uint32{12}, []byte{10, 11, 13}},
		{"duplicate dropped", []struct {
			seq uint32
			rel bool
		}{{10, true}, {10, true}, {11, false}}, -1, []uint32{10}, []byte{11}},
		{"after its gap was skipped", []struct {
			seq uint32
			rel bool
		}{{10, false}, {12, false}, {11, true}}, 2, []uint32{11}, []byte{10, 12}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var r receiver
			var gotIP []byte
			var gotRel []uint32
			deliver := func(p []byte) { gotIP = append(gotIP, p[1]) }
			for i, s := range tt.steps {
				if i == tt.tickAfter {
					r.tick(t0.Add(time.Second), 10*time.Millisecond, deliver)
				}
				pkt := ip(s.seq)
				if s.rel {
					pkt = rel
				}
				if r.onIP(t0, s.seq, pkt, deliver) {
					gotRel = append(gotRel, s.seq)
				}
			}
			if !slices.Equal(gotRel, tt.wantRel) || !slices.Equal(gotIP, tt.wantIP) {
				t.Errorf("reliable %v ip %v, want %v %v", gotRel, gotIP, tt.wantRel, tt.wantIP)
			}
		})
	}
}

func TestReceiverReliableTooOld(t *testing.T) {
	var r receiver
	deliver := func([]byte) {}
	r.onIP(t0, 1, []byte{0x45}, deliver)
	for seq := uint32(3); seq < 3+seqSetBits+10; seq++ {
		r.onIP(t0, seq, []byte{0x45}, deliver)
	}
	if !r.onIP(t0, 2, []byte{0x22}, deliver) {
		t.Error("a reliable re-send older than the duplicate window was dropped")
	}
}

// relSink collects the stream offsets a node received.
type relSink struct {
	got  map[uint64]int
	dups int
}

func newRelSink(n *node) *relSink {
	rs := &relSink{got: map[uint64]int{}}
	n.frames = func(fs []wire.Frame) {
		for _, f := range fs {
			if d, ok := f.(*wire.StreamData); ok {
				if rs.got[d.Offset]++; rs.got[d.Offset] > 1 {
					rs.dups++
				}
			}
		}
	}
	return rs
}

// pushReliable keeps the reliable backlog of n filled with count payloads.
func pushReliable(t *testing.T, s *sim, n *node, count int, size int) *int {
	sent := new(int)
	var step func()
	step = func() {
		for *sent < count {
			full, err := n.e.SendReliable(relPayload(t, uint64(*sent), size))
			if err != nil {
				t.Fatal(err)
			}
			*sent++
			n.poke()
			if full {
				break
			}
		}
		if *sent < count {
			s.at(s.now.Add(5*time.Millisecond), step)
		}
	}
	step()
	return sent
}

func (rs *relSink) missing(count int) int {
	m := 0
	for i := range count {
		if rs.got[uint64(i)] == 0 {
			m++
		}
	}
	return m
}

func TestReliableSurvivesLoss(t *testing.T) {
	s := newSim(t, []linkSpec{{mbps: 8, rtt: 40 * time.Millisecond, loss: 0.08}, {mbps: 8, rtt: 60 * time.Millisecond, loss: 0.08},
		{mbps: 2, rtt: 300 * time.Millisecond, loss: 0.08}})
	rs := newRelSink(s.b)
	s.run(2 * time.Second)
	const count = 3000
	pushReliable(t, s, s.a, count, 1200)
	s.run(20 * time.Second)
	if m := rs.missing(count); m > 0 {
		t.Fatalf("%d of %d reliable payloads never arrived", m, count)
	}
	var rtx uint64
	for _, p := range s.a.e.Stats().Paths {
		rtx += p.Retransmit
	}
	if rtx == 0 {
		t.Error("no re-sends on 8 % loss")
	}
	t.Logf("received %d, dups %d, re-sends %d", len(rs.got), rs.dups, rtx)
	if st := s.a.e.Stats(); st.Reliable != 0 || len(s.a.e.unacked) != 0 {
		t.Errorf("backlog %d B, %d unacked after the transfer", st.Reliable, len(s.a.e.unacked))
	}
}

func TestReliablePathDeath(t *testing.T) {
	s := newSim(t, []linkSpec{{mbps: 8, rtt: 40 * time.Millisecond}, {mbps: 8, rtt: 60 * time.Millisecond}})
	rs := newRelSink(s.b)
	s.run(2 * time.Second)
	const count = 4000
	pushReliable(t, s, s.a, count, 1200)
	s.run(1500 * time.Millisecond)
	s.setLink(1, false) // dies with packets in flight and in its queue
	s.run(20 * time.Second)
	if m := rs.missing(count); m > 0 {
		t.Fatalf("%d of %d reliable payloads lost with the dead path", m, count)
	}
	if st := s.pathStats(s.a, 1); st.State != Dead.String() {
		t.Errorf("path 1 %s, want dead", st.State)
	}
}

func TestReliableNeverDroppedFromQueue(t *testing.T) {
	s := newSim(t, []linkSpec{{mbps: 2, rtt: 100 * time.Millisecond}})
	rs := newRelSink(s.b)
	s.run(2 * time.Second)
	const count = 600 // ~3 s of the link: far past the IP queue's drop delay
	for i := range count {
		if _, err := s.a.e.SendReliable(relPayload(t, uint64(i), 1200)); err != nil {
			t.Fatal(err)
		}
	}
	s.a.poke()
	s.run(15 * time.Second)
	if m := rs.missing(count); m > 0 {
		t.Fatalf("%d of %d queued reliable payloads lost", m, count)
	}
	if d := s.a.e.Stats().Dropped; d != 0 {
		t.Errorf("dropped %d", d)
	}
}

func TestReliableBacklogRoom(t *testing.T) {
	s := newSim(t, []linkSpec{{mbps: 8, rtt: 40 * time.Millisecond}})
	s.run(2 * time.Second)
	full := false
	for i := 0; !full; i++ {
		var err error
		if full, err = s.a.e.SendReliable(relPayload(t, uint64(i), 1200)); err != nil {
			t.Fatal(err)
		}
		if i > 1000 {
			t.Fatal("backlog never full")
		}
	}
	select {
	case <-s.a.e.Room():
		t.Fatal("room signaled while full")
	default:
	}
	s.a.poke()
	s.run(time.Second)
	select {
	case <-s.a.e.Room():
	default:
		t.Fatalf("no room signal, backlog %d", s.a.e.ReliableBacklog())
	}
}

func TestReliableSharesWithIPQueue(t *testing.T) {
	s := newSim(t, []linkSpec{{mbps: 8, rtt: 40 * time.Millisecond}})
	ipBytes, relBytes := 0, 0
	s.b.hook = func(pkt []byte) { ipBytes += len(pkt) }
	s.b.frames = func(fs []wire.Frame) {
		for _, f := range fs {
			if d, ok := f.(*wire.StreamData); ok {
				relBytes += len(d.Data)
			}
		}
	}
	s.run(2 * time.Second)
	s.startSource(s.a, 20, 1200, 6*time.Second) // UDP-like flood, far over the link
	pushReliable(t, s, s.a, 100000, 1200)
	s.run(6 * time.Second)
	total := float64(ipBytes + relBytes)
	if total == 0 {
		t.Fatal("nothing delivered")
	}
	if share := float64(relBytes) / total; share < 0.35 || share > 0.65 {
		t.Errorf("reliable share %.2f (ip %d B, reliable %d B), want about half", share, ipBytes, relBytes)
	}
	t.Logf("ip %d B, reliable %d B", ipBytes, relBytes)
}

// TestReliableFramesReturned checks that Receive hands the stream frames of
// a reliable payload to the caller.
func TestReliableFramesReturned(t *testing.T) {
	e := New(Config{Transmit: func(uint8, []wire.Frame) error { return nil }})
	e.AddPath(1, PathConfig{}, true)
	pl := relPayload(t, 7, 3)
	var seq [4]byte
	binary.BigEndian.PutUint32(seq[:], 5)
	rest := e.Receive(1, 100, []wire.Frame{&wire.IP{Seq: 5, Packet: pl}, &wire.PathGone{PathID: 2}})
	if len(rest) != 2 {
		t.Fatalf("rest %d frames, want stream data + path gone", len(rest))
	}
	if d, ok := rest[0].(*wire.StreamData); !ok || d.Offset != 7 || len(d.Data) != 3 {
		t.Errorf("rest[0] = %#v", rest[0])
	}
}

// TestReliableLoadKeepsPathAlive: a reliable (PEP) stream saturating a
// deep-buffered path does not starve its keepalives — PINGs bypass the
// engine queues — so the path never goes DEAD while PONGs still arrive
// (phase 18, suspect 1 of the modem path flap under PEP load).
func TestReliableLoadKeepsPathAlive(t *testing.T) {
	for _, q := range []time.Duration{150 * time.Millisecond, time.Second, 2 * time.Second} {
		specs := []linkSpec{
			{mbps: 40, rtt: 50 * time.Millisecond},
			{mbps: 25, rtt: 70 * time.Millisecond, queue: q},
		}
		s := newSim(t, specs)
		rs := newRelSink(s.b)
		s.run(time.Second)
		pushReliable(t, s, s.a, 6000, 1200)
		s.run(8 * time.Second)
		if m := rs.missing(6000); m > 0 {
			t.Errorf("queue %v: %d of 6000 payloads missing", q, m)
		}
		for _, n := range []*node{s.a, s.b} {
			for _, st := range n.states[2] {
				if st == Dead {
					t.Errorf("queue %v: path 2 states %v, want never dead", q, n.states[2])
					break
				}
			}
		}
	}
}
