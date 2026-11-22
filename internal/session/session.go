package session

import (
	"errors"
	"fmt"
	"net/netip"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zyndata/cross-stream/internal/bond"
	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/wire"
)

// Timers and limits from protocol.md section 1 and decisions.md.
const (
	RekeyAfterTime    = 10 * time.Minute
	RekeyAfterPackets = 1 << 32
	OldKeyLifetime    = 30 * time.Second
	KeepaliveInterval = 10 * time.Second // hub sends PING when idle
	DeadAfter         = 30 * time.Second // hub re-handshakes after this much silence
	MaxPaths          = 64               // paths per session; DATA on a further path id is dropped
)

// Errors returned by Session.
var (
	ErrNoKeys = errors.New("session: no confirmed keys")
	ErrAuth   = errors.New("session: packet failed authentication or replay check")
	ErrNoPath = errors.New("session: no path")
	ErrPaths  = errors.New("session: too many paths")
)

// Sender writes one outer packet to a remote address. *net.UDPConn implements it.
type Sender interface {
	WriteToUDPAddrPort(b []byte, addr netip.AddrPort) (int, error)
}

// Path is one way to reach the peer: path id, current remote address and the
// local socket it was heard on.
type Path struct {
	ID       uint8
	Addr     netip.AddrPort
	Conn     Sender
	LastRecv time.Time
	Stream   bool // Conn is a reliable stream (TCP/WSS)
}

// streamer is implemented by senders over a reliable stream transport.
type streamer interface{ Stream() bool }

func isStream(c Sender) bool {
	st, ok := c.(streamer)
	return ok && st.Stream()
}

// keyset is one handshake's transport keys with their own replay window.
type keyset struct {
	send    *crypto.DataCipher
	recv    *crypto.DataCipher
	replay  crypto.ReplayWindow
	created time.Time
	expires time.Time // only for the previous keyset
	sent    atomic.Uint64
}

func newKeyset(k crypto.Keys, now time.Time) *keyset {
	return &keyset{send: crypto.NewDataCipher(k.Send), recv: crypto.NewDataCipher(k.Recv), created: now}
}

// Options configure a Session. Zero values use defaults.
type Options struct {
	Now func() time.Time
	// Deliver receives the peer's IP packets in sequence order (must not keep pkt).
	Deliver func(pkt []byte)
	// OnState is told about path state changes; it runs with the bonding
	// engine locked and may only use SendOn.
	OnState func(pathID uint8, s bond.State)
	// OnPathAddr is told when a path is first heard or its source address changes.
	OnPathAddr func(pathID uint8, addr netip.AddrPort)
	// MaxTunnelMTU caps the tunnel MTU reported by the engine.
	MaxTunnelMTU int
}

// Session is one hub↔server session: keys, replay windows, paths, counters
// and the bonding engine that spreads IP packets over the paths. It is shared
// by hub and server and safe for concurrent use.
type Session struct {
	id   uint32
	now  func() time.Time
	opts Options
	bond *bond.Engine
	stop chan struct{}
	once sync.Once

	mu    sync.Mutex
	cur   *keyset // used for sending
	next  *keyset // responder keys not yet confirmed by the initiator
	prev  *keyset // kept OldKeyLifetime after a rekey for late packets
	paths map[uint8]*Path
	cfgs  map[uint8]bond.PathConfig // policy for paths, also before they exist
	last  time.Time                 // last authenticated packet

	rxBytes atomic.Uint64
	txBytes atomic.Uint64
}

// New returns a session without keys or paths. Call Start to run its timers.
func New(id uint32, opts Options) *Session {
	if opts.Now == nil {
		opts.Now = time.Now
	}
	if opts.Deliver == nil {
		opts.Deliver = func([]byte) {}
	}
	s := &Session{id: id, now: opts.Now, opts: opts, stop: make(chan struct{}),
		paths: make(map[uint8]*Path), cfgs: make(map[uint8]bond.PathConfig)}
	s.bond = bond.New(bond.Config{Now: opts.Now, Transmit: s.transmit, Deliver: opts.Deliver,
		OnState: opts.OnState, MaxTunnelMTU: opts.MaxTunnelMTU})
	return s
}

// Bond returns the session's bonding engine (stats, path policy).
func (s *Session) Bond() *bond.Engine { return s.bond }

// Start runs the bonding engine's timers until Close.
func (s *Session) Start() { go s.loop() }

// Close stops the timers. The session must not be used to send afterwards.
func (s *Session) Close() { s.once.Do(func() { close(s.stop) }) }

func (s *Session) loop() {
	t := time.NewTimer(0)
	defer t.Stop()
	for {
		select {
		case <-s.stop:
			return
		case <-t.C:
		case <-s.bond.Wake():
		}
		next := s.bond.Tick()
		t.Reset(max(next.Sub(s.now()), 50*time.Microsecond))
	}
}

