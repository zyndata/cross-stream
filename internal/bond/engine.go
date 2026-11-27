package bond

import (
	"crypto/rand"
	"encoding/binary"
	"slices"
	"sync"
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

// DefaultTunnelMTU is the tunnel MTU without a smaller path (protocol.md).
const DefaultTunnelMTU = 1360

// Config wires an Engine to its environment. Transmit, Deliver and OnState
// are called with the engine lock held and must not call the Engine.
type Config struct {
	Now func() time.Time
	// Transmit sends frames as one DATA datagram on a path.
	Transmit func(pathID uint8, frames []wire.Frame) error
	// Deliver receives in-order IP packets; it must not keep pkt.
	Deliver func(pkt []byte)
	// OnState is told about path state changes (optional).
	OnState func(pathID uint8, s State)
	// MaxTunnelMTU caps TunnelMTU (default DefaultTunnelMTU).
	MaxTunnelMTU int
	// FirstSeq is the first sequence number; nil = random.
	FirstSeq *uint32
}

type queued struct {
	pkt []byte
	at  time.Time
}

// Engine is one side of a bonded session: it numbers IP packets, schedules
// them over paths, acknowledges, re-sends lost ones and reorders what it
// receives. Safe for concurrent use.
type Engine struct {
	cfg   Config
	start time.Time
	wake  chan struct{}

	mu       sync.Mutex
	paths    map[uint8]*path
	order    []*path // by id
	nextSeq  uint32
	unacked  map[uint32]*sentPacket
	queue    []queued
	rtq      []queued // priority: critical packets, real-time ones in min_latency
	rtx      []*sentPacket
	relq     []queued // reliable payloads (stream frames): never dropped
	relBytes int      // bytes in relq
	relTurn  bool     // the reliable queue sends next when both have packets
	room     chan struct{}

	relWaiting bool // a SendReliable caller waits for Room
	recv       receiver
	overflow   uint64 // bytes/s, 0 = off
	dropped    uint64
	nextDrop   time.Time // earliest next soft drop from the send queue

	mode           Mode
	redundancy     Redundancy
	redundantUntil time.Time // auto redundancy is on until then
	noAggregate    bool
}

// New returns an engine without paths.
func New(cfg Config) *Engine {
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	if cfg.MaxTunnelMTU == 0 {
		cfg.MaxTunnelMTU = DefaultTunnelMTU
	}
	e := &Engine{cfg: cfg, start: cfg.Now(), wake: make(chan struct{}, 1), room: make(chan struct{}, 1),
		paths: make(map[uint8]*path), unacked: make(map[uint32]*sentPacket), redundancy: RedundancyAuto}
	if cfg.FirstSeq != nil {
		e.nextSeq = *cfg.FirstSeq
	} else {
		var b [4]byte
		_, _ = rand.Read(b[:]) // crypto/rand.Read never fails
		e.nextSeq = binary.BigEndian.Uint32(b[:])
	}
	e.recv.buf = make(map[uint32]held)
	return e
}

// Wake signals when the engine wants Tick to run before the deadline it
// returned last (new packets queued, ACK pending).
func (e *Engine) Wake() <-chan struct{} { return e.wake }

func (e *Engine) kick() {
	select {
	case e.wake <- struct{}{}:
	default:
	}
}

// AddPath adds a path or updates an existing one. alive marks it usable at
// once (e.g. the handshake just came over it); otherwise it is probed first.
func (e *Engine) AddPath(id uint8, cfg PathConfig, alive bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.cfg.Now()
	p := e.paths[id]
	if p == nil {
		p = newPath(id, cfg, now)
		e.paths[id] = p
		e.order = append(e.order, p)
		slices.SortFunc(e.order, func(a, b *path) int { return int(a.id) - int(b.id) })
	} else {
		e.setConfigLocked(p, cfg, now)
		p.fastProbe(now)
	}
	if alive && p.state == Dead {
		e.setState(p, Alive, now)
	}
	e.kick()
}

// RemovePath drops a path; its unacked packets are re-sent on other paths.
func (e *Engine) RemovePath(id uint8) {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.paths[id]
	if p == nil {
		return
	}
	e.killPath(e.cfg.Now(), p)
	delete(e.paths, id)
	e.order = slices.DeleteFunc(e.order, func(q *path) bool { return q == p })
	e.kick()
}

// MarkDead makes a path DEAD now (e.g. its relay's control channel was
// lost); it is probed and comes back after PongsToAlive PONGs.
func (e *Engine) MarkDead(id uint8) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p := e.paths[id]; p != nil && p.state != Dead {
		e.killPath(e.cfg.Now(), p)
		e.kick()
	}
}

