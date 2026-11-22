package pep

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	mrand "math/rand/v2"
	"net"
	"net/netip"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

// fakeLink delivers reliable payloads to the peer session with reordering
// and duplicates (the bond never loses them).
type fakeLink struct {
	mu      sync.Mutex
	peer    *Session
	q       chan []byte
	room    chan struct{}
	stop    chan struct{}
	reorder int     // payloads held for random picks
	dup     float64 // probability of a repeated payload
	rng     *mrand.Rand
	sent    [][]byte // with record: every payload
	record  bool
}

const fakeFull = 64

func newFakeLink(reorder int, dup float64, seed uint64) *fakeLink {
	return &fakeLink{q: make(chan []byte, 1<<16), room: make(chan struct{}, 1), stop: make(chan struct{}),
		reorder: reorder, dup: dup, rng: mrand.New(mrand.NewPCG(seed, 7))}
}

func (l *fakeLink) SendReliable(b []byte) (bool, error) {
	c := append([]byte(nil), b...)
	l.mu.Lock()
	if l.record {
		l.sent = append(l.sent, c)
	}
	l.mu.Unlock()
	select {
	case l.q <- c:
	case <-l.stop:
	}
	return len(l.q) >= fakeFull, nil
}

func (l *fakeLink) Room() <-chan struct{} { return l.room }
func (l *fakeLink) TunnelMTU() int        { return 1360 }

func (l *fakeLink) run() {
	var held [][]byte
	deliver := func(b []byte) {
		fs, err := wire.ParseFrames(b)
		if err != nil {
			panic(err)
		}
		for _, f := range fs {
			l.peer.Handle(f)
		}
	}
	for {
		if len(held) == 0 {
			select {
			case b := <-l.q:
				held = append(held, b)
			case <-l.stop:
				return
			}
		}
	fill:
		for len(held) < l.reorder {
			select {
			case b := <-l.q:
				held = append(held, b)
			default:
				break fill
			}
		}
		if len(l.q) < fakeFull {
			select {
			case l.room <- struct{}{}:
			default:
			}
		}
		l.mu.Lock()
		i := l.rng.IntN(len(held))
		again := l.rng.Float64() < l.dup
		l.mu.Unlock()
		p := held[i]
		held = append(held[:i], held[i+1:]...)
		deliver(p)
		if again {
			deliver(p)
		}
	}
}

// pair is a hub session and a server session joined by fake links.
type pair struct {
	hub, srv *Session
	up, down *fakeLink
}

func newPair(t *testing.T, srvCfg, hubCfg Config, reorder int, dup float64) *pair {
	t.Helper()
	p := &pair{up: newFakeLink(reorder, dup, 1), down: newFakeLink(reorder, dup, 2)}
	hubCfg.Link, srvCfg.Link = p.up, p.down
	if srvCfg.Serve == nil {
		srvCfg.Serve = func(st *Stream, c net.Conn) { Pipe(st, c, nil) }
	}
	p.hub, p.srv = New(hubCfg), New(srvCfg)
	p.up.peer, p.down.peer = p.srv, p.hub
	go p.up.run()
	go p.down.run()
	t.Cleanup(func() {
		p.hub.Close()
		p.srv.Close()
		close(p.up.stop)
		close(p.down.stop)
	})
	return p
}

// tcpDial dials real loopback TCP (the origin).
func tcpDial(ctx context.Context, dst netip.AddrPort) (net.Conn, error) {
	var d net.Dialer
	return d.DialContext(ctx, "tcp4", dst.String())
}

// origin starts a loopback TCP server running fn per connection.
func origin(t *testing.T, fn func(c net.Conn)) netip.AddrPort {
	t.Helper()
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ln.Close() })
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				defer func() { _ = c.Close() }()
				fn(c)
			}()
		}
	}()
	return netip.MustParseAddrPort(ln.Addr().String())
}

func echo(c net.Conn) {
	_, _ = io.Copy(c, c)
	_ = c.(*net.TCPConn).CloseWrite()
}