func (s *Session) transmit(pathID uint8, frames []wire.Frame) error {
	return s.SendOn(pathID, frames...)
}

// ID returns the session id carried in every outer header.
func (s *Session) ID() uint32 { return s.id }

// InstallKeys adds the keys of a finished handshake. The initiator (hub)
// installs them confirmed and sends with them at once. The responder (server)
// installs them unconfirmed and keeps sending with the old keys until the first
// packet that opens with the new ones arrives, so a replayed or superseded
// handshake never breaks a working session.
func (s *Session) InstallKeys(k crypto.Keys, confirmed bool) {
	ks := newKeyset(k, s.now())
	s.mu.Lock()
	defer s.mu.Unlock()
	if confirmed {
		s.rotate(ks)
		return
	}
	s.next = ks
}

// rotate makes ks current and keeps the old current as prev. Caller holds mu.
func (s *Session) rotate(ks *keyset) {
	if s.cur != nil {
		s.cur.expires = s.now().Add(OldKeyLifetime)
		s.prev = s.cur
	}
	s.cur = ks
}

// SetPath adds or updates a path; a new one is probed before it carries data.
func (s *Session) SetPath(id uint8, addr netip.AddrPort, conn Sender) {
	s.AddPath(id, addr, conn, false)
}

// AddPath adds or updates a path. alive makes a new path usable at once
// (the handshake response just came over it).
func (s *Session) AddPath(id uint8, addr netip.AddrPort, conn Sender, alive bool) {
	s.mu.Lock()
	s.setPathLocked(id, addr, conn)
	cfg := s.cfgs[id]
	s.mu.Unlock()
	s.bond.AddPath(id, cfg, alive)
	s.bond.SetStream(id, isStream(conn))
}

// RemovePath forgets a path (relay left); its unacked packets are re-sent.
func (s *Session) RemovePath(id uint8) {
	s.mu.Lock()
	delete(s.paths, id)
	s.mu.Unlock()
	s.bond.RemovePath(id)
}

// PathAddr returns the current remote address of a path.
func (s *Session) PathAddr(id uint8) (netip.AddrPort, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p := s.paths[id]; p != nil {
		return p.Addr, true
	}
	return netip.AddrPort{}, false
}

// SetPathConfig sets a path's policy now and for when it (re)appears.
func (s *Session) SetPathConfig(id uint8, cfg bond.PathConfig) {
	s.mu.Lock()
	s.cfgs[id] = cfg
	s.mu.Unlock()
	s.bond.SetPathConfig(id, cfg)
}

// setPathLocked records a path; it returns true for a new path or a changed
// address. A stream path spread over several TCP connections (phase 20)
// moves between their source ports all the time: a new port from the same
// IP on a stream path follows the connection but is not a change.
func (s *Session) setPathLocked(id uint8, addr netip.AddrPort, conn Sender) bool {
	now := s.now()
	p, ok := s.paths[id]
	if !ok {
		s.paths[id] = &Path{ID: id, Addr: addr, Conn: conn, LastRecv: now, Stream: isStream(conn)}
		return true
	}
	sameStreamIP := p.Stream && isStream(conn) && p.Addr.Addr() == addr.Addr()
	changed := p.Addr != addr && !sameStreamIP
	p.Addr, p.Conn, p.LastRecv = addr, conn, now
	if changed {
		p.Stream = isStream(conn)
	}
	return changed
}

// Open authenticates and decrypts a DATA packet (header already parsed,
// payload = ciphertext) and records the path it came from (source-address
// roaming: the path follows the newest valid source). The bonding engine
// consumes IP, ACK, PING and PONG frames (IP packets go to Options.Deliver);
// Open returns the other frames.
func (s *Session) Open(h wire.Header, packet []byte, from netip.AddrPort, conn Sender) ([]wire.Frame, error) {
	frames, isNew, changed, err := s.open(h, packet, from, conn)
	if err != nil {
		return nil, err
	}
	if isNew { // an authenticated packet just came over it: usable at once
		s.mu.Lock()
		cfg := s.cfgs[h.PathID]
		s.mu.Unlock()
		s.bond.AddPath(h.PathID, cfg, true)
	}
	if isNew || changed {
		s.bond.SetStream(h.PathID, isStream(conn))
	}
	if changed && s.opts.OnPathAddr != nil {
		s.opts.OnPathAddr(h.PathID, from)
	}
	return s.bond.Receive(h.PathID, len(packet), frames), nil
}

