package pep

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

// Defaults (architecture.md "Performance Enhancing Proxy — PEP").
const (
	DefaultWindow     = 4 << 20   // per-stream receive window
	DefaultMemCap     = 256 << 20 // receive windows granted per session, in total
	MinWindow         = 64 << 10  // credit a stream gets even when the cap is used up (at most Window/4)
	DefaultMaxStreams = 4096
	DefaultIdle       = 2 * time.Hour
	DefaultOpenWait   = 20 * time.Second // hub: wait for STREAM_OPEN_RESULT
	DefaultDialWait   = 10 * time.Second // server: origin connect timeout
	SendBuffer        = 256 << 10        // bytes a stream's writer may queue
	TombstoneFor      = 2 * time.Minute  // ids of removed streams: late frames ignored
	tickEvery         = time.Second
	streamDataHeader  = wire.FrameHeaderLen + 12
)

// STREAM_RESET codes (protocol.md "Frames").
const (
	ResetAbort    uint8 = 0 // local side aborted (client or origin reset, error)
	ResetUnknown  uint8 = 1 // frame for a stream the receiver does not know
	ResetFlow     uint8 = 2 // data beyond the granted window or final offset
	ResetIdle     uint8 = 3 // idle timeout
	ResetShutdown uint8 = 4 // session closing
	ResetLimit    uint8 = 5 // stream limit reached
)

// Errors.
var (
	ErrClosed  = errors.New("pep: session closed")
	ErrLimit   = errors.New("pep: stream limit reached")
	ErrNoServe = errors.New("pep: peer does not accept streams")
)

// ResetError is a stream ended by a STREAM_RESET.
type ResetError struct {
	Code   uint8
	Remote bool
}

func (e *ResetError) Error() string {
	side := "local"
	if e.Remote {
		side = "peer"
	}
	return fmt.Sprintf("pep: stream reset by %s (code %d)", side, e.Code)
}

// OpenError is a refused STREAM_OPEN.
type OpenError struct{ Code uint8 }

func (e *OpenError) Error() string { return fmt.Sprintf("pep: open failed (code %d)", e.Code) }

// Link carries reliable payloads (encoded stream frames) to the peer; the
// bond engine implements it.
type Link interface {
	// SendReliable queues a payload; full asks the caller to wait for Room
	// before queueing more stream data.
	SendReliable(b []byte) (full bool, err error)
	Room() <-chan struct{}
	// TunnelMTU is the largest payload one sequenced packet carries.
	TunnelMTU() int
}

// Config configures a Session. Zero values use the defaults.
type Config struct {
	Link Link
	// Dial connects to an origin (server side). nil: STREAM_OPEN is refused.
	Dial func(ctx context.Context, dst netip.AddrPort) (net.Conn, error)
	// Serve runs a dialed stream (server side), usually Pipe; it owns both.
	Serve      func(st *Stream, conn net.Conn)
	Window     int
	MemCap     int
	MaxStreams int
	Idle       time.Duration
	OpenWait   time.Duration
	DialWait   time.Duration
	Log        *slog.Logger
	Now        func() time.Time
}

// Stats is a snapshot of a session.
type Stats struct {
	Streams  int `json:"streams"`
	Buffered int `json:"buffered"` // received bytes not read yet (incl. out of order)
	Granted  int `json:"granted"`  // receive window granted to the peer, in total
	Backlog  int `json:"backlog"`  // bytes written by local apps, not sent yet
}

// Session is the stream table of one hub↔server session.
type Session struct {
	cfg    Config
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	kick   chan struct{}

	mu      sync.Mutex
	closed  bool
	streams map[uint32]*Stream
	tombs   map[uint32]time.Time
	nextID  uint32
	granted int
	sendq   []*Stream // streams with data or a FIN to send, round robin
}

