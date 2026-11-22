package bond

import (
	"container/heap"
	"encoding/binary"
	"math/rand/v2"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

// --- discrete-event simulator: two engines joined by emulated links ---

type event struct {
	at  time.Time
	n   int
	run func()
}

type eventQueue []*event

func (q eventQueue) Len() int { return len(q) }
func (q eventQueue) Less(i, j int) bool {
	if q[i].at.Equal(q[j].at) {
		return q[i].n < q[j].n
	}
	return q[i].at.Before(q[j].at)
}
func (q eventQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *eventQueue) Push(x any)   { *q = append(*q, x.(*event)) }
func (q *eventQueue) Pop() any {
	old := *q
	e := old[len(old)-1]
	*q = old[:len(old)-1]
	return e
}

type sim struct {
	t    *testing.T
	now  time.Time
	q    eventQueue
	n    int
	rng  *rand.Rand
	a, b *node
	idx  uint64 // next source packet index (shared by all sources)
}

// link is one direction of an emulated cellular link: rate, delay, random
// loss, a tail-drop queue and an MTU.
type link struct {
	rate     float64 // bytes/s
	delay    time.Duration
	jitter   time.Duration
	loss     float64
	queueMax time.Duration // queue limit expressed as drain time
	mtu      int           // outer bytes, 0 = no limit
	burst    time.Duration // > 0: arrivals are released in batches every burst/2..burst (LTE-like)
	stall    time.Duration // > 0: every 1–3 s delivery pauses for stall/2..stall, then releases at once
	up       bool

	nextBatch  time.Time
	stallStart time.Time
	stallEnd   time.Time

	busyUntil   time.Time
	lastArrival time.Time
}

type linkSpec struct {
	mbps   float64
	rtt    time.Duration
	loss   float64
	jitter time.Duration
	mtu    int
	queue  time.Duration // link buffer as drain time (default 150 ms)
	burst  time.Duration // batch release period (0 = smooth)
	bBurst time.Duration // b → a batch period if set (ACK direction of a download)
	stall  time.Duration // a → b delivery stalls (see link.stall)
}

// node is one side: an engine plus its tick scheduling and delivery log.
type node struct {
	s        *sim
	e        *Engine
	out      map[uint8]*link // links this node sends on
	peer     *node
	nextTick time.Time

	delivered []uint64 // packet indexes in delivery order
	lastIdx   uint64
	hasIdx    bool
	outOfOrd  int
	dupes     int
	gaps      []time.Duration // pauses between deliveries
	lastDeliv time.Time
	maxGap    time.Duration
	maxGapAt  time.Time
	bytes     int
	states    map[uint8][]State
	hook      func(pkt []byte)      // if set, gets every delivered packet instead of the stats above
	frames    func(fs []wire.Frame) // if set, gets the frames Receive did not consume
}

func newSim(t *testing.T, specs []linkSpec) *sim { return newSimSeed(t, specs, 1) }

// newSimSeed is newSim with another seed for the links' randomness.
func newSimSeed(t *testing.T, specs []linkSpec, seed uint64) *sim {
	s := &sim{t: t, now: time.Unix(1_700_000_000, 0), rng: rand.New(rand.NewPCG(seed, 2))}
	s.a, s.b = s.newNode(), s.newNode()
	s.a.peer, s.b.peer = s.b, s.a
	for i, sp := range specs {
		id := uint8(i + 1)
		mk := func() *link {
			bps := sp.mbps * 1e6 / 8
			q := sp.queue
			if q == 0 {
				q = 150 * time.Millisecond
			}
			return &link{rate: bps, delay: sp.rtt / 2, jitter: sp.jitter, loss: sp.loss,
				queueMax: q, mtu: sp.mtu, burst: sp.burst, up: true}
		}
		s.a.out[id], s.b.out[id] = mk(), mk()
		if sp.bBurst > 0 {
			s.b.out[id].burst = sp.bBurst
		}
		s.a.out[id].stall = sp.stall
		s.a.e.AddPath(id, PathConfig{}, false)
		s.b.e.AddPath(id, PathConfig{}, false)
	}
	s.a.schedule(s.now)
	s.b.schedule(s.now)
	return s
}

func (s *sim) newNode() *node {
	n := &node{s: s, out: make(map[uint8]*link), states: make(map[uint8][]State)}
	first := uint32(0xFFFFFF00) // exercise sequence wrap-around
	n.e = New(Config{
		Now:      func() time.Time { return s.now },
		Transmit: n.transmit,
		Deliver:  n.deliver,
		OnState:  func(id uint8, st State) { n.states[id] = append(n.states[id], st) },
		FirstSeq: &first,
	})
	return n
}

func (s *sim) at(t time.Time, f func()) {
	s.n++
	heap.Push(&s.q, &event{at: t, n: s.n, run: f})
}

// run processes events until d from now.
func (s *sim) run(d time.Duration) {
	end := s.now.Add(d)
	for len(s.q) > 0 && !s.q[0].at.After(end) {
		ev := heap.Pop(&s.q).(*event)
		s.now = ev.at
		ev.run()
	}
	s.now = end
}

func (n *node) schedule(t time.Time) {
	if !n.nextTick.IsZero() && !t.Before(n.nextTick) && n.nextTick.After(n.s.now) {
		return
	}
	n.nextTick = t
	n.s.at(t, func() {
		if !n.nextTick.Equal(t) {
			return
		}
		n.nextTick = time.Time{}
		n.schedule(n.e.Tick())
	})
}

// poke runs Tick soon if the engine asked for it.
func (n *node) poke() {
	select {
	case <-n.e.Wake():
		n.schedule(n.s.now)
	default:
	}
}

func (n *node) transmit(id uint8, frames []wire.Frame) error {
	var plain []byte
	for _, f := range frames {
		var err error
		if plain, err = wire.AppendFrame(plain, f); err != nil {
			n.s.t.Fatalf("encode frame: %v", err)
		}
	}
	size := DatagramOverhead + len(plain)
	l := n.out[id]
	s := n.s
	if l == nil || !l.up || (l.mtu > 0 && size > l.mtu) || s.rng.Float64() < l.loss {
		return nil
	}
	start := later(l.busyUntil, s.now)
	if start.Sub(s.now) > l.queueMax {
		return nil // tail drop
	}
	l.busyUntil = start.Add(time.Duration(float64(size) / l.rate * float64(time.Second)))
	arr := l.busyUntil.Add(l.delay)
	if l.jitter > 0 {
		arr = arr.Add(time.Duration(s.rng.Int64N(int64(l.jitter))))
	}
	arr = later(arr, l.lastArrival) // one link keeps FIFO order
	if l.burst > 0 {
		for l.nextBatch.Before(arr) {
			l.nextBatch = later(l.nextBatch, arr.Add(-l.burst)).Add(l.burst/2 + time.Duration(s.rng.Int64N(int64(l.burst/2)+1)))
		}
		arr = l.nextBatch
	}
	if l.stall > 0 {
		if l.stallStart.IsZero() {
			l.stallStart = s.now.Add(time.Second)
		}
		for !arr.Before(l.stallEnd) && !arr.Before(l.stallStart) {
			l.stallEnd = l.stallStart.Add(l.stall/2 + time.Duration(s.rng.Int64N(int64(l.stall/2)+1)))
			if arr.Before(l.stallEnd) {
				break
			}
			l.stallStart = l.stallEnd.Add(time.Second + time.Duration(s.rng.Int64N(int64(2*time.Second))))
		}
		if !arr.Before(l.stallStart) && arr.Before(l.stallEnd) {
			arr = l.stallEnd
		}
		l.lastArrival = later(l.lastArrival, arr)
	}
	l.lastArrival = arr
	peer := n.peer
	s.at(arr, func() {
		if !l.up {
			return
		}
		fs, err := wire.ParseFrames(plain)
		if err != nil {
			s.t.Fatalf("parse frames: %v", err)
		}
		rest := peer.e.Receive(id, size, fs)
		if peer.frames != nil && len(rest) > 0 {
			peer.frames(rest)
		}
		peer.poke()
	})
	return nil
}

func (n *node) deliver(pkt []byte) {
	if n.hook != nil {
		n.hook(pkt)
		return
	}
	idx := binary.BigEndian.Uint64(pkt)
	switch {
	case n.hasIdx && idx == n.lastIdx:
		n.dupes++
	case n.hasIdx && idx < n.lastIdx:
		n.outOfOrd++
	}
	if !n.lastDeliv.IsZero() {
		if g := n.s.now.Sub(n.lastDeliv); g > n.maxGap {
			n.maxGap, n.maxGapAt = g, n.s.now
		}
	}
	n.lastDeliv = n.s.now
	n.lastIdx, n.hasIdx = idx, true
	n.delivered = append(n.delivered, idx)
	n.bytes += len(pkt)
}

// source sends constant-rate packets of size bytes from n for d.
type source struct {
	n     *node
	size  int
	rate  float64 // bytes/s
	until time.Time
	stop  bool
}

func (s *sim) startSource(n *node, mbps float64, size int, d time.Duration) *source {
	src := &source{n: n, size: size, rate: mbps * 1e6 / 8, until: s.now.Add(d)}
	src.next()
	return src
}

func (src *source) next() {
	s := src.n.s
	if src.stop || !s.now.Before(src.until) {
		return
	}
	pkt := make([]byte, src.size)
	binary.BigEndian.PutUint64(pkt, s.idx)
	s.idx++
	src.n.e.Send(pkt)
	src.n.poke()
	s.at(s.now.Add(time.Duration(float64(src.size)/src.rate*float64(time.Second))), src.next)
}

func (s *sim) setLink(id uint8, up bool) {
	s.a.out[id].up, s.b.out[id].up = up, up
}

// mbpsSince returns the goodput at b since a mark.
type mark struct {
	at    time.Time
	bytes int
	data  map[uint8]uint64
}

func (s *sim) mark() mark {
	m := mark{at: s.now, bytes: s.b.bytes, data: map[uint8]uint64{}}
	for _, p := range s.a.e.Stats().Paths {
		m.data[p.ID] = p.TxData
	}
	return m
}

func (s *sim) goodput(m mark) float64 {
	return float64(s.b.bytes-m.bytes) * 8 / s.now.Sub(m.at).Seconds() / 1e6
}

// pathMbps returns the data rate each path of a carried since m.
func (s *sim) pathMbps(m mark) map[uint8]float64 {
	out := map[uint8]float64{}
	for _, p := range s.a.e.Stats().Paths {
		out[p.ID] = float64(p.TxData-m.data[p.ID]) * 8 / s.now.Sub(m.at).Seconds() / 1e6
	}
	return out
}

func (s *sim) pathStats(n *node, id uint8) PathStats {
	for _, p := range n.e.Stats().Paths {
		if p.ID == id {
			return p
		}
	}
	s.t.Fatalf("no path %d", id)
	return PathStats{}
}

func (s *sim) checkOrder() {
	s.t.Helper()
	if s.b.outOfOrd > 0 || s.b.dupes > 0 {
		s.t.Errorf("delivery: %d out of order, %d duplicates", s.b.outOfOrd, s.b.dupes)
	}
}
