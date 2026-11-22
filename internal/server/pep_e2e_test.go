package server_test

import (
	"bytes"
	"context"
	"crypto/rand"
	"io"
	"net"
	"net/netip"
	"sync"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/server"
)

// TestPEPStreamThroughSession opens a PEP stream from a real hub client
// through a real server session; the server's origin dial is redirected to
// a loopback echo server.
func TestPEPStreamThroughSession(t *testing.T) {
	ln, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = ln.Close() }()
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go func() {
				_, _ = io.Copy(c, c)
				_ = c.(*net.TCPConn).CloseWrite()
				_ = c.Close()
			}()
		}
	}()
	tests := []struct {
		name string
		pep  bool
	}{{"server accepts streams", true}, {"server without PEP", false}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var mu sync.Mutex
			var dialed []netip.AddrPort
			e := startServerWith(t, func(o *server.Options) {
				o.Config.PEP = tt.pep
				o.DialOrigin = func(ctx context.Context, dst netip.AddrPort) (net.Conn, error) {
					mu.Lock()
					dialed = append(dialed, dst)
					mu.Unlock()
					var d net.Dialer
					return d.DialContext(ctx, "tcp4", ln.Addr().String())
				}
			})
			res, err := e.ctl.Enroll(e.ctx, "p1")
			if err != nil {
				t.Fatal(err)
			}
			dir, err := e.enrollHub(t, res.Code)
			if err != nil {
				t.Fatal(err)
			}
			_, _, _, cancel, c := connectClient(t, e, dir)
			defer cancel()
			deadline := time.Now().Add(5 * time.Second)
			for !c.Up() && time.Now().Before(deadline) {
				time.Sleep(10 * time.Millisecond)
			}
			if !c.Up() {
				t.Fatal("session not up")
			}
			if !tt.pep {
				if c.PEP() != nil {
					t.Fatal("hub uses PEP although the server did not announce it")
				}
				return
			}
			ps := c.PEP()
			if ps == nil {
				t.Fatal("no PEP session although the server announced it")
			}
			dst := netip.MustParseAddrPort("203.0.113.9:443")
			st, err := ps.Open(context.Background(), dst)
			if err != nil {
				t.Fatal(err)
			}
			data := make([]byte, 2<<20)
			_, _ = rand.Read(data)
			go func() {
				_, _ = st.Write(data)
				_ = st.CloseWrite()
			}()
			got, err := io.ReadAll(st)
			if err != nil || !bytes.Equal(got, data) {
				t.Fatalf("echo through the session: %d of %d bytes, %v", len(got), len(data), err)
			}
			_ = st.Close()
			mu.Lock()
			defer mu.Unlock()
			if len(dialed) != 1 || dialed[0] != dst {
				t.Errorf("server dialed %v, want [%s]", dialed, dst)
			}
		})
	}
}