func waitStreams(t *testing.T, s *Session, want int) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for s.Stats().Streams != want {
		if time.Now().After(deadline) {
			t.Fatalf("streams %d, want %d", s.Stats().Streams, want)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

func TestEchoThroughReorderingLink(t *testing.T) {
	tests := []struct {
		name    string
		reorder int
		dup     float64
		size    int
	}{
		{"in order", 1, 0, 1 << 20},
		{"reordered", 16, 0, 3 << 20},
		{"reordered with duplicates", 16, 0.1, 2 << 20},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dst := origin(t, echo)
			p := newPair(t, Config{Dial: tcpDial}, Config{Window: 256 << 10}, tt.reorder, tt.dup)
			st, err := p.hub.Open(context.Background(), dst)
			if err != nil {
				t.Fatal(err)
			}
			data := make([]byte, tt.size)
			_, _ = rand.Read(data)
			go func() {
				_, _ = st.Write(data)
				_ = st.CloseWrite()
			}()
			got, err := io.ReadAll(st)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(got, data) {
				t.Fatalf("echo: %d bytes back, want %d (equal prefix: %v)", len(got), len(data), bytes.HasPrefix(data, got))
			}
			_ = st.Close()
			waitStreams(t, p.hub, 0)
			waitStreams(t, p.srv, 0)
			if g := p.hub.Stats().Granted; g != 0 {
				t.Errorf("hub granted %d after close", g)
			}
		})
	}
}

func TestManyStreams(t *testing.T) {
	dst := origin(t, echo)
	p := newPair(t, Config{Dial: tcpDial}, Config{}, 8, 0.02)
	var wg sync.WaitGroup
	errc := make(chan error, 20)
	for i := range 20 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			st, err := p.hub.Open(context.Background(), dst)
			if err != nil {
				errc <- err
				return
			}
			msg := bytes.Repeat([]byte{byte(i)}, 50_000+i*1000)
			go func() {
				_, _ = st.Write(msg)
				_ = st.CloseWrite()
			}()
			got, err := io.ReadAll(st)
			if err == nil && !bytes.Equal(got, msg) {
				err = errors.New("stream data mixed up")
			}
			_ = st.Close()
			errc <- err
		}()
	}
	wg.Wait()
	close(errc)
	for err := range errc {
		if err != nil {
			t.Error(err)
		}
	}
	waitStreams(t, p.srv, 0)
}

func TestHalfCloseResponse(t *testing.T) {
	dst := origin(t, func(c net.Conn) {
		n, _ := io.Copy(io.Discard, c) // request until the client's FIN
		_, _ = c.Write([]byte("got " + string(rune('0'+n%10))))
	})
	p := newPair(t, Config{Dial: tcpDial}, Config{}, 4, 0)
	st, err := p.hub.Open(context.Background(), dst)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = st.Write([]byte("hello"))
	_ = st.CloseWrite()
	got, err := io.ReadAll(st)
	if err != nil || string(got) != "got 5" {
		t.Fatalf("response %q, %v", got, err)
	}
	_ = st.Close()
	waitStreams(t, p.srv, 0)
}

func TestOpenErrors(t *testing.T) {
	tests := []struct {
		name string
		dial func(context.Context, netip.AddrPort) (net.Conn, error)
		want uint8
	}{
		{"refused", func(context.Context, netip.AddrPort) (net.Conn, error) {
			return nil, &net.OpError{Op: "dial", Err: syscall.ECONNREFUSED}
		}, wire.OpenRefused},
		{"timeout", func(ctx context.Context, _ netip.AddrPort) (net.Conn, error) {
			<-ctx.Done()
			return nil, ctx.Err()
		}, wire.OpenTimeout},
		{"unreachable", func(context.Context, netip.AddrPort) (net.Conn, error) {
			return nil, errors.New("no route to host")
		}, wire.OpenUnreachable},
		{"server without dialer", nil, wire.OpenRefused},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			p := newPair(t, Config{Dial: tt.dial, DialWait: 100 * time.Millisecond}, Config{}, 1, 0)
			_, err := p.hub.Open(context.Background(), netip.MustParseAddrPort("192.0.2.1:80"))
			var oe *OpenError
			if !errors.As(err, &oe) || oe.Code != tt.want {
				t.Fatalf("Open: %v, want code %d", err, tt.want)
			}
			waitStreams(t, p.hub, 0)
			waitStreams(t, p.srv, 0)
		})
	}
}

