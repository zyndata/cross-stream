package transport

import (
	"bytes"
	"context"
	"errors"
	"net"
	"net/netip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/wire"
)

// echoServer starts TCP and WSS listeners whose handler echoes every packet
// back with its first byte incremented.
func echoServer(t *testing.T, opts ...func(*Server)) (tcp, wss wire.Endpoint, sv *Server) {
	t.Helper()
	sv = &Server{Handler: func(pkt []byte, from netip.AddrPort, s *ServerConn) {
		if !from.IsValid() || s.RemoteAddr() != from {
			t.Errorf("from %v, conn %v", from, s.RemoteAddr())
		}
		out := append([]byte(nil), pkt...)
		out[0]++
		_, _ = s.WriteToUDPAddrPort(out, netip.AddrPort{})
	}}
	for _, o := range opts {
		o(sv)
	}
	cert, err := LoadOrCreateCert(t.TempDir(), "srv.test")
	if err != nil {
		t.Fatal(err)
	}
	ln1, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	ln2, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = sv.ServeTCP(ln1) }()
	go func() { _ = sv.ServeWSS(ln2, cert, "/xs") }()
	t.Cleanup(sv.Close)
	p1 := uint16(ln1.Addr().(*net.TCPAddr).Port)
	p2 := uint16(ln2.Addr().(*net.TCPAddr).Port)
	return wire.Endpoint{Transport: wire.TransportTCP, Host: "127.0.0.1", Port: p1},
		wire.Endpoint{Transport: wire.TransportWSS, Host: "srv.test", Port: p2, Path: "/xs"}, sv
}

func localLookup(context.Context, string, string) ([]netip.Addr, error) {
	return []netip.Addr{netip.MustParseAddr("127.0.0.1")}, nil
}

func TestStreamRoundTrip(t *testing.T) {
	tcp, wss, _ := echoServer(t)
	d := Dialer{Lookup: localLookup, Timeout: 5 * time.Second}
	for _, ep := range []wire.Endpoint{tcp, wss} {
		t.Run(string(ep.Transport), func(t *testing.T) {
			c, err := d.Dial(context.Background(), ep)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = c.Close() }()
			if c.Transport() != ep.Transport {
				t.Fatalf("transport %s", c.Transport())
			}
			sizes := []int{1, 14, 1400, MaxPacket}
			for _, n := range sizes {
				pkt := bytes.Repeat([]byte{7}, n)
				if err := c.Send(pkt); err != nil {
					t.Fatal(err)
				}
			}
			buf := make([]byte, MaxPacket)
			for _, n := range sizes {
				got, err := c.Recv(buf)
				if err != nil {
					t.Fatal(err)
				}
				if got != n || buf[0] != 8 || (n > 1 && buf[n-1] != 7) {
					t.Fatalf("got %d bytes first %d, want %d", got, buf[0], n)
				}
			}
			if err := c.Send(make([]byte, MaxPacket+1)); err == nil {
				t.Fatal("oversized packet accepted")
			}
		})
	}
}

func TestUDPConn(t *testing.T) {
	srv, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = srv.Close() }()
	go func() {
		buf := make([]byte, 2000)
		for {
			n, from, err := srv.ReadFromUDPAddrPort(buf)
			if err != nil {
				return
			}
			_, _ = srv.WriteToUDPAddrPort(buf[:n], from)
		}
	}()
	ep := wire.Endpoint{Transport: wire.TransportUDP, Host: "127.0.0.1", Port: uint16(srv.LocalAddr().(*net.UDPAddr).Port)}
	c, err := Dialer{}.Dial(context.Background(), ep)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Send([]byte("hello")); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 100)
	n, err := c.Recv(buf)
	if err != nil || string(buf[:n]) != "hello" || c.Transport() != wire.TransportUDP {
		t.Fatalf("recv %q %v", buf[:n], err)
	}
}

func TestServerCloseEndsConnections(t *testing.T) {
	tcp, _, sv := echoServer(t)
	c, err := Dialer{}.Dial(context.Background(), tcp)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = c.Close() }()
	if err := c.Send([]byte{1}); err != nil {
		t.Fatal(err)
	}
	buf := make([]byte, 10)
	if _, err := c.Recv(buf); err != nil {
		t.Fatal(err)
	}
	sv.Close()
	if _, err := c.Recv(buf); err == nil {
		t.Fatal("recv after server close")
	}
}

