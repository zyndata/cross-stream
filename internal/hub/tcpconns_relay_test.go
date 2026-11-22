package hub

import (
	"context"
	"errors"
	"log/slog"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	"github.com/zyndata/cross-stream/internal/control"
	"github.com/zyndata/cross-stream/internal/crypto"
	"github.com/zyndata/cross-stream/internal/pairing"
	"github.com/zyndata/cross-stream/internal/tun"
)

// TestRelayTCPConnsPolicy: a relay on a tcp:// endpoint is welcomed with
// TCPConnsDefault connections; when its stats show a high retransmission
// ratio the hub sends the same endpoint with TCPConnsLossy, and back after a
// calm window. The override fixes the number.
func TestRelayTCPConnsPolicy(t *testing.T) {
	tests := []struct {
		name                        string
		override                    int
		wantWelcome, wantLossy, end int
	}{
		{"policy", 0, TCPConnsDefault, TCPConnsLossy, TCPConnsDefault},
		{"override", 3, 3, 0, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var clock atomic.Int64
			clock.Store(time.Unix(1_700_000_000, 0).UnixNano())
			ctl, d := startHubFakeRelay(t, tt.override, func() time.Time { return time.Unix(0, clock.Load()) })
			m, err := ctl.Recv()
			if err != nil {
				t.Fatal(err)
			}
			w, ok := m.(*control.Welcome)
			if !ok || w.TCPConns != tt.wantWelcome || w.ServerEP != "tcp://127.0.0.1:9" {
				t.Fatalf("welcome %#v", m)
			}
			var sent, retrans uint64
			// one stats message per fake second; the clock moves only after
			// the hub has taken the previous one
			stats := func(secs int, loss float64) {
				for range secs {
					sent += 1000
					retrans += uint64(loss * 1000)
					if err := ctl.Send(&control.Stats{Battery: 100, CellularOK: true, TCPConns: 2, TCPRetrans: retrans, TCPSent: sent}); err != nil {
						t.Fatal(err)
					}
					eventually(t, "stats seen", func() bool {
						st := d.Status()
						return len(st.Relays) == 1 && st.Relays[0].Stats != nil && st.Relays[0].Stats.TCPSent == sent
					})
					clock.Add(int64(time.Second))
				}
			}
			stats(12, 0.06)
			if tt.wantLossy == 0 {
				expectNoEndpoint(t, ctl)
				return
			}
			expectEndpoint(t, ctl, w.ServerEP, tt.wantLossy)
			stats(35, 0)
			expectEndpoint(t, ctl, w.ServerEP, tt.end)
		})
	}
}

func expectEndpoint(t *testing.T, ctl *control.Conn, ep string, n int) {
	t.Helper()
	_ = ctl.SetReadDeadline(time.Now().Add(5 * time.Second))
	for {
		m, err := ctl.Recv()
		if err != nil {
			t.Fatalf("waiting for endpoint with %d connections: %v", n, err)
		}
		if e, ok := m.(*control.Endpoint); ok {
			if e.ServerEP != ep || e.TCPConns != n {
				t.Fatalf("endpoint %#v, want %s with %d connections", e, ep, n)
			}
			return
		}
	}
}

func expectNoEndpoint(t *testing.T, ctl *control.Conn) {
	t.Helper()
	_ = ctl.SetReadDeadline(time.Now().Add(300 * time.Millisecond))
	for {
		m, err := ctl.Recv()
		var ne net.Error
		if errors.As(err, &ne) && ne.Timeout() {
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		if e, ok := m.(*control.Endpoint); ok {
			t.Fatalf("unexpected endpoint %#v", e)
		}
	}
}

// startHubFakeRelay runs a daemon enrolled with a tcp:// endpoint and pairs
// a fake relay over a real control connection (hello sent, welcome unread).
func startHubFakeRelay(t *testing.T, tcpConns int, now func() time.Time) (*control.Conn, *Daemon) {
	t.Helper()
	dir := t.TempDir()
	srv, _ := crypto.GenerateKeypair()
	if err := SaveEnrollment(dir, Enrollment{Name: "hub1", ServerKey: crypto.EncodeKey(srv.Public),
		Endpoints: []string{"tcp://127.0.0.1:9"}, TunnelIP: "100.64.0.2", MTU: 1280}); err != nil {
		t.Fatal(err)
	}
	d, err := NewDaemon(DaemonConfig{Dir: dir, CtlPort: -1, DataPort: -1, ProxyPort: freePort(t), Version: "test",
		LanPrefix: netip.MustParsePrefix("127.0.0.1/8"), Log: slog.New(slog.DiscardHandler), TCPConns: tcpConns,
		StatsNow: now, NewTUN: func(c tun.Config) (tun.Device, error) {
			dev, _ := tun.Pipe(c.Name, "app", c.MTU)
			return dev, nil
		}})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- d.Run(ctx) }()
	t.Cleanup(func() {
		cancel()
		if err := <-done; err != nil {
			t.Errorf("daemon run: %v", err)
		}
	})
	code, err := d.PairCode()
	if err != nil {
		t.Fatal(err)
	}
	p, _ := pairing.Decode(code)
	hk, _ := p.HubKey()
	key, _ := crypto.GenerateKeypair()
	ctl, err := control.Dial(ctx, &net.Dialer{Timeout: 5 * time.Second}, p.ControlAddr().String(), key, hk)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = ctl.Close() })
	if err := ctl.Send(&control.Hello{RelayName: "anna", PairToken: p.Tok}); err != nil {
		t.Fatal(err)
	}
	return ctl, d
}