func TestOpenTimesOutWithoutAnswer(t *testing.T) {
	l := newFakeLink(1, 0, 3) // nobody runs it: no answer ever
	s := New(Config{Link: l, OpenWait: 50 * time.Millisecond})
	defer s.Close()
	defer close(l.stop)
	_, err := s.Open(context.Background(), netip.MustParseAddrPort("192.0.2.1:80"))
	var oe *OpenError
	if !errors.As(err, &oe) || oe.Code != wire.OpenTimeout {
		t.Fatalf("Open: %v", err)
	}
	if n := s.Stats().Streams; n != 0 {
		t.Errorf("%d streams left", n)
	}
}

func TestStreamLimit(t *testing.T) {
	dst := origin(t, echo)
	p := newPair(t, Config{Dial: tcpDial, MaxStreams: 2}, Config{}, 1, 0)
	for range 2 {
		if _, err := p.hub.Open(context.Background(), dst); err != nil {
			t.Fatal(err)
		}
	}
	_, err := p.hub.Open(context.Background(), dst)
	var oe *OpenError
	if !errors.As(err, &oe) || oe.Code != wire.OpenRefused {
		t.Fatalf("third Open: %v, want refused", err)
	}
	hub := New(Config{Link: newFakeLink(1, 0, 4), MaxStreams: 1})
	defer hub.Close()
	go func() { _, _ = hub.Open(context.Background(), dst) }()
	time.Sleep(20 * time.Millisecond)
	if _, err := hub.Open(context.Background(), dst); !errors.Is(err, ErrLimit) {
		t.Errorf("local limit: %v", err)
	}
}

func TestFlowControlAndMemCap(t *testing.T) {
	const total = 1 << 20
	dst := origin(t, func(c net.Conn) { _, _ = c.Write(make([]byte, total)) })
	const window, memCap = 64 << 10, 100 << 10
	p := newPair(t, Config{Dial: tcpDial}, Config{Window: window, MemCap: memCap}, 4, 0)
	var sts []*Stream
	for range 3 {
		st, err := p.hub.Open(context.Background(), dst)
		if err != nil {
			t.Fatal(err)
		}
		sts = append(sts, st)
	}
	time.Sleep(300 * time.Millisecond) // origins write; nobody reads on the hub
	st := p.hub.Stats()
	// Over the cap every stream still gets a floor of Window/4.
	if limit := memCap + 3*window/4; st.Granted > limit || st.Buffered > limit {
		t.Fatalf("granted %d buffered %d over the cap %d", st.Granted, st.Buffered, limit)
	}
	for _, s := range sts {
		n, err := io.Copy(io.Discard, s)
		if err != nil || n != total {
			t.Fatalf("read %d, %v; want %d", n, err, total)
		}
		_ = s.Close()
	}
	waitStreams(t, p.hub, 0)
}

func TestWindowViolationResets(t *testing.T) {
	l := newFakeLink(1, 0, 5)
	l.record = true
	s := New(Config{Link: l, Window: 1000})
	defer s.Close()
	defer close(l.stop)
	s.mu.Lock()
	id := s.nextID + 1
	s.mu.Unlock()
	go func() { _, _ = s.Open(context.Background(), netip.MustParseAddrPort("192.0.2.1:80")) }()
	time.Sleep(20 * time.Millisecond)
	s.Handle(&wire.StreamOpenResult{StreamID: id, Code: wire.OpenOK})
	s.Handle(&wire.StreamData{StreamID: id, Offset: 900, Data: make([]byte, 200)})
	if n := s.Stats().Streams; n != 0 {
		t.Fatalf("stream kept after data past its window (%d streams)", n)
	}
	if !sentReset(t, l, id, ResetFlow) {
		t.Error("no STREAM_RESET flow sent")
	}
}

// sentReset reports whether l carried a STREAM_RESET for id with code.
func sentReset(t *testing.T, l *fakeLink, id uint32, code uint8) bool {
	t.Helper()
	l.mu.Lock()
	defer l.mu.Unlock()
	for _, b := range l.sent {
		fs, err := wire.ParseFrames(b)
		if err != nil {
			t.Fatal(err)
		}
		for _, f := range fs {
			if r, ok := f.(*wire.StreamReset); ok && r.StreamID == id && r.Code == code {
				return true
			}
		}
	}
	return false
}