// SetPathConfig changes a path's policy. It reports false for an unknown path.
func (e *Engine) SetPathConfig(id uint8, cfg PathConfig) bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	p := e.paths[id]
	if p == nil {
		return false
	}
	e.setConfigLocked(p, cfg, e.cfg.Now())
	e.kick()
	return true
}

func (e *Engine) setConfigLocked(p *path, cfg PathConfig, now time.Time) {
	p.cfg = cfg
	p.bucket.setRate(cfg.RateBps, now)
	if p.state == Sleeping {
		if cls, ok := p.dataClass(); ok && cls != Backup {
			e.setState(p, Alive, now)
		}
	}
}

// SetStream marks a path as carried over a reliable stream transport
// (TCP/WSS): no fast retransmit, StreamETAPenalty (architecture.md "Server
// transports").
func (e *Engine) SetStream(id uint8, stream bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p := e.paths[id]; p != nil {
		p.stream = stream
	}
}

// HoldAlive keeps a path from being declared DEAD for missed PONGs or
// silence until the given time (a speed test fills its queue on purpose).
func (e *Engine) HoldAlive(id uint8, until time.Time) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p := e.paths[id]; p != nil {
		p.holdUntil = until
	}
}

// SetUsed sets the bytes already counted against a path's cap.
func (e *Engine) SetUsed(id uint8, used uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p := e.paths[id]; p != nil {
		p.used = used
	}
}

// SetOverflow sets the overflow threshold in bytes/s (0 = off): secondary
// paths stay unused while normal paths already send at least that rate.
func (e *Engine) SetOverflow(bps uint64) {
	e.mu.Lock()
	e.overflow = bps
	e.mu.Unlock()
}

// Send queues one IP packet (copied) and sends what the paths allow now.
func (e *Engine) Send(pkt []byte) {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.cfg.Now()
	if len(e.queue)+len(e.rtq) >= QueueMaxPackets {
		e.dropped++
		return
	}
	q := queued{pkt: append([]byte(nil), pkt...), at: now}
	if c := classify(pkt); c.critical || (c.realtime && e.mode == MinLatency) {
		e.rtq = append(e.rtq, q)
	} else {
		e.queue = append(e.queue, q)
	}
	e.flush(now)
	if len(e.queue) > 0 || len(e.rtq) > 0 {
		e.kick()
	}
}

// NoteSent counts n bytes sent on a path outside the engine (speed test
// filler) against its rate limit and data cap.
func (e *Engine) NoteSent(id uint8, n int) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p := e.paths[id]; p != nil {
		now := e.cfg.Now()
		p.account(now, n, true)
		p.bucket.take(now, n)
	}
}

