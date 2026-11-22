package transport

import (
	"context"
	"net/netip"
	"runtime"
	"sync"
	"testing"
	"time"
)

// waitConns waits until c has n live connections.
func waitConns(t *testing.T, c Conn, n int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for c.(Counter).TCPCounters().Conns != n {
		if time.Now().After(deadline) {
			t.Fatalf("%d live connections, want %d", c.(Counter).TCPCounters().Conns, n)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// echoAll sends n packets and checks that all come back.
func echoAll(t *testing.T, c Conn, n int) {
	t.Helper()
	for i := range n {
		if err := c.Send([]byte{byte(i), 1, 2}); err != nil {
			t.Fatal(err)
		}
	}
	buf := make([]byte, MaxPacket)
	seen := map[byte]bool{}
	for range n {
		k, err := c.Recv(buf)
		if err != nil || k != 3 {
			t.Fatalf("recv %d %v", k, err)
		}
		seen[buf[0]-1] = true
	}
	if len(seen) != n {
		t.Fatalf("%d distinct packets back, want %d", len(seen), n)
	}
}

// remoteCounter wraps the echo handler and counts packets per connection.
type remoteCounter struct {
	mu    sync.Mutex
	seen  map[netip.AddrPort]int
	conns map[netip.AddrPort]*ServerConn
}

func countingServer(t *testing.T) (*remoteCounter, *Server, func() Conn) {
	t.Helper()
	rc := &remoteCounter{seen: map[netip.AddrPort]int{}, conns: map[netip.AddrPort]*ServerConn{}}
	tcp, _, sv := echoServer(t)
	echo := sv.Handler
	sv.Handler = func(pkt []byte, from netip.AddrPort, s *ServerConn) {
		rc.mu.Lock()
		rc.seen[from]++
		rc.conns[from] = s
		rc.mu.Unlock()
		echo(pkt, from, s)
	}
	dial := func() Conn {
		d := Dialer{Lookup: localLookup, Timeout: 5 * time.Second, StreamConns: 1}
		c, err := d.Dial(context.Background(), tcp)
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = c.Close() })
		return c
	}
	return rc, sv, dial
}

func (rc *remoteCounter) reset() {
	rc.mu.Lock()
	clear(rc.seen)
	clear(rc.conns)
	rc.mu.Unlock()
}

func (rc *remoteCounter) used() int {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return len(rc.seen)
}

func TestStreamConnsRoundRobin(t *testing.T) {
	tcp, _, _ := echoServer(t)
	tests := []struct {
		conns   int
		wantErr bool
	}{{0, false}, {1, false}, {2, false}, {4, false}, {MaxStreamConns + 1, true}}
	for _, tc := range tests {
		d := Dialer{Lookup: localLookup, Timeout: 5 * time.Second, StreamConns: tc.conns}
		c, err := d.Dial(context.Background(), tcp)
		if (err != nil) != tc.wantErr {
			t.Fatalf("%d conns: %v", tc.conns, err)
		}
		if tc.wantErr {
			continue
		}
		waitConns(t, c, max(tc.conns, 1))
		echoAll(t, c, 40)
		if err := c.Close(); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Recv(make([]byte, MaxPacket)); err == nil {
			t.Fatal("recv after close")
		}
		if err := c.Send([]byte{1}); err == nil {
			t.Fatal("send after close")
		}
	}
}

func TestStreamConnsSpread(t *testing.T) {
	rc, _, dial := countingServer(t)
	c := dial()
	for _, n := range []int{2, 4, 3, 1} {
		c.(Resizer).SetConns(n)
		waitConns(t, c, n)
		rc.reset()
		echoAll(t, c, 40)
		if got := rc.used(); got != n {
			t.Fatalf("SetConns(%d): %d connections carried packets", n, got)
		}
	}
	c.(Resizer).SetConns(MaxStreamConns + 5) // clamped
	waitConns(t, c, MaxStreamConns)
	c.(Resizer).SetConns(0)
	waitConns(t, c, 1)
}

// TestStreamConnsMemberDies: a connection the server closes is dropped, the
// path keeps working on the others at once and the set redials it.
func TestStreamConnsMemberDies(t *testing.T) {
	rc, _, dial := countingServer(t)
	c := dial()
	c.(Resizer).SetConns(3)
	waitConns(t, c, 3)
	rc.reset()
	echoAll(t, c, 30)
	rc.mu.Lock()
	var victim *ServerConn
	for _, s := range rc.conns {
		victim = s
		break
	}
	rc.mu.Unlock()
	_ = victim.Close()
	waitConns(t, c, 2)
	echoAll(t, c, 30)  // no stall while one is missing
	waitConns(t, c, 3) // replaced after the backoff (≤ 1 s)
	rc.reset()
	echoAll(t, c, 30)
	if got := rc.used(); got != 3 {
		t.Fatalf("%d connections after the redial, want 3", got)
	}
}

// TestStreamConnsServerGone: the set ends when no connection is left and
// the server cannot be reached again.
func TestStreamConnsServerGone(t *testing.T) {
	_, sv, dial := countingServer(t)
	c := dial()
	c.(Resizer).SetConns(3)
	waitConns(t, c, 3)
	sv.Close()
	done := make(chan struct{})
	go func() {
		buf := make([]byte, MaxPacket)
		for {
			if _, err := c.Recv(buf); err != nil {
				close(done)
				return
			}
		}
	}()
	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("no error after the server closed")
	}
}

func TestStreamConnsCounters(t *testing.T) {
	_, _, dial := countingServer(t)
	c := dial()
	c.(Resizer).SetConns(2)
	waitConns(t, c, 2)
	echoAll(t, c, 20)
	got := c.(Counter).TCPCounters()
	if got.Conns != 2 {
		t.Fatalf("conns %d", got.Conns)
	}
	if runtime.GOOS == "linux" || runtime.GOOS == "windows" {
		if got.Sent == 0 {
			t.Fatalf("no sent counter on %s: %+v", runtime.GOOS, got)
		}
	}
	c.(Resizer).SetConns(1) // a closed member's counters are kept
	waitConns(t, c, 1)
	if after := c.(Counter).TCPCounters(); after.Sent < got.Sent {
		t.Fatalf("counters went back: %+v → %+v", got, after)
	}
}