func TestUnknownStreamAndTombstones(t *testing.T) {
	l := newFakeLink(1, 0, 6)
	l.record = true
	s := New(Config{Link: l})
	defer s.Close()
	defer close(l.stop)
	s.Handle(&wire.StreamData{StreamID: 99, Data: []byte("x")})
	if !sentReset(t, l, 99, ResetUnknown) {
		t.Fatal("unknown stream not reset")
	}
	l.mu.Lock()
	l.sent = nil
	l.mu.Unlock()
	s.Handle(&wire.StreamWindow{StreamID: 99, MaxOffset: 5}) // tombstoned now: ignored
	s.Handle(&wire.StreamReset{StreamID: 98})                // never answered
	if sentReset(t, l, 99, ResetUnknown) || sentReset(t, l, 98, ResetUnknown) {
		t.Error("answered a tombstoned id or a reset")
	}
	s.tick(time.Now().Add(TombstoneFor + time.Second))
	s.mu.Lock()
	n := len(s.tombs)
	s.mu.Unlock()
	if n != 0 {
		t.Errorf("%d tombstones after expiry", n)
	}
}

func TestDuplicateOpenDialsOnce(t *testing.T) {
	var mu sync.Mutex
	dials := 0
	l := newFakeLink(1, 0, 7)
	block := make(chan struct{})
	s := New(Config{Link: l, Dial: func(context.Context, netip.AddrPort) (net.Conn, error) {
		mu.Lock()
		dials++
		mu.Unlock()
		<-block
		return nil, errors.New("down")
	}, Serve: func(*Stream, net.Conn) {}})
	open := &wire.StreamOpen{StreamID: 7, Dst: netip.MustParseAddrPort("192.0.2.1:80")}
	s.Handle(open)
	s.Handle(open)
	time.Sleep(20 * time.Millisecond)
	close(block)
	time.Sleep(20 * time.Millisecond)
	s.Handle(open) // after the failure: tombstoned
	time.Sleep(20 * time.Millisecond)
	s.Close()
	close(l.stop)
	if dials != 1 {
		t.Errorf("%d dials, want 1", dials)
	}
}

func TestIdleAndSessionClose(t *testing.T) {
	dst := origin(t, echo)
	p := newPair(t, Config{Dial: tcpDial}, Config{Idle: time.Hour}, 1, 0)
	st, err := p.hub.Open(context.Background(), dst)
	if err != nil {
		t.Fatal(err)
	}
	p.hub.tick(time.Now().Add(2 * time.Hour))
	if _, err := st.Read(make([]byte, 1)); !errors.As(err, new(*ResetError)) {
		t.Fatalf("read after idle timeout: %v", err)
	}
	waitStreams(t, p.srv, 0) // the reset reached the server
	st2, err := p.hub.Open(context.Background(), dst)
	if err != nil {
		t.Fatal(err)
	}
	p.hub.Close()
	if _, err := st2.Write([]byte("x")); err == nil {
		t.Error("write after session close")
	}
	if _, err := p.hub.Open(context.Background(), dst); !errors.Is(err, ErrClosed) {
		t.Errorf("Open after Close: %v", err)
	}
}

func TestOriginResetReachesClient(t *testing.T) {
	dst := origin(t, func(c net.Conn) {
		_ = c.(*net.TCPConn).SetLinger(0)
		buf := make([]byte, 1)
		_, _ = c.Read(buf)
	})
	p := newPair(t, Config{Dial: tcpDial}, Config{}, 1, 0)
	st, err := p.hub.Open(context.Background(), dst)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = st.Write([]byte("x"))
	_, err = io.ReadAll(st)
	var re *ResetError
	if !errors.As(err, &re) || !re.Remote {
		t.Fatalf("read: %v, want a reset by the peer", err)
	}
}

func TestAllocIDSkipsZeroAndUsed(t *testing.T) {
	l := newFakeLink(1, 0, 8)
	s := New(Config{Link: l})
	defer s.Close()
	defer close(l.stop)
	s.mu.Lock()
	s.nextID = ^uint32(0) - 1
	s.streams[^uint32(0)] = newStream(s, ^uint32(0), netip.AddrPort{}, true)
	s.tombs[1] = time.Now()
	got := s.allocIDLocked()
	s.mu.Unlock()
	if got != 2 {
		t.Errorf("id %d, want 2", got)
	}
}
