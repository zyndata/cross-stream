package pep

import (
	"errors"
	"io"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

// Stream is one proxied TCP connection carried as a byte stream over the
// bond: Read gets the peer's bytes in order, Write queues bytes for the
// peer. All state is guarded by the session lock.
type Stream struct {
	s     *Session
	id    uint32
	dst   netip.AddrPort
	local bool // opened by this side (hub)

	opened    bool // OPEN answered OK
	announced bool // OPEN / OPEN_RESULT sent: later grants send WINDOW
	finished  bool // removed from the table
	endErr    error
	done      chan struct{}
	openc     chan uint8

	lastActive time.Time

	// receive side
	rcond    *sync.Cond
	rbuf     []byte            // in order, not read yet
	ooo      map[uint64][]byte // out of order, by offset
	oooBytes int
	rcvNext  uint64 // next offset expected in order
	readOff  uint64 // bytes handed to Read
	rcvMax   uint64 // window granted to the peer
	rfin     uint64
	hasRfin  bool

	// send side
	wcond       *sync.Cond
	sbuf        []byte
	sndNext     uint64 // next offset to send
	peerMax     uint64 // the peer's window
	finPending  bool
	finSent     bool
	closedLocal bool
	inSendq     bool
}

func newStream(s *Session, id uint32, dst netip.AddrPort, local bool) *Stream {
	st := &Stream{s: s, id: id, dst: dst, local: local, done: make(chan struct{}), openc: make(chan uint8, 1),
		ooo: make(map[uint64][]byte)}
	st.rcond = sync.NewCond(&s.mu)
	st.wcond = sync.NewCond(&s.mu)
	st.touch()
	return st
}

// ID returns the stream id.
func (st *Stream) ID() uint32 { return st.id }

// Dst returns the stream's destination.
func (st *Stream) Dst() netip.AddrPort { return st.dst }

// Done is closed when the stream has ended.
func (st *Stream) Done() <-chan struct{} { return st.done }

func (st *Stream) touch() { st.lastActive = st.s.cfg.Now() }

// err is why the stream ended. Caller holds the lock or the stream is done.
func (st *Stream) err() error {
	if st.endErr != nil {
		return st.endErr
	}
	return net.ErrClosed
}

// atEOF reports whether every byte up to the peer's FIN was read.
func (st *Stream) atEOF() bool { return st.hasRfin && st.readOff == st.rfin }

// Read reads the peer's bytes in order; io.EOF after the peer's FIN.
func (st *Stream) Read(p []byte) (int, error) {
	s := st.s
	s.mu.Lock()
	defer s.mu.Unlock()
	for len(st.rbuf) == 0 {
		switch {
		case st.finished:
			return 0, st.err()
		case st.atEOF():
			s.maybeRetireLocked(st)
			return 0, io.EOF
		}
		st.rcond.Wait()
	}
	if st.finished {
		return 0, st.err()
	}
	n := copy(p, st.rbuf)
	st.rbuf = st.rbuf[n:]
	if len(st.rbuf) == 0 {
		st.rbuf = nil
	}
	st.readOff += uint64(n)
	s.granted -= n
	if st.rcvMax-st.readOff < uint64(s.cfg.Window)/2 {
		s.grantLocked(st)
	}
	return n, nil
}

// Write queues p for the peer; it blocks while SendBuffer bytes wait.
func (st *Stream) Write(p []byte) (int, error) {
	s := st.s
	s.mu.Lock()
	defer s.mu.Unlock()
	total := 0
	for len(p) > 0 {
		for !st.finished && !st.finPending && len(st.sbuf) >= SendBuffer {
			st.wcond.Wait()
		}
		switch {
		case st.finished:
			return total, st.err()
		case st.finPending:
			return total, net.ErrClosed
		}
		n := min(len(p), SendBuffer-len(st.sbuf))
		st.sbuf = append(st.sbuf, p[:n]...)
		p = p[n:]
		total += n
		st.touch()
		s.queueSendLocked(st)
	}
	return total, nil
}

// CloseWrite sends a FIN after the queued bytes.
func (st *Stream) CloseWrite() error {
	s := st.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.finished {
		return st.err()
	}
	if !st.finPending {
		st.finPending = true
		s.queueSendLocked(st)
	}
	st.wcond.Broadcast()
	return nil
}

// Close ends the stream: gracefully (FIN after the queued bytes) when the
// peer's bytes were read to EOF, otherwise with a reset.
func (st *Stream) Close() error {
	s := st.s
	s.mu.Lock()
	defer s.mu.Unlock()
	if st.finished {
		return nil
	}
	if !st.atEOF() {
		s.resetLocked(st, ResetAbort, false, true)
		return nil
	}
	st.closedLocal = true
	if !st.finPending {
		st.finPending = true
		s.queueSendLocked(st)
	}
	st.wcond.Broadcast()
	s.maybeRetireLocked(st)
	return nil
}

// Abort resets the stream at once.
func (st *Stream) Abort() {
	s := st.s
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetLocked(st, ResetAbort, false, true)
}

// maybeRetireLocked removes a stream whose both directions ended cleanly.
func (s *Session) maybeRetireLocked(st *Stream) {
	if st.closedLocal && st.finSent && st.atEOF() {
		s.removeLocked(st, io.EOF)
	}
}

// hasSendLocked reports whether the pump can send something for st now.
func (st *Stream) hasSendLocked() bool {
	if len(st.sbuf) > 0 {
		return st.peerMax > st.sndNext
	}
	return st.finPending && !st.finSent
}

// chunkLocked encodes the next STREAM_DATA (or the STREAM_CLOSE) of st.
func (st *Stream) chunkLocked(maxData int) []byte {
	if st.finished {
		return nil
	}
	if len(st.sbuf) > 0 {
		if st.peerMax <= st.sndNext {
			return nil // window closed: a WINDOW frame queues st again
		}
		n := int(min(uint64(min(len(st.sbuf), maxData)), st.peerMax-st.sndNext))
		b, err := wire.AppendFrame(nil, &wire.StreamData{StreamID: st.id, Offset: st.sndNext, Data: st.sbuf[:n]})
		if err != nil {
			return nil
		}
		st.sbuf = st.sbuf[n:]
		if len(st.sbuf) == 0 {
			st.sbuf = nil
		}
		st.sndNext += uint64(n)
		st.touch()
		st.wcond.Broadcast()
		return b
	}
	if st.finPending && !st.finSent {
		b, err := wire.AppendFrame(nil, &wire.StreamClose{StreamID: st.id, FinalOffset: st.sndNext})
		if err != nil {
			return nil
		}
		st.finSent = true
		st.s.maybeRetireLocked(st)
		return b
	}
	return nil
}

// onData stores the peer's bytes at off (reassembly by offset).
func (s *Session) onData(st *Stream, off uint64, data []byte) {
	end := off + uint64(len(data))
	if end < off || end > st.rcvMax || (st.hasRfin && end > st.rfin) {
		s.resetLocked(st, ResetFlow, false, true)
		return
	}
	st.touch()
	switch {
	case end <= st.rcvNext || len(data) == 0:
		return // repeated
	case off > st.rcvNext:
		if _, dup := st.ooo[off]; !dup {
			st.ooo[off] = append([]byte(nil), data...)
			st.oooBytes += len(data)
		}
		return
	}
	st.rbuf = append(st.rbuf, data[st.rcvNext-off:]...)
	st.rcvNext = end
	for {
		c, ok := st.ooo[st.rcvNext]
		if !ok {
			break
		}
		delete(st.ooo, st.rcvNext)
		st.oooBytes -= len(c)
		st.rbuf = append(st.rbuf, c...)
		st.rcvNext += uint64(len(c))
	}
	st.rcond.Broadcast()
}

// onWindow raises the peer's window.
func (s *Session) onWindow(st *Stream, maxOff uint64) {
	if maxOff <= st.peerMax {
		return
	}
	st.peerMax = maxOff
	if st.hasSendLocked() {
		s.queueSendLocked(st)
	}
}

// onClose records the peer's FIN.
func (s *Session) onClose(st *Stream, final uint64) {
	if st.hasRfin {
		return
	}
	if final < st.rcvNext || final > st.rcvMax {
		s.resetLocked(st, ResetFlow, false, true)
		return
	}
	for off, c := range st.ooo { // data already buffered past the claimed end
		if off+uint64(len(c)) > final {
			s.resetLocked(st, ResetFlow, false, true)
			return
		}
	}
	st.rfin, st.hasRfin = final, true
	st.touch()
	st.rcond.Broadcast()
	s.maybeRetireLocked(st)
}

func isRefused(err error) bool { return errors.Is(err, syscall.ECONNREFUSED) }

// streamAddr is a stream endpoint for net.Conn.
type streamAddr struct{ ap netip.AddrPort }

func (a streamAddr) Network() string { return "pep" }
func (a streamAddr) String() string  { return a.ap.String() }

// LocalAddr implements net.Conn (no local address: the zero AddrPort).
func (st *Stream) LocalAddr() net.Addr { return streamAddr{} }

// RemoteAddr implements net.Conn: the stream's destination.
func (st *Stream) RemoteAddr() net.Addr { return streamAddr{st.dst} }

// SetDeadline implements net.Conn; streams have no deadlines (idle streams
// end after Config.Idle).
func (st *Stream) SetDeadline(time.Time) error { return errors.ErrUnsupported }

// SetReadDeadline implements net.Conn; see SetDeadline.
func (st *Stream) SetReadDeadline(time.Time) error { return errors.ErrUnsupported }

// SetWriteDeadline implements net.Conn; see SetDeadline.
func (st *Stream) SetWriteDeadline(time.Time) error { return errors.ErrUnsupported }

var _ net.Conn = (*Stream)(nil)