// New starts a session; Close stops it.
func New(cfg Config) *Session {
	if cfg.Window <= 0 {
		cfg.Window = DefaultWindow
	}
	if cfg.MemCap <= 0 {
		cfg.MemCap = DefaultMemCap
	}
	if cfg.MaxStreams <= 0 {
		cfg.MaxStreams = DefaultMaxStreams
	}
	if cfg.Idle <= 0 {
		cfg.Idle = DefaultIdle
	}
	if cfg.OpenWait <= 0 {
		cfg.OpenWait = DefaultOpenWait
	}
	if cfg.DialWait <= 0 {
		cfg.DialWait = DefaultDialWait
	}
	if cfg.Log == nil {
		cfg.Log = slog.Default()
	}
	if cfg.Now == nil {
		cfg.Now = time.Now
	}
	s := &Session{cfg: cfg, kick: make(chan struct{}, 1), streams: make(map[uint32]*Stream),
		tombs: make(map[uint32]time.Time)}
	// Ids start at a random point: the server keeps a hub's session across a
	// hub restart, and new streams must not hit its tombstones.
	var b [4]byte
	_, _ = rand.Read(b[:]) // crypto/rand.Read never fails
	s.nextID = binary.BigEndian.Uint32(b[:])
	s.ctx, s.cancel = context.WithCancel(context.Background())
	s.wg.Add(2)
	go s.pump()
	go s.tickLoop()
	return s
}

// Close resets every stream and stops the session.
func (s *Session) Close() {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return
	}
	s.closed = true
	for _, st := range s.streams {
		s.resetLocked(st, ResetShutdown, false, true)
	}
	s.mu.Unlock()
	s.cancel()
	s.wg.Wait()
}

// Stats returns the session counters.
func (s *Session) Stats() Stats {
	s.mu.Lock()
	defer s.mu.Unlock()
	st := Stats{Streams: len(s.streams), Granted: s.granted}
	for _, x := range s.streams {
		st.Buffered += len(x.rbuf) + x.oooBytes
		st.Backlog += len(x.sbuf)
	}
	return st
}

// Open opens a stream to dst through the peer (hub side) and waits for the
// peer's answer.
func (s *Session) Open(ctx context.Context, dst netip.AddrPort) (*Stream, error) {
	s.mu.Lock()
	if s.closed {
		s.mu.Unlock()
		return nil, ErrClosed
	}
	if len(s.streams) >= s.cfg.MaxStreams {
		s.mu.Unlock()
		return nil, ErrLimit
	}
	id := s.allocIDLocked()
	st := newStream(s, id, dst, true)
	s.streams[id] = st
	s.grantLocked(st)
	st.announced = true
	err := s.sendLocked(&wire.StreamOpen{StreamID: id, Dst: dst}, &wire.StreamWindow{StreamID: id, MaxOffset: st.rcvMax})
	s.mu.Unlock()
	if err != nil {
		st.Abort()
		return nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, s.cfg.OpenWait)
	defer cancel()
	select {
	case code := <-st.openc:
		if code != wire.OpenOK {
			return nil, &OpenError{Code: code}
		}
		return st, nil
	case <-st.done:
		return nil, st.err()
	case <-ctx.Done():
		st.Abort()
		return nil, &OpenError{Code: wire.OpenTimeout}
	}
}

// allocIDLocked returns the next free stream id (never 0).
func (s *Session) allocIDLocked() uint32 {
	for {
		s.nextID++
		if s.nextID == 0 {
			continue
		}
		if _, used := s.streams[s.nextID]; used {
			continue
		}
		if _, dead := s.tombs[s.nextID]; dead {
			continue
		}
		return s.nextID
	}
}

// Handle processes one stream frame from the peer. It reports false for
// frames that are not stream frames.
func (s *Session) Handle(f wire.Frame) bool {
	switch f := f.(type) {
	case *wire.StreamOpen:
		s.onOpen(f)
	case *wire.StreamOpenResult:
		s.withStream(f.StreamID, func(st *Stream) { s.onOpenResult(st, f.Code) })
	case *wire.StreamData:
		s.withStream(f.StreamID, func(st *Stream) { s.onData(st, f.Offset, f.Data) })
	case *wire.StreamWindow:
		s.withStream(f.StreamID, func(st *Stream) { s.onWindow(st, f.MaxOffset) })
	case *wire.StreamClose:
		s.withStream(f.StreamID, func(st *Stream) { s.onClose(st, f.FinalOffset) })
	case *wire.StreamReset:
		s.mu.Lock()
		if st := s.streams[f.StreamID]; st != nil {
			s.resetLocked(st, f.Code, true, false)
		} else if !s.closed {
			// Reliable payloads arrive unordered: an OPEN that comes after
			// its RESET must not create the stream.
			s.tombs[f.StreamID] = s.cfg.Now()
		}
		s.mu.Unlock()
	default:
		return false
	}
	return true
}

