package transport

import (
	"context"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

// MaxStreamConns bounds Dialer.StreamConns and SetConns.
const MaxStreamConns = 8

// Replacement backoff of a connection set: a member that died within
// memberStable of its dial (or a failed dial) delays the next dial, from
// memberRetry doubling to maxMemberBackoff.
const (
	memberStable     = 30 * time.Second
	memberRetry      = time.Second
	maxMemberBackoff = 30 * time.Second
)

// Resizer is a connection whose number of TCP connections changes live.
type Resizer interface {
	// SetConns sets the wanted number of connections (clamped to
	// 1..MaxStreamConns): extra ones are closed, missing ones dialed.
	SetConns(n int)
}

// TCPCounters are a connection set's TCP counters since it was dialed, in
// segments: tcpi_total_retrans / tcpi_data_segs_out on Linux/Android,
// BytesRetrans / BytesOut divided by the MSS on Windows. Both stay 0 where
// the OS does not report them.
type TCPCounters struct {
	Conns   int    // live connections
	Retrans uint64 // retransmitted
	Sent    uint64 // sent (data)
}

// Counter is a connection that reports TCPCounters.
type Counter interface {
	TCPCounters() TCPCounters
}

// member is one TCP connection of a set.
type member struct {
	*tcpConn
	born time.Time
}

// connSet spreads one path over several TCP connections: packets go out
// round-robin (a connection whose send queue is full is skipped); the
// server answers on the connection that brought the newest packet, so the
// download moves between them too. No wire change. A loss on one connection
// then stalls only the packets queued on it. A connection that fails is
// dropped and redialed in the background while the others carry the path;
// the set fails (Recv returns the error) only when no connection is left.
type connSet struct {
	dial func(ctx context.Context) (*tcpConn, error)
	ctx  context.Context // ends with the set
	stop context.CancelFunc

	live atomic.Pointer[[]*member] // immutable snapshot for Send
	next atomic.Uint32
	in   chan []byte
	done chan struct{}
	once sync.Once
	err  error // set before done closes

	mu      sync.Mutex
	members []*member
	want    int
	filling bool
	backoff time.Duration // before the next replacement dial
	closedR uint64        // counters of members already closed
	closedS uint64
}

// dialSet dials the first connection (an error ends the dial) and starts
// the other n-1 in the background.
func dialSet(ctx context.Context, n int, dial func(ctx context.Context) (*tcpConn, error)) (*connSet, error) {
	first, err := dial(ctx)
	if err != nil {
		return nil, err
	}
	sctx, stop := context.WithCancel(context.Background())
	s := &connSet{dial: dial, ctx: sctx, stop: stop, in: make(chan []byte, 256), done: make(chan struct{}), want: n}
	s.mu.Lock()
	s.addLocked(first)
	s.fillLocked()
	s.mu.Unlock()
	return s, nil
}

// addLocked makes c a member and starts its reader (c is closed when the
// set already ended).
func (s *connSet) addLocked(c *tcpConn) {
	if s.isDone() {
		_ = c.Close()
		return
	}
	m := &member{tcpConn: c, born: time.Now()}
	s.members = append(s.members, m)
	s.publishLocked()
	go s.read(m)
}

func (s *connSet) publishLocked() {
	snap := append([]*member(nil), s.members...)
	s.live.Store(&snap)
}

func (s *connSet) read(m *member) {
	for {
		buf := make([]byte, MaxPacket)
		n, err := m.Recv(buf)
		if err != nil {
			s.drop(m, err)
			return
		}
		select {
		case s.in <- buf[:n]:
		case <-s.done:
			return
		}
	}
}

// removeLocked takes m out of the set (false when it already was) and
// keeps its counters.
func (s *connSet) removeLocked(m *member) bool {
	for i, x := range s.members {
		if x != m {
			continue
		}
		s.members = append(s.members[:i], s.members[i+1:]...)
		s.publishLocked()
		r, snt, _ := tcpCounters(m.c)
		s.closedR += r
		s.closedS += snt
		_ = m.Close()
		return true
	}
	return false
}

// drop handles a failed member: the set ends when none is left and no dial
// is running, otherwise a replacement is dialed.
func (s *connSet) drop(m *member, err error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.removeLocked(m) {
		return
	}
	if time.Since(m.born) < memberStable {
		s.backoff = min(max(2*s.backoff, memberRetry), maxMemberBackoff)
	} else {
		s.backoff = 0
	}
	if len(s.members) == 0 && !s.filling {
		s.failLocked(err)
		return
	}
	s.fillLocked()
}

// fillLocked starts the filler when connections are missing.
func (s *connSet) fillLocked() {
	if s.filling || len(s.members) >= s.want || s.isDone() {
		return
	}
	s.filling = true
	go s.fill()
}

// fill dials until the set has want connections; a failed dial with no
// connection left ends the set.
func (s *connSet) fill() {
	for {
		s.mu.Lock()
		wait := s.backoff
		if len(s.members) >= s.want || s.isDone() {
			s.filling = false
			s.mu.Unlock()
			return
		}
		s.mu.Unlock()
		if wait > 0 {
			select {
			case <-s.ctx.Done():
			case <-time.After(wait):
			}
		}
		c, err := s.dial(s.ctx)
		s.mu.Lock()
		switch {
		case err == nil && len(s.members) < s.want:
			s.addLocked(c)
		case err == nil:
			_ = c.Close()
		case len(s.members) == 0:
			s.filling = false
			s.failLocked(err)
			s.mu.Unlock()
			return
		default:
			s.backoff = min(max(2*s.backoff, memberRetry), maxMemberBackoff)
		}
		s.mu.Unlock()
	}
}

func (s *connSet) isDone() bool {
	select {
	case <-s.done:
		return true
	default:
		return false
	}
}

func (s *connSet) failLocked(err error) {
	s.once.Do(func() {
		s.err = err
		close(s.done)
		s.stop()
		for _, m := range s.members {
			_ = m.Close()
		}
	})
}

// SetConns implements Resizer.
func (s *connSet) SetConns(n int) {
	n = min(max(n, 1), MaxStreamConns)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.want = n
	for len(s.members) > n {
		s.removeLocked(s.members[len(s.members)-1])
	}
	s.fillLocked()
}

// TCPCounters implements Counter.
func (s *connSet) TCPCounters() TCPCounters {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := TCPCounters{Conns: len(s.members), Retrans: s.closedR, Sent: s.closedS}
	for _, m := range s.members {
		r, snt, _ := tcpCounters(m.c)
		out.Retrans += r
		out.Sent += snt
	}
	return out
}

// Send writes pkt on the next connection whose queue has room.
func (s *connSet) Send(pkt []byte) error {
	live := *s.live.Load()
	if len(live) == 0 {
		select {
		case <-s.done:
			return s.err
		default:
			return ErrQueueFull // a replacement is being dialed
		}
	}
	start := int(s.next.Add(1))
	err := error(ErrQueueFull)
	for i := range live {
		if err = live[(start+i)%len(live)].Send(pkt); err == nil {
			return nil
		}
	}
	return err
}

// Recv returns the next packet from any connection.
func (s *connSet) Recv(buf []byte) (int, error) {
	select {
	case p := <-s.in:
		return copyPacket(buf, p)
	case <-s.done:
		select { // deliver what already arrived first
		case p := <-s.in:
			return copyPacket(buf, p)
		default:
		}
		return 0, s.err
	}
}

func copyPacket(buf, p []byte) (int, error) {
	if len(p) > len(buf) {
		return 0, fmt.Errorf("transport: packet of %d bytes, buffer %d", len(p), len(buf))
	}
	return copy(buf, p), nil
}

func (s *connSet) Close() error {
	s.mu.Lock()
	s.failLocked(net.ErrClosed)
	s.mu.Unlock()
	return nil
}

func (s *connSet) Transport() wire.Transport { return wire.TransportTCP }

var errStreamConns = errors.New("transport: StreamConns out of range")
