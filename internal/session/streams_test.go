package session

import (
	"errors"
	"net/netip"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

// stuckStream is a stream sender whose queue is full.
type stuckStream struct{ streamRecorder }

func (*stuckStream) WriteToUDPAddrPort([]byte, netip.AddrPort) (int, error) {
	return 0, errors.New("transport: send queue full")
}

func (r *recorder) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.pkts)
}

// TestStreamSendFallback: the server sends on the connection of the newest
// packet and falls back to the path's other live connections only when that
// one fails; a connection silent for StreamFresh or from the path's old IP
// is not used.
func TestStreamSendFallback(t *testing.T) {
	a1 := netip.MustParseAddrPort("192.0.2.9:40000")
	a2 := netip.MustParseAddrPort("192.0.2.9:40001")
	other := netip.MustParseAddrPort("192.0.2.10:40002")
	tests := []struct {
		name    string
		setup   func(p *pair, c2 Sender)
		conn2   Sender
		want    [2]int // packets on c1, c2 out of 4
		wantErr bool
	}{
		{"newest connection carries", func(*pair, Sender) {}, &streamRecorder{}, [2]int{0, 4}, false},
		{"newest stalled: the other one", func(*pair, Sender) {}, &stuckStream{}, [2]int{4, 0}, false},
		{"newest stalled, the other silent too long", func(p *pair, _ Sender) {
			p.clk.advance(StreamFresh)
		}, &stuckStream{}, [2]int{0, 0}, true},
		{"newest stalled after a move to another IP", func(p *pair, c2 Sender) {
			p.srv.mu.Lock()
			p.srv.setPathLocked(2, other, c2)
			p.srv.mu.Unlock()
		}, &stuckStream{}, [2]int{0, 0}, true},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newPair(t)
			keysFromPair(t, p)
			c1 := &streamRecorder{}
			p.srv.mu.Lock()
			p.srv.setPathLocked(2, a1, c1)
			p.srv.setPathLocked(2, a2, tc.conn2)
			p.srv.mu.Unlock()
			tc.setup(p, tc.conn2)
			for range 4 {
				if err := p.srv.SendOn(2, &wire.Ping{}); (err != nil) != tc.wantErr {
					t.Fatalf("send: %v, want error %v", err, tc.wantErr)
				}
			}
			n2 := 0
			if r, ok := tc.conn2.(*streamRecorder); ok {
				n2 = r.count()
			}
			if got := [2]int{c1.count(), n2}; got != tc.want {
				t.Fatalf("packets per connection %v, want %v", got, tc.want)
			}
		})
	}
}

func TestStreamListCapped(t *testing.T) {
	s := New(1, Options{})
	base := time.Unix(0, 0)
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range maxStreams + 3 {
		s.setPathLocked(1, netip.AddrPortFrom(netip.MustParseAddr("192.0.2.9"), uint16(40000+i)), &streamRecorder{})
		s.paths[1].streams[len(s.paths[1].streams)-1].seen = base.Add(time.Duration(i) * time.Second)
	}
	if n := len(s.paths[1].streams); n != maxStreams {
		t.Fatalf("%d connections kept, want %d", n, maxStreams)
	}
}

// queuedStream is a stream sender that reports a send queue of bl packets.
type queuedStream struct {
	streamRecorder
	bl int
}

func (q *queuedStream) Backlog() int { return q.bl }

// TestStreamSendBacklog: the newest connection keeps the path's packets
// until its queue reaches streamSwitchBacklog and another live connection
// has at most half as many waiting (a TCP loss stalls the newest one).
func TestStreamSendBacklog(t *testing.T) {
	a1 := netip.MustParseAddrPort("192.0.2.9:40000")
	a2 := netip.MustParseAddrPort("192.0.2.9:40001")
	tests := []struct {
		name       string
		bl1, blNew int
		want       [2]int // packets on the older, the newest of 4
	}{
		{"both short", 0, 0, [2]int{0, 4}},
		{"newest below the switch mark", 0, streamSwitchBacklog - 1, [2]int{0, 4}},
		{"newest stalled, the other short", 3, streamSwitchBacklog, [2]int{4, 0}},
		{"newest stalled, the other not much shorter", streamSwitchBacklog/2 + 1, streamSwitchBacklog, [2]int{0, 4}},
		{"exactly half", streamSwitchBacklog / 2, streamSwitchBacklog, [2]int{4, 0}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			p := newPair(t)
			keysFromPair(t, p)
			c1, c2 := &queuedStream{bl: tc.bl1}, &queuedStream{bl: tc.blNew}
			p.srv.mu.Lock()
			p.srv.setPathLocked(2, a1, c1)
			p.srv.setPathLocked(2, a2, c2) // newest
			p.srv.mu.Unlock()
			for range 4 {
				if err := p.srv.SendOn(2, &wire.Ping{}); err != nil {
					t.Fatal(err)
				}
			}
			if got := [2]int{c1.count(), c2.count()}; got != tc.want {
				t.Fatalf("packets per connection %v, want %v", got, tc.want)
			}
		})
	}
}