// withStream runs fn on a live stream with the lock held. A frame for an
// unknown id that is not a recent tombstone is answered with a reset.
func (s *Session) withStream(id uint32, fn func(*Stream)) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if st := s.streams[id]; st != nil {
		fn(st)
		return
	}
	if _, dead := s.tombs[id]; dead || s.closed {
		return
	}
	s.tombs[id] = s.cfg.Now()
	_ = s.sendLocked(&wire.StreamReset{StreamID: id, Code: ResetUnknown})
}

func (s *Session) onOpen(f *wire.StreamOpen) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, live := s.streams[f.StreamID]; live || s.closed {
		return // repeated OPEN
	}
	if _, dead := s.tombs[f.StreamID]; dead {
		return
	}
	if s.cfg.Dial == nil || s.cfg.Serve == nil || len(s.streams) >= s.cfg.MaxStreams {
		s.tombs[f.StreamID] = s.cfg.Now()
		_ = s.sendLocked(&wire.StreamOpenResult{StreamID: f.StreamID, Code: wire.OpenRefused})
		return
	}
	st := newStream(s, f.StreamID, f.Dst, false)
	s.streams[f.StreamID] = st
	s.wg.Add(1)
	go s.dial(st)
}

// dial connects a peer-opened stream to its origin and answers the OPEN.
func (s *Session) dial(st *Stream) {
	defer s.wg.Done()
	ctx, cancel := context.WithTimeout(s.ctx, s.cfg.DialWait)
	conn, err := s.cfg.Dial(ctx, st.dst)
	cancel()
	s.mu.Lock()
	if err != nil || st.finished {
		code := openCode(err)
		if err == nil {
			_ = conn.Close()
		}
		if !st.finished {
			_ = s.sendLocked(&wire.StreamOpenResult{StreamID: st.id, Code: code})
			s.removeLocked(st, &OpenError{Code: code})
		}
		s.mu.Unlock()
		s.cfg.Log.Debug("pep open failed", "stream", st.id, "dst", st.dst, "err", err)
		return
	}
	s.grantLocked(st)
	st.opened, st.announced = true, true
	st.touch()
	_ = s.sendLocked(&wire.StreamOpenResult{StreamID: st.id, Code: wire.OpenOK},
		&wire.StreamWindow{StreamID: st.id, MaxOffset: st.rcvMax})
	s.mu.Unlock()
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.cfg.Serve(st, conn)
	}()
}

// openCode maps a dial error to a STREAM_OPEN_RESULT code.
func openCode(err error) uint8 {
	var ne net.Error
	switch {
	case err == nil:
		return wire.OpenRefused // session closing
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return wire.OpenTimeout
	case isRefused(err):
		return wire.OpenRefused
	}
	return wire.OpenUnreachable
}

func (s *Session) onOpenResult(st *Stream, code uint8) {
	if !st.local || st.opened {
		return
	}
	if code != wire.OpenOK {
		s.removeLocked(st, &OpenError{Code: code})
		select {
		case st.openc <- code:
		default:
		}
		return
	}
	st.opened = true
	st.touch()
	select {
	case st.openc <- code:
	default:
	}
}

// sendLocked encodes frames into one reliable payload. Control frames are
// always queued, whatever the backlog.
func (s *Session) sendLocked(frames ...wire.Frame) error {
	var b []byte
	for _, f := range frames {
		var err error
		if b, err = wire.AppendFrame(b, f); err != nil {
			return fmt.Errorf("pep: encode %T: %w", f, err)
		}
	}
	if _, err := s.cfg.Link.SendReliable(b); err != nil {
		return fmt.Errorf("pep: send: %w", err)
	}
	return nil
}