func (s *Session) open(h wire.Header, packet []byte, from netip.AddrPort, conn Sender) (frames []wire.Frame, isNew, changed bool, err error) {
	if len(packet) < wire.HeaderLen {
		return nil, false, false, ErrAuth
	}
	hdr, ct := packet[:wire.HeaderLen], packet[wire.HeaderLen:]
	s.mu.Lock()
	defer s.mu.Unlock()
	now := s.now()
	if s.prev != nil && !now.Before(s.prev.expires) {
		s.prev = nil
	}
	var plain []byte
	var used *keyset
	newest := false
	for _, ks := range [...]*keyset{s.cur, s.next, s.prev} {
		if ks == nil || !ks.replay.Check(h.Counter) {
			continue
		}
		out, err := ks.recv.Open(nil, hdr, ct, h.Counter)
		if err != nil {
			continue
		}
		// Only the newest packet of the current keys moves a known path: a
		// reordered or delayed one must not point it back at an old address.
		newest = ks != s.prev && ks.replay.Newer(h.Counter)
		ks.replay.Update(h.Counter)
		plain, used = out, ks
		break
	}
	if used == nil {
		return nil, false, false, ErrAuth
	}
	if used == s.next {
		s.rotate(s.next)
		s.next = nil
	}
	_, known := s.paths[h.PathID]
	if !known && len(s.paths) >= MaxPaths {
		return nil, false, false, ErrPaths
	}
	// Parse before the path is registered: a path recorded here but never
	// reported as new would stay unknown to the bonding engine.
	frames, err = wire.ParseFrames(plain)
	if err != nil {
		return nil, false, false, fmt.Errorf("session open: %w", err)
	}
	if p := s.paths[h.PathID]; known && !newest {
		p.LastRecv = now
	} else {
		changed = s.setPathLocked(h.PathID, from, conn)
	}
	s.last = now
	s.rxBytes.Add(uint64(len(packet)))
	return frames, !known, changed, nil
}

// SendIP queues one IP packet (copied) for the bonding engine, which numbers
// it and sends it on the best path.
func (s *Session) SendIP(pkt []byte) {
	s.bond.Send(pkt)
}

// Send encrypts control frames into one DATA packet and sends it on the
// best path (alive, lowest RTT).
func (s *Session) Send(frames ...wire.Frame) error {
	id, ok := s.bond.BestPath()
	if !ok {
		return ErrNoPath
	}
	return s.SendOn(id, frames...)
}

// SendOn encrypts frames into one DATA packet and sends it on path id.
func (s *Session) SendOn(id uint8, frames ...wire.Frame) error {
	s.mu.Lock()
	ks := s.cur
	pp := s.paths[id]
	var p Path
	if pp != nil {
		p = *pp
	}
	s.mu.Unlock()
	if ks == nil {
		return ErrNoKeys
	}
	if pp == nil {
		return ErrNoPath
	}
	var plain []byte
	for _, f := range frames {
		var err error
		if plain, err = wire.AppendFrame(plain, f); err != nil {
			return fmt.Errorf("session send: %w", err)
		}
	}
	counter, err := ks.send.NextCounter()
	if err != nil {
		return fmt.Errorf("session send: %w", err)
	}
	ks.sent.Store(counter + 1)
	buf := wire.AppendHeader(make([]byte, 0, wire.HeaderLen+len(plain)+crypto.TagLen),
		wire.Header{Type: wire.TypeData, SessionID: s.id, PathID: p.ID, Counter: counter})
	buf = ks.send.Seal(buf, buf[:wire.HeaderLen], plain, counter)
	if _, err := p.Conn.WriteToUDPAddrPort(buf, p.Addr); err != nil {
		return fmt.Errorf("session send on path %d: %w", p.ID, err)
	}
	s.txBytes.Add(uint64(len(buf)))
	return nil
}

// NeedsRekey reports whether the initiator should start a new handshake:
// current keys older than RekeyAfterTime or RekeyAfterPackets sent.
func (s *Session) NeedsRekey() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.cur == nil {
		return false
	}
	return s.now().Sub(s.cur.created) >= RekeyAfterTime || s.cur.sent.Load() >= RekeyAfterPackets
}

// Stats is a snapshot of session counters.
type Stats struct {
	ID       uint32
	RxBytes  uint64
	TxBytes  uint64
	LastRecv time.Time
	Paths    []Path
	Bond     bond.Stats
}

// Stats returns a snapshot of the session counters, paths (sorted by id) and
// bonding engine.
func (s *Session) Stats() Stats {
	s.mu.Lock()
	st := Stats{ID: s.id, RxBytes: s.rxBytes.Load(), TxBytes: s.txBytes.Load(), LastRecv: s.last}
	for id := 0; id < 256; id++ {
		if p, ok := s.paths[uint8(id)]; ok {
			st.Paths = append(st.Paths, *p)
		}
	}
	s.mu.Unlock()
	st.Bond = s.bond.Stats()
	return st
}