// Receive handles the frames of one authenticated datagram of size bytes that
// arrived on a path. It consumes IP, ACK, PING and PONG frames and returns
// the others, including the stream frames of reliable payloads (they alias
// the datagram). IP packet slices may be kept until delivered.
func (e *Engine) Receive(id uint8, size int, frames []wire.Frame) []wire.Frame {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.cfg.Now()
	p := e.paths[id]
	if p != nil {
		p.lastRecv = now
		p.account(now, size, false)
	}
	var rest []wire.Frame
	acked := false
	for _, f := range frames {
		switch f := f.(type) {
		case *wire.IP:
			if p != nil {
				p.rxData += uint64(len(f.Packet))
			}
			if e.recv.onIP(now, f.Seq, f.Packet, e.cfg.Deliver) {
				// A reliable payload: its stream frames go to the caller.
				if fs, err := wire.ParseFrames(f.Packet); err == nil {
					rest = append(rest, fs...)
				}
			}
		case *wire.Ack:
			e.onAckFrame(now, f)
			acked = true
		case *wire.Ping:
			if p != nil {
				e.transmit(now, p, &wire.Pong{TS: f.TS, ProbeLen: uint16(min(size, 0xFFFF))})
			}
		case *wire.Pong:
			if p != nil {
				e.onPong(now, p, f)
			}
		default:
			rest = append(rest, f)
		}
	}
	if acked {
		e.detectLoss(now)
	}
	if due, _ := e.recv.ackDue(now); due {
		e.sendAcks(now)
	} else if len(e.recv.pending) > 0 {
		e.kick()
	}
	e.flush(now)
	return rest
}

// BestPath returns the path for control frames: alive, recently heard,
// lowest RTT. ok is false when the engine has no path.
func (e *Engine) BestPath() (uint8, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p, _ := e.ackPaths(e.cfg.Now()); p != nil {
		return p.id, true
	}
	if len(e.order) > 0 {
		return e.order[0].id, true
	}
	return 0, false
}

// PathState returns a path's state.
func (e *Engine) PathState(id uint8) (State, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if p := e.paths[id]; p != nil {
		return p.state, true
	}
	return Dead, false
}

// TunnelMTU is the largest IP packet that fits every live probed path.
func (e *Engine) TunnelMTU() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.tunnelMTU()
}

func (e *Engine) tunnelMTU() int {
	m := e.cfg.MaxTunnelMTU
	for _, p := range e.order {
		if p.state != Dead && p.mtu > 0 {
			m = min(m, p.mtu-IPOverhead)
		}
	}
	return max(m, MinTunnelMTU)
}

// Stats returns a snapshot of all paths and counters.
func (e *Engine) Stats() Stats {
	e.mu.Lock()
	defer e.mu.Unlock()
	now := e.cfg.Now()
	st := Stats{Queue: len(e.queue) + len(e.rtq) + len(e.rtx) + len(e.relq), Reliable: e.relBytes, Reorder: len(e.recv.buf), Skipped: e.recv.skipped,
		Dropped: e.dropped, Delivered: e.recv.delivered, Dups: e.recv.dups, Late: e.recv.late, TunnelMTU: e.tunnelMTU(),
		Mode: e.mode.String(), Redundancy: e.redundancy.String(), Redundant: e.redundant(now), Aggregate: !e.noAggregate}
	for _, p := range e.order {
		st.Paths = append(st.Paths, p.stats(now))
	}
	return st
}

func (e *Engine) setState(p *path, s State, now time.Time) {
	if p.state == s {
		return
	}
	if s == Alive && p.state == Dead {
		p.becomeAlive(now)
	}
	if s == Alive && p.state == Sleeping {
		p.wokeAt = now // its last PONG may be 2 s old (slow pings)
	}
	p.state = s
	if s == Alive || s == Suspect {
		p.nextPing = earlier(p.nextPing, now.Add(PingInterval))
	}
	if e.cfg.OnState != nil {
		e.cfg.OnState(p.id, s)
	}
}

// ts converts a time to the PING timestamp (µs since engine start).
func (e *Engine) ts(t time.Time) uint64 { return uint64(t.Sub(e.start) / time.Microsecond) }

func (e *Engine) tsTime(ts uint64) time.Time {
	return e.start.Add(time.Duration(ts) * time.Microsecond)
}