// grantLocked extends st's receive window to readOff + Window as far as the
// session memory cap allows, and tells the peer when it changed.
func (s *Session) grantLocked(st *Stream) {
	want := st.readOff + uint64(s.cfg.Window)
	if st.finished || want <= st.rcvMax {
		return
	}
	// Every stream keeps at least MinWindow of credit, cap or not: idle
	// streams hold their full windows, and a new one must still move.
	avail := max(s.cfg.MemCap-s.granted, min(MinWindow, s.cfg.Window/4)-int(st.rcvMax-st.readOff))
	inc := min(int(want-st.rcvMax), avail)
	if inc <= 0 {
		return
	}
	st.rcvMax += uint64(inc)
	s.granted += inc
	if st.announced { // the first window goes out with OPEN / OPEN_RESULT
		_ = s.sendLocked(&wire.StreamWindow{StreamID: st.id, MaxOffset: st.rcvMax})
	}
}

// removeLocked drops st from the table, returns its grant and ends it.
func (s *Session) removeLocked(st *Stream, err error) {
	if st.finished {
		return
	}
	st.finished = true
	st.endErr = err
	if s.streams[st.id] == st {
		delete(s.streams, st.id)
		s.tombs[st.id] = s.cfg.Now()
	}
	s.granted -= int(st.rcvMax - st.readOff)
	st.rcvMax = st.readOff
	st.sbuf = nil
	st.rcond.Broadcast()
	st.wcond.Broadcast()
	close(st.done)
}

// resetLocked ends st with a reset; send tells the peer.
func (s *Session) resetLocked(st *Stream, code uint8, remote, send bool) {
	if st.finished {
		return
	}
	if send {
		_ = s.sendLocked(&wire.StreamReset{StreamID: st.id, Code: code})
	}
	s.removeLocked(st, &ResetError{Code: code, Remote: remote})
}

func (s *Session) wakePump() {
	select {
	case s.kick <- struct{}{}:
	default:
	}
}

// queueSendLocked puts st on the round-robin send list.
func (s *Session) queueSendLocked(st *Stream) {
	if !st.inSendq && !st.finished {
		st.inSendq = true
		s.sendq = append(s.sendq, st)
		s.wakePump()
	}
}

// pump moves stream data into the link, one chunk per stream in turn,
// while the link's backlog has room.
func (s *Session) pump() {
	defer s.wg.Done()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-s.kick:
		}
		for {
			s.mu.Lock()
			b := s.nextChunkLocked()
			s.mu.Unlock()
			if b == nil {
				break
			}
			full, err := s.cfg.Link.SendReliable(b)
			if err != nil {
				s.cfg.Log.Debug("pep send", "err", err)
			}
			if full {
				select {
				case <-s.cfg.Link.Room():
				case <-s.ctx.Done():
					return
				}
			}
		}
	}
}

// nextChunkLocked returns the next payload to send, or nil.
func (s *Session) nextChunkLocked() []byte {
	maxData := max(s.cfg.Link.TunnelMTU()-streamDataHeader, 64)
	for len(s.sendq) > 0 {
		st := s.sendq[0]
		s.sendq[0] = nil
		s.sendq = s.sendq[1:]
		st.inSendq = false
		b := st.chunkLocked(maxData)
		if b == nil {
			continue
		}
		if st.hasSendLocked() {
			st.inSendq = true
			s.sendq = append(s.sendq, st)
		}
		return b
	}
	s.sendq = nil
	return nil
}

// tickLoop expires idle streams and tombstones and re-grants windows the
// memory cap held back.
func (s *Session) tickLoop() {
	defer s.wg.Done()
	t := time.NewTicker(tickEvery)
	defer t.Stop()
	for {
		select {
		case <-s.ctx.Done():
			return
		case <-t.C:
			s.tick(s.cfg.Now())
		}
	}
}

func (s *Session) tick(now time.Time) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, at := range s.tombs {
		if now.Sub(at) >= TombstoneFor {
			delete(s.tombs, id)
		}
	}
	for _, st := range s.streams {
		if now.Sub(st.lastActive) >= s.cfg.Idle {
			s.resetLocked(st, ResetIdle, false, true)
			continue
		}
		if st.rcvMax-st.readOff < uint64(s.cfg.Window)/2 {
			s.grantLocked(st)
		}
	}
}