func TestResolveError(t *testing.T) {
	d := Dialer{Lookup: func(context.Context, string, string) ([]netip.Addr, error) {
		return nil, errors.New("no network")
	}}
	_, err := d.Dial(context.Background(), wire.Endpoint{Transport: wire.TransportTCP, Host: "srv.test", Port: 1})
	if !errors.Is(err, ErrResolve) {
		t.Fatalf("err %v, want ErrResolve", err)
	}
}

func TestWriterQueueFull(t *testing.T) {
	block := make(chan struct{})
	w := newWriter(func([]byte) error { <-block; return nil })
	defer close(block)
	var full bool
	for range SendQueue + 2 {
		if err := w.send([]byte{1}); errors.Is(err, ErrQueueFull) {
			full = true
			break
		}
	}
	if !full {
		t.Fatal("queue never full")
	}
	w.stop(net.ErrClosed)
	if err := w.send([]byte{1}); !errors.Is(err, net.ErrClosed) {
		t.Fatalf("send after stop: %v", err)
	}
}

func TestLoadOrCreateCert(t *testing.T) {
	dir := t.TempDir()
	a, err := LoadOrCreateCert(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	b, err := LoadOrCreateCert(dir, "")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Certificate[0], b.Certificate[0]) {
		t.Fatal("certificate not reused")
	}
	if err := os.WriteFile(filepath.Join(dir, CertFile), []byte("junk"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadOrCreateCert(dir, ""); err == nil {
		t.Fatal("corrupt certificate accepted")
	}
}

func TestPerIPConnectionCap(t *testing.T) {
	refused := make(chan netip.Addr, 4)
	tcp, wss, _ := echoServer(t, func(s *Server) {
		s.MaxPerIP = 2
		s.OnRefuse = func(a netip.Addr) { refused <- a }
	})
	d := Dialer{Lookup: localLookup}
	buf := make([]byte, 10)
	var open []Conn
	defer func() {
		for _, c := range open {
			_ = c.Close()
		}
	}()
	for _, ep := range []wire.Endpoint{tcp, wss} { // TCP and WSS count together
		c, err := d.Dial(context.Background(), ep)
		if err != nil {
			t.Fatal(err)
		}
		open = append(open, c)
		if err := c.Send([]byte{1}); err != nil {
			t.Fatal(err)
		}
		if _, err := c.Recv(buf); err != nil {
			t.Fatalf("%v: %v", ep.Transport, err)
		}
	}
	c, err := d.Dial(context.Background(), tcp)
	if err != nil {
		t.Fatal(err)
	}
	_ = c.Send([]byte{1})
	if _, err := c.Recv(buf); err == nil {
		t.Fatal("third connection from one IP served")
	}
	_ = c.Close()
	select {
	case a := <-refused:
		if a != netip.MustParseAddr("127.0.0.1") {
			t.Fatalf("refused %v", a)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("OnRefuse not called")
	}
	_ = open[0].Close() // a slot frees up
	open = open[1:]
	deadline := time.Now().Add(5 * time.Second)
	for {
		c, err := d.Dial(context.Background(), tcp)
		if err != nil {
			t.Fatal(err)
		}
		_ = c.Send([]byte{1})
		_, err = c.Recv(buf)
		if err == nil {
			open = append(open, c)
			break
		}
		_ = c.Close()
		if time.Now().After(deadline) {
			t.Fatal("no connection accepted after one closed")
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestFirstPacketTimeout(t *testing.T) {
	tcp, wss, _ := echoServer(t, func(s *Server) { s.FirstPacket = 200 * time.Millisecond })
	d := Dialer{Lookup: localLookup}
	for _, ep := range []wire.Endpoint{tcp, wss} {
		c, err := d.Dial(context.Background(), ep)
		if err != nil {
			t.Fatal(err)
		}
		start := time.Now()
		if _, err := c.Recv(make([]byte, 10)); err == nil {
			t.Fatalf("%v: silent connection got data", ep.Transport)
		}
		if el := time.Since(start); el > 3*time.Second {
			t.Fatalf("%v: closed after %v, want ~200 ms", ep.Transport, el)
		}
		_ = c.Close()
	}
}
